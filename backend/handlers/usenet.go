package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"novastream/internal/httpheaders"
	"novastream/models"
	usenetsvc "novastream/services/usenet"
)

type usenetHealthService interface {
	CheckHealth(ctx context.Context, candidate models.NZBResult) (*models.NZBHealthCheck, error)
}

// nzbImporter is the subset of the importer service used for track probing.
type nzbImporter interface {
	ProcessNZBImmediately(ctx context.Context, fileName string, nzbBytes []byte) (string, error)
}

// usenetTrackProber probes a Usenet NZB for audio/subtitle tracks via WebDAV + ffprobe.
type usenetTrackProber struct {
	importer     nzbImporter
	httpClient   *http.Client
	ffprobePath  string
	webdavBase   string // e.g. "http://user:pass@127.0.0.1:7777"
	webdavPrefix string // e.g. "/webdav"
}

// probe fetches the NZB, registers it in the virtual FS, then runs ffprobe via WebDAV.
// Returns (audioTracks, subtitleTracks, errMsg). errMsg is non-empty on failure.
func (p *usenetTrackProber) probe(ctx context.Context, candidate models.NZBResult) ([]models.NZBAudioTrack, []models.NZBSubtitleTrack, string) {
	downloadURL := strings.TrimSpace(candidate.DownloadURL)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(candidate.Link)
	}
	if downloadURL == "" {
		return nil, nil, "no download URL in candidate"
	}

	// Fetch NZB bytes (second fetch; health check already fetched internally)
	nzbBytes, fileName, err := p.fetchNZB(ctx, downloadURL)
	if err != nil {
		return nil, nil, fmt.Sprintf("fetch NZB: %v", err)
	}

	// Register NZB in virtual FS to get the file's storage path
	processCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	storagePath, err := p.importer.ProcessNZBImmediately(processCtx, fileName, nzbBytes)
	if err != nil {
		return nil, nil, fmt.Sprintf("process NZB: %v", err)
	}

	// Only probe single video files; skip directories and non-video archives
	if !isVideoFilePath(storagePath) {
		return nil, nil, "NZB resolves to a directory or non-video file; track probing unsupported"
	}

	// Build WebDAV URL for ffprobe
	probeURL := p.buildProbeURL(storagePath)
	if probeURL == "" {
		return nil, nil, "WebDAV not configured"
	}

	audio, subs, probeErr := p.runFFProbe(ctx, probeURL)
	if probeErr != nil {
		return nil, nil, fmt.Sprintf("ffprobe: %v", probeErr)
	}

	log.Printf("[usenet-tracks] probe complete title=%q audio=%d subtitle=%d", candidate.Title, len(audio), len(subs))
	return audio, subs, ""
}

func (p *usenetTrackProber) fetchNZB(ctx context.Context, downloadURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	httpheaders.SetNZBDownloadHeaders(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, nzbFileNameFromResponse(resp, downloadURL), nil
}

func (p *usenetTrackProber) buildProbeURL(storagePath string) string {
	base := p.webdavBase
	prefix := p.webdavPrefix
	if base == "" || prefix == "" {
		return ""
	}
	pathToUse := storagePath
	if !strings.HasPrefix(pathToUse, "/") {
		pathToUse = "/" + pathToUse
	}
	if !strings.HasPrefix(pathToUse, prefix) {
		pathToUse = prefix + pathToUse
	}
	return base + pathToUse
}

func (p *usenetTrackProber) runFFProbe(ctx context.Context, probeURL string) ([]models.NZBAudioTrack, []models.NZBSubtitleTrack, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-analyzeduration", "10000000",
		"-probesize", "10000000",
		probeURL,
	}

	cmd := exec.CommandContext(probeCtx, p.ffprobePath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("%w (stderr: %s)", err, stderr.String())
	}

	var result struct {
		Streams []struct {
			Index       int               `json:"index"`
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, nil, fmt.Errorf("parse output: %w", err)
	}

	var audioTracks []models.NZBAudioTrack
	var subtitleTracks []models.NZBSubtitleTrack

	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		lang, title := "", ""
		if stream.Tags != nil {
			lang = stream.Tags["language"]
			title = stream.Tags["title"]
		}
		switch stream.CodecType {
		case "audio":
			audioTracks = append(audioTracks, models.NZBAudioTrack{
				Index:    stream.Index,
				Language: lang,
				Codec:    codec,
				Title:    title,
			})
		case "subtitle":
			isForced := stream.Disposition != nil && stream.Disposition["forced"] > 0
			isBitmap, bitmapType := nzbBitmapSubtitleCodec(codec)
			subtitleTracks = append(subtitleTracks, models.NZBSubtitleTrack{
				Index:      stream.Index,
				Language:   lang,
				Codec:      codec,
				Title:      title,
				Forced:     isForced,
				IsBitmap:   isBitmap,
				BitmapType: bitmapType,
			})
		}
	}

	return audioTracks, subtitleTracks, nil
}

