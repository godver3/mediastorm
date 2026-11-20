package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novastream/internal/mediaresolve"
	"novastream/internal/nzbfetch"
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

type usenetProbeMetadataService interface {
	ListDirectory(virtualPath string) ([]string, error)
	ListSubdirectories(virtualPath string) ([]string, error)
}

type externalUsenetTrackResolver interface {
	ResolveExternalUsenetForProbe(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (streamURL string, authHeader string, handled bool, err error)
}

// usenetTrackProber probes a Usenet NZB for audio/subtitle tracks via WebDAV + ffprobe.
type usenetTrackProber struct {
	importer         nzbImporter
	metadata         usenetProbeMetadataService
	externalResolver externalUsenetTrackResolver
	httpClient       *http.Client
	ffprobePath      string
	webdavBase       string // e.g. "http://user:pass@127.0.0.1:7777"
	webdavPrefix     string // e.g. "/webdav"
	// userAgentFor returns the per-indexer User-Agent override, if any.
	userAgentFor func(indexer string) string
}

type usenetProbeMediaCandidate struct {
	path     string
	priority int
}

const usenetTrackProbeScanMaxDepth = 4

var usenetTrackProbePlayableExtensionPriority = map[string]int{
	".mkv":  1,
	".mp4":  2,
	".m4v":  3,
	".avi":  4,
	".mov":  5,
	".wmv":  6,
	".ts":   7,
	".m2ts": 8,
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

	// Reuse the shared acquisition cache when the title budget is enabled.
	userAgent := ""
	if p.userAgentFor != nil {
		userAgent = p.userAgentFor(candidate.Indexer)
	}
	nzbBytes, fileName, err := p.fetchNZB(ctx, downloadURL, userAgent, candidate)
	if err != nil {
		return nil, nil, fmt.Sprintf("fetch NZB: %v", err)
	}

	// Detach the heavy WebDAV/Usenet work from the request context so a client
	// navigation/abort (AbortController) doesn't SIGKILL an in-progress ffprobe.
	// Large remuxes serve probe bytes slowly over WebDAV-from-Usenet and lose the
	// race against client cancellation; letting the probe finish on its own budget
	// lets the result be cached for the next attempt. Own timeouts still bound it.
	detached := context.WithoutCancel(ctx)

	if p.externalResolver != nil {
		streamURL, authHeader, handled, err := p.externalResolver.ResolveExternalUsenetForProbe(detached, candidate, nzbBytes, fileName)
		if handled {
			if err != nil {
				return nil, nil, fmt.Sprintf("external usenet resolve: %v", err)
			}
			if strings.TrimSpace(streamURL) == "" {
				return nil, nil, "external usenet resolve returned no stream URL"
			}
			audio, subs, probeErr := p.runFFProbe(detached, streamURL, authHeader)
			if probeErr != nil {
				return nil, nil, fmt.Sprintf("ffprobe: %v", probeErr)
			}
			log.Printf("[usenet-tracks] external probe complete title=%q audio=%d subtitle=%d", candidate.Title, len(audio), len(subs))
			return audio, subs, ""
		}
	}

	// Register NZB in virtual FS to get the file's storage path
	processCtx, cancel := context.WithTimeout(detached, 60*time.Second)
	defer cancel()
	storagePath, err := p.importer.ProcessNZBImmediately(processCtx, fileName, nzbBytes)
	if err != nil {
		return nil, nil, fmt.Sprintf("process NZB: %v", err)
	}

	resolvedPath, err := p.resolveProbeMediaPath(candidate, storagePath)
	if err != nil {
		return nil, nil, err.Error()
	}

	// Build WebDAV URL for ffprobe
	probeURL := p.buildProbeURL(resolvedPath)
	if probeURL == "" {
		return nil, nil, "WebDAV not configured"
	}

	audio, subs, probeErr := p.runFFProbe(detached, probeURL, "")
	if probeErr != nil {
		return nil, nil, fmt.Sprintf("ffprobe: %v", probeErr)
	}

	log.Printf("[usenet-tracks] probe complete title=%q audio=%d subtitle=%d", candidate.Title, len(audio), len(subs))
	return audio, subs, ""
}

func (p *usenetTrackProber) resolveProbeMediaPath(candidate models.NZBResult, storagePath string) (string, error) {
	if isVideoFilePath(storagePath) {
		return storagePath, nil
	}
	if p.metadata == nil || !isLikelyUsenetProbeDirectory(storagePath) {
		return "", fmt.Errorf("NZB resolves to a directory or non-video file; track probing unsupported")
	}

	hints := buildUsenetProbeSelectionHints(candidate, storagePath)
	mediaPath, err := p.findBestProbeMediaFile(storagePath, hints)
	if err != nil {
		return "", fmt.Errorf("resolve media file: %w", err)
	}
	return mediaPath, nil
}

func buildUsenetProbeSelectionHints(candidate models.NZBResult, directory string) mediaresolve.SelectionHints {
	hints := mediaresolve.SelectionHints{
		ReleaseTitle: candidate.Title,
		QueueName:    candidate.GUID,
		Directory:    directory,
	}
	if candidate.Attributes == nil {
		return hints
	}
	if code := strings.TrimSpace(candidate.Attributes["targetEpisodeCode"]); code != "" {
		hints.TargetEpisodeCode = code
	}
	if season, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["targetSeason"])); season > 0 {
		hints.TargetSeason = season
	}
	if episode, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["targetEpisode"])); episode > 0 {
		hints.TargetEpisode = episode
	}
	if hints.TargetEpisodeCode == "" && hints.TargetSeason > 0 && hints.TargetEpisode > 0 {
		hints.TargetEpisodeCode = fmt.Sprintf("S%02dE%02d", hints.TargetSeason, hints.TargetEpisode)
	}
	if absolute, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["targetAbsoluteEpisode"])); absolute > 0 {
		hints.AbsoluteEpisodeNumber = absolute
	} else if absolute, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["absoluteEpisodeNumber"])); absolute > 0 {
		hints.AbsoluteEpisodeNumber = absolute
	}
	if strings.TrimSpace(candidate.Attributes["isDaily"]) == "true" {
		hints.IsDaily = true
	}
	if airDate := strings.TrimSpace(candidate.Attributes["targetAirDate"]); airDate != "" {
		hints.TargetAirDate = airDate
	}
	return hints
}