// isVideoFilePath returns true when path ends with a recognised video extension.
func isVideoFilePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".m2ts":
		return true
	}
	return false
}

// nzbBitmapSubtitleCodec returns (isBitmap, displayType) for a codec name.
func nzbBitmapSubtitleCodec(codec string) (bool, string) {
	switch codec {
	case "hdmv_pgs_subtitle", "pgssub":
		return true, "PGS"
	case "dvd_subtitle", "dvdsub":
		return true, "VOBSUB"
	}
	return false, ""
}

// nzbFileNameFromResponse extracts a filename from an HTTP response or falls back to the URL path.
func nzbFileNameFromResponse(resp *http.Response, downloadURL string) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if i := strings.Index(cd, "filename="); i >= 0 {
			name := strings.Trim(strings.TrimPrefix(cd[i:], "filename="), `"`)
			if name != "" {
				return name
			}
		}
	}
	if u, err := url.Parse(downloadURL); err == nil {
		if base := filepath.Base(u.Path); base != "." && base != "/" {
			return base
		}
	}
	return "download.nzb"
}

// UsenetHandler exposes endpoints for NNTP-backed NZB health checks.
type UsenetHandler struct {
	Service     usenetHealthService
	trackProber *usenetTrackProber
}

var _ usenetHealthService = (*usenetsvc.Service)(nil)

func NewUsenetHandler(s usenetHealthService) *UsenetHandler {
	return &UsenetHandler{Service: s}
}

// ConfigureTrackProbing enables optional audio/subtitle track probing via WebDAV + ffprobe.
// When configured the handler probes tracks whenever probeForTracks=true arrives in the request.
// Requires all four parameters to be non-empty; silently skips configuration otherwise.
func (h *UsenetHandler) ConfigureTrackProbing(importer nzbImporter, ffprobePath, baseURL, prefix, username, password string) {
	if importer == nil || ffprobePath == "" || baseURL == "" || prefix == "" {
		return
	}
	// Build base URL with embedded credentials, mirroring ConfigureLocalWebDAVAccess in video.go.
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		log.Printf("[usenet-tracks] invalid WebDAV base URL %q: %v", baseURL, err)
		return
	}
	if username != "" {
		parsed.User = url.UserPassword(username, password)
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	h.trackProber = &usenetTrackProber{
		importer:     importer,
		httpClient:   &http.Client{Timeout: 60 * time.Second},
		ffprobePath:  ffprobePath,
		webdavBase:   strings.TrimRight(parsed.String(), "/"),
		webdavPrefix: "/" + strings.Trim(prefix, "/"),
	}
	log.Printf("[usenet-tracks] track probing configured via WebDAV")
}

// CheckHealth accepts an NZB indexer result and returns segment availability from Usenet.
// When probeForTracks=true in the request body, also probes audio/subtitle tracks via WebDAV+ffprobe.
func (h *UsenetHandler) CheckHealth(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Result         models.NZBResult `json:"result"`
		ProbeForTracks bool             `json:"probeForTracks,omitempty"`
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	res, err := h.Service.CheckHealth(r.Context(), request.Result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Probe for tracks when requested and the file is healthy.
	if request.ProbeForTracks && res != nil && res.Healthy {
		res.TracksProbed = true
		if h.trackProber != nil {
			audio, subs, probeErr := h.trackProber.probe(r.Context(), request.Result)
			if probeErr != "" {
				log.Printf("[usenet-tracks] probe failed for %q: %s", request.Result.Title, probeErr)
				res.TrackProbeError = probeErr
			} else {
				res.AudioTracks = audio
				res.SubtitleTracks = subs
			}
		} else {
			res.TrackProbeError = "track probing not configured (requires WebDAV and ffprobe)"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