func (p *usenetTrackProber) findBestProbeMediaFile(dirPath string, hints mediaresolve.SelectionHints) (string, error) {
	var candidates []usenetProbeMediaCandidate
	var resolverCandidates []mediaresolve.Candidate
	bestIdx := -1

	var scan func(string, int) error
	scan = func(currentPath string, depth int) error {
		if depth > usenetTrackProbeScanMaxDepth {
			return nil
		}

		files, err := p.metadata.ListDirectory(currentPath)
		if err != nil {
			log.Printf("[usenet-tracks] failed to list directory %q: %v", currentPath, err)
			return err
		}
		for _, filename := range files {
			ext := strings.ToLower(path.Ext(filename))
			priority, playable := usenetTrackProbePlayableExtensionPriority[ext]
			if !playable {
				continue
			}
			lowerName := strings.ToLower(filename)
			if strings.Contains(lowerName, "sample") || strings.Contains(lowerName, "extras") {
				log.Printf("[usenet-tracks] skipping sample/extras file: %q", filename)
				continue
			}

			filePath := path.Join(currentPath, filename)
			candidates = append(candidates, usenetProbeMediaCandidate{path: filePath, priority: priority})
			resolverCandidates = append(resolverCandidates, mediaresolve.Candidate{Label: filePath, Priority: priority})
			idx := len(candidates) - 1
			if bestIdx == -1 || candidates[idx].priority < candidates[bestIdx].priority {
				bestIdx = idx
			}
		}

		subdirs, err := p.metadata.ListSubdirectories(currentPath)
		if err != nil {
			log.Printf("[usenet-tracks] failed to list subdirectories in %q: %v", currentPath, err)
			return err
		}
		for _, subdir := range subdirs {
			lowerDir := strings.ToLower(subdir)
			if lowerDir == "sample" || lowerDir == "samples" || lowerDir == "extras" || lowerDir == "extra" {
				log.Printf("[usenet-tracks] skipping sample/extras directory: %q", subdir)
				continue
			}
			if err := scan(path.Join(currentPath, subdir), depth+1); err != nil {
				log.Printf("[usenet-tracks] error scanning subdirectory %q: %v", subdir, err)
			}
		}
		return nil
	}

	if err := scan(dirPath, 0); err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no playable media files found")
	}
	if len(candidates) == 1 {
		log.Printf("[usenet-tracks] only playable file found; selecting %q", candidates[0].path)
		return candidates[0].path, nil
	}

	selectorHints := hints
	if strings.TrimSpace(selectorHints.Directory) == "" {
		selectorHints.Directory = dirPath
	}
	selectedIdx, reason := mediaresolve.SelectBestCandidate(resolverCandidates, selectorHints)
	if selectedIdx != -1 {
		if strings.TrimSpace(reason) == "" {
			reason = "heuristic match"
		}
		log.Printf("[usenet-tracks] selected media candidate %q (%s)", candidates[selectedIdx].path, reason)
		return candidates[selectedIdx].path, nil
	}
	if bestIdx != -1 {
		log.Printf("[usenet-tracks] selector did not find a definitive match; falling back to %q", candidates[bestIdx].path)
		return candidates[bestIdx].path, nil
	}
	return candidates[0].path, nil
}

func (p *usenetTrackProber) fetchNZB(ctx context.Context, downloadURL, userAgent string, candidate models.NZBResult) ([]byte, string, error) {
	data, header, err := nzbfetch.Shared.Fetch(ctx, p.httpClient, nil, downloadURL, candidate, userAgent)
	if err != nil {
		return nil, "", err
	}
	return data, nzbFileNameFromResponse(&http.Response{Header: header}, downloadURL), nil
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
	return base + (&url.URL{Path: pathToUse}).EscapedPath()
}

func (p *usenetTrackProber) runFFProbe(ctx context.Context, probeURL string, headers string) ([]models.NZBAudioTrack, []models.NZBSubtitleTrack, error) {
	// Generous cap: large remuxes serve probe bytes slowly over WebDAV-from-Usenet
	// (header + cues may require scattered on-demand segment fetches).
	probeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-analyzeduration", "10000000",
		"-probesize", "10000000",
	}
	if strings.TrimSpace(headers) != "" {
		args = append(args, "-headers", headers)
	}
	args = append(args, probeURL)

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

func isLikelyUsenetProbeDirectory(p string) bool {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return false
	}
	if strings.HasSuffix(trimmed, "/") {
		return true
	}
	ext := strings.ToLower(path.Ext(path.Base(trimmed)))
	if ext == "" {
		return true
	}
	_, playable := usenetTrackProbePlayableExtensionPriority[ext]
	return !playable
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
	Service      usenetHealthService
	trackProber  *usenetTrackProber
	healthMemory usenetHealthRecorder
	userAgentFor func(indexer string) string
}

// usenetHealthRecorder remembers explicit health check outcomes so search
// results can be annotated server-side (see services/sourcehealth).
type usenetHealthRecorder interface {
	RecordUsenetHealth(result models.NZBResult, check *models.NZBHealthCheck)
}

// SetHealthRecorder enables remembering health check outcomes.
func (h *UsenetHandler) SetHealthRecorder(recorder usenetHealthRecorder) {
	h.healthMemory = recorder
}

var _ usenetHealthService = (*usenetsvc.Service)(nil)

func NewUsenetHandler(s usenetHealthService) *UsenetHandler {
	return &UsenetHandler{Service: s}
}

// SetIndexerUserAgentLookup supplies per-indexer User-Agent overrides for
// NZB fetches made by track probing. Call before ConfigureTrackProbing.
func (h *UsenetHandler) SetIndexerUserAgentLookup(fn func(indexer string) string) {
	h.userAgentFor = fn
	if h.trackProber != nil {
		h.trackProber.userAgentFor = fn
	}
}

// ConfigureTrackProbing enables optional audio/subtitle track probing via WebDAV + ffprobe.
// When configured the handler probes tracks whenever probeForTracks=true arrives in the request.
// Requires all four parameters to be non-empty; silently skips configuration otherwise.
func (h *UsenetHandler) ConfigureTrackProbing(importer nzbImporter, metadata usenetProbeMetadataService, externalResolver externalUsenetTrackResolver, ffprobePath, baseURL, prefix, username, password string) {
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
		importer:         importer,
		metadata:         metadata,
		externalResolver: externalResolver,
		httpClient:       &http.Client{Timeout: 60 * time.Second},
		ffprobePath:      ffprobePath,
		webdavBase:       strings.TrimRight(parsed.String(), "/"),
		webdavPrefix:     "/" + strings.Trim(prefix, "/"),
		userAgentFor:     h.userAgentFor,
	}
	log.Printf("[usenet-tracks] track probing configured via WebDAV")
}

// CheckHealth accepts an NZB indexer result and returns segment availability from Usenet.
// When probeForTracks=true in the request body, also probes audio/subtitle tracks via WebDAV+ffprobe.
func (h *UsenetHandler) CheckHealth(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Result         models.NZBResult `json:"result"`
		ProbeForTracks bool             `json:"probeForTracks,omitempty"`
		ProfileID      string           `json:"profileId,omitempty"`
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if request.ProfileID != "" {
		if request.Result.Attributes == nil {
			request.Result.Attributes = map[string]string{}
		}
		request.Result.Attributes["profileId"] = request.ProfileID
	}

	res, err := h.Service.CheckHealth(r.Context(), request.Result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if h.healthMemory != nil && res != nil {
		h.healthMemory.RecordUsenetHealth(request.Result, res)
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
