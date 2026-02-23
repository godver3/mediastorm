package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"novastream/services/streaming"
)

// audioStreamInfo holds metadata for an audio stream
type audioStreamInfo struct {
	Index    int
	Codec    string
	Language string
	Title    string
}

// subtitleStreamInfo holds metadata for a subtitle stream
type subtitleStreamInfo struct {
	Index     int
	Codec     string
	Language  string
	Title     string
	IsForced  bool
	IsDefault bool
	IsBitmap  bool // True for PGS, HDMV, DVD subtitles (can't extract to VTT, but native player can render)
}

// isHLSCommentaryTrack checks if an audio track is a commentary track based on its title
func isHLSCommentaryTrack(title string) bool {
	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	commentaryIndicators := []string{
		"commentary",
		"director's commentary",
		"directors commentary",
		"audio commentary",
		"cast commentary",
		"crew commentary",
		"isolated score",
		"music only",
		"score only",
	}
	for _, indicator := range commentaryIndicators {
		if strings.Contains(lowerTitle, indicator) {
			return true
		}
	}
	return false
}

// UnifiedProbeResult holds all data extracted from a single ffprobe call
type UnifiedProbeResult struct {
	Duration           float64
	ColorTransfer      string // e.g., "smpte2084" for HDR, "bt709" for SDR
	VideoCodec         string // e.g., "h264", "hevc", "mpeg4" - used to detect incompatible codecs
	AudioStreams       []audioStreamInfo
	SubtitleStreams    []subtitleStreamInfo
	HasTrueHD          bool
	HasCompatibleAudio bool
	// Extended fields for VideoFullResult compatibility
	HasDolbyVision     bool
	HasHDR10           bool
	DolbyVisionProfile string
}

// cachedProbeEntry stores a probe result with expiration time
type cachedProbeEntry struct {
	result    *UnifiedProbeResult
	expiresAt time.Time
}

const (
	// TTL for cached probe results (shared between prequeue and HLS)
	// Increased to 2 hours to avoid re-probing during audio/subtitle track switches
	probeCacheTTL = 2 * time.Hour
)

// GetCachedProbe retrieves a cached probe result if available and not expired
func (m *HLSManager) GetCachedProbe(path string) *UnifiedProbeResult {
	m.probeCacheMu.RLock()
	defer m.probeCacheMu.RUnlock()

	entry, exists := m.probeCache[path]
	if !exists {
		return nil
	}

	if time.Now().After(entry.expiresAt) {
		return nil // expired
	}

	log.Printf("[hls] probe cache HIT for path: %s", path)
	return entry.result
}

// CacheProbe stores a probe result in the cache with TTL
func (m *HLSManager) CacheProbe(path string, result *UnifiedProbeResult) {
	m.probeCacheMu.Lock()
	defer m.probeCacheMu.Unlock()

	m.probeCache[path] = &cachedProbeEntry{
		result:    result,
		expiresAt: time.Now().Add(probeCacheTTL),
	}
	log.Printf("[hls] probe cached for path: %s (expires in %v)", path, probeCacheTTL)
}

// cleanupProbeCache removes expired entries from the probe cache
func (m *HLSManager) cleanupProbeCache() {
	m.probeCacheMu.Lock()
	defer m.probeCacheMu.Unlock()

	now := time.Now()
	for path, entry := range m.probeCache {
		if now.After(entry.expiresAt) {
			delete(m.probeCache, path)
		}
	}
}

// probeAllMetadata consolidates what was previously 4 separate ffprobe calls (duration, color, audio, subtitles).
// Results are cached for probeCacheTTL (2h) to avoid redundant probes between prequeue and HLS.
func (m *HLSManager) probeAllMetadata(ctx context.Context, path string) (*UnifiedProbeResult, error) {
	if m.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	// Check cache first
	if cached := m.GetCachedProbe(path); cached != nil {
		return cached, nil
	}

	isExternalURL := strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")

	var result *UnifiedProbeResult
	var err error

	// For external URLs, probe directly
	if isExternalURL {
		result, err = m.probeAllMetadataFromURL(ctx, path)
		if err == nil && result != nil {
			m.CacheProbe(path, result)
		}
		return result, err
	}

	// For provider-backed paths, try direct URL first for better metadata access
	if m.streamer != nil {
		if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
			if directURL, err := directProvider.GetDirectURL(ctx, path); err == nil && directURL != "" {
				log.Printf("[hls] probing all metadata using direct URL for path: %s", path)
				result, err = m.probeAllMetadataFromURL(ctx, directURL)
				if err == nil && result != nil {
					m.CacheProbe(path, result) // Cache by original path, not direct URL
				}
				return result, err
			}
		}
	}

	// Try local WebDAV URL as fallback (for usenet content)
	if webdavURL, ok := m.buildLocalWebDAVURLFromPath(path); ok {
		log.Printf("[hls] probing all metadata using local WebDAV URL for path: %s", path)
		result, err = m.probeAllMetadataFromURL(ctx, webdavURL)
		if err == nil && result != nil {
			m.CacheProbe(path, result)
		}
		return result, err
	}

	// Fall back to pipe-based probe
	if m.streamer == nil {
		return nil, fmt.Errorf("stream provider not configured")
	}

	log.Printf("[hls] probing all metadata using pipe for path: %s", path)
	request := streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("provider stream: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return nil, fmt.Errorf("provider stream returned empty body")
	}
	defer resp.Close()

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		io.CopyBuffer(pw, resp.Body, buf)
	}()

	probeCtx, probeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-probesize", "1000000",      // 1MB (faster startup)
		"-analyzeduration", "500000", // 0.5s (faster startup)
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr
	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		return nil, fmt.Errorf("ffprobe execution: %w", err)
	}

	result, parseErr := m.parseUnifiedProbeOutput(output)
	if parseErr == nil && result != nil {
		m.CacheProbe(path, result)
	}
	return result, parseErr
}

// probeAllMetadataFromURL probes all metadata directly from an external URL
func (m *HLSManager) probeAllMetadataFromURL(ctx context.Context, url string) (*UnifiedProbeResult, error) {
	log.Printf("[hls] probing all metadata from external URL (unified probe)")

	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-probesize", "1000000",      // 1MB (faster startup)
		"-analyzeduration", "500000", // 0.5s (faster startup)
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe execution: %w", err)
	}

	return m.parseUnifiedProbeOutput(output)
}

// parseUnifiedProbeOutput parses the JSON output from ffprobe -show_format -show_streams
func (m *HLSManager) parseUnifiedProbeOutput(output []byte) (*UnifiedProbeResult, error) {
	var probeData struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Index         int               `json:"index"`
			CodecType     string            `json:"codec_type"`
			CodecName     string            `json:"codec_name"`
			ColorTransfer string            `json:"color_transfer"`
			Tags          map[string]string `json:"tags"`
			Disposition   map[string]int    `json:"disposition"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &probeData); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	result := &UnifiedProbeResult{}

	// Parse duration
	if probeData.Format.Duration != "" {
		if d, err := strconv.ParseFloat(probeData.Format.Duration, 64); err == nil {
			result.Duration = d
		}
	}

	// Compatible audio codecs for iOS/tvOS HLS
	compatibleCodecs := map[string]bool{
		"aac":  true,
		"ac3":  true,
		"eac3": true,
		"mp3":  true,
	}

	// Text-based subtitle codecs that can be converted to WebVTT
	textSubtitleCodecs := map[string]bool{
		"subrip": true, "srt": true, "ass": true, "ssa": true,
		"webvtt": true, "vtt": true, "mov_text": true, "text": true,
		"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
		"mpl2": true, "pjs": true, "realtext": true, "stl": true,
		"subviewer": true, "subviewer1": true, "vplayer": true,
	}
	// Bitmap subtitle codecs (cannot extract to VTT, but native player can render)
	bitmapSubtitleCodecs := map[string]bool{
		"hdmv_pgs_subtitle": true, "pgssub": true, "pgs": true,
		"dvd_subtitle": true, "dvdsub": true,
		"dvb_subtitle": true, "dvbsub": true,
		"xsub": true,
	}

	for _, stream := range probeData.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))

		switch stream.CodecType {
		case "video":
			// Get video codec and color transfer from first video stream
			if result.VideoCodec == "" {
				result.VideoCodec = codec
			}
			if result.ColorTransfer == "" {
				result.ColorTransfer = stream.ColorTransfer
			}
		case "audio":
			lang := ""
			title := ""
			if stream.Tags != nil {
				lang = stream.Tags["language"]
				title = stream.Tags["title"]
			}
			result.AudioStreams = append(result.AudioStreams, audioStreamInfo{
				Index:    stream.Index,
				Codec:    codec,
				Language: lang,
				Title:    title,
			})
			if IsIncompatibleAudioCodec(codec) {
				result.HasTrueHD = true
			}
			if compatibleCodecs[codec] {
				result.HasCompatibleAudio = true
			}
		case "subtitle":
			isBitmap := bitmapSubtitleCodecs[codec]
			// Skip completely unknown subtitle formats
			if !textSubtitleCodecs[codec] && !isBitmap {
				continue
			}
			lang := ""
			title := ""
			isForced := false
			isDefault := false
			if stream.Tags != nil {
				lang = stream.Tags["language"]
				title = stream.Tags["title"]
			}
			if stream.Disposition != nil {
				isForced = stream.Disposition["forced"] > 0
				isDefault = stream.Disposition["default"] > 0
			}
			result.SubtitleStreams = append(result.SubtitleStreams, subtitleStreamInfo{
				Index:     stream.Index,
				Codec:     codec,
				Language:  lang,
				Title:     title,
				IsForced:  isForced,
				IsDefault: isDefault,
				IsBitmap:  isBitmap,
			})
		}
	}

	log.Printf("[hls] unified probe: duration=%.2f videoCodec=%s colorTransfer=%s audio=%d subtitle=%d hasTrueHD=%v hasCompatible=%v",
		result.Duration, result.VideoCodec, result.ColorTransfer, len(result.AudioStreams), len(result.SubtitleStreams),
		result.HasTrueHD, result.HasCompatibleAudio)

	return result, nil
}

// probeAudioStreams inspects audio streams for codec compatibility and exposes their ordering
func (m *HLSManager) probeAudioStreams(ctx context.Context, path string) (streams []audioStreamInfo, hasTrueHD bool, hasCompatibleAudio bool, err error) {
	if m.ffprobePath == "" {
		return nil, false, true, nil // Assume compatible if we can't probe
	}

	// For external URLs, probe directly instead of going through provider
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return m.probeAudioStreamsFromURL(ctx, path)
	}

	if m.streamer == nil {
		return nil, false, true, nil // Assume compatible if no provider
	}

	// Try to get a direct URL for probing (needed for files with moov atom at end)
	// This allows ffprobe to seek instead of relying on the first 16MB containing metadata
	if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
		if directURL, err := directProvider.GetDirectURL(ctx, path); err == nil && directURL != "" {
			log.Printf("[hls] probing audio using direct URL for path: %s", path)
			return m.probeAudioStreamsFromURL(ctx, directURL)
		} else if err != nil {
			log.Printf("[hls] failed to get direct URL for audio probe: %v", err)
		}
	}

	// Try local WebDAV URL as fallback (for usenet content)
	if webdavURL, ok := m.buildLocalWebDAVURLFromPath(path); ok {
		log.Printf("[hls] probing audio using local WebDAV URL for path: %s", path)
		return m.probeAudioStreamsFromURL(ctx, webdavURL)
	}

	// Fall back to pipe-based probe with first 16MB
	log.Printf("[hls] probing audio using pipe method for path: %s", path)
	request := streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		log.Printf("[hls] failed to probe audio streams: %v", err)
		return nil, false, true, nil // Assume compatible on error
	}
	if resp.Body == nil {
		resp.Close()
		return nil, false, true, nil
	}
	defer resp.Close()

	// Create pipe to feed data to ffprobe
	pr, pw := io.Pipe()
	copyDone := make(chan error, 1)

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		copyDone <- copyErr
	}()

	// Run ffprobe with timeout
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-select_streams", "a",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		log.Printf("[hls] ffprobe audio failed: %v", err)
		return nil, false, true, nil // Assume compatible on error
	}

	// Parse JSON output
	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe output: %v", err)
		return nil, false, true, nil
	}

	compatibleCodecs := map[string]bool{
		"aac":  true,
		"ac3":  true,
		"eac3": true,
		"mp3":  true,
	}

	streams = make([]audioStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, audioStreamInfo{Index: stream.Index, Codec: codec})
		if IsIncompatibleAudioCodec(codec) {
			hasTrueHD = true
		}
		if compatibleCodecs[codec] {
			hasCompatibleAudio = true
		}
	}

	log.Printf("[hls] audio probe results: hasIncompatibleAudio=%v hasCompatibleAudio=%v codecs=%d",
		hasTrueHD, hasCompatibleAudio, len(result.Streams))

	return streams, hasTrueHD, hasCompatibleAudio, nil
}

// probeAudioStreamsFromURL probes audio streams directly from an external URL
func (m *HLSManager) probeAudioStreamsFromURL(ctx context.Context, url string) (streams []audioStreamInfo, hasTrueHD bool, hasCompatibleAudio bool, err error) {
	log.Printf("[hls] probing audio streams from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-select_streams", "a",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[hls] ffprobe audio from URL failed: %v", err)
		return nil, false, true, nil // Assume compatible on error
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe audio output from URL: %v", err)
		return nil, false, true, nil
	}

	compatibleCodecs := map[string]bool{
		"aac":  true,
		"ac3":  true,
		"eac3": true,
		"mp3":  true,
	}

	streams = make([]audioStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, audioStreamInfo{Index: stream.Index, Codec: codec})
		if IsIncompatibleAudioCodec(codec) {
			hasTrueHD = true
		}
		if compatibleCodecs[codec] {
			hasCompatibleAudio = true
		}
	}

	log.Printf("[hls] audio probe from URL results: hasIncompatibleAudio=%v hasCompatibleAudio=%v codecs=%d",
		hasTrueHD, hasCompatibleAudio, len(result.Streams))

	return streams, hasTrueHD, hasCompatibleAudio, nil
}

// probeSubtitleStreams lists subtitle streams and preserves their ordering for FFmpeg mapping
func (m *HLSManager) probeSubtitleStreams(ctx context.Context, path string) (streams []subtitleStreamInfo, err error) {
	if m.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	// For external URLs, probe directly instead of going through provider
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return m.probeSubtitleStreamsFromURL(ctx, path)
	}

	if m.streamer == nil {
		return nil, fmt.Errorf("streamer not configured")
	}

	// Try to get a direct URL for probing (needed for files with moov atom at end)
	// This allows ffprobe to seek instead of relying on the first 16MB containing metadata
	if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
		if directURL, err := directProvider.GetDirectURL(ctx, path); err == nil && directURL != "" {
			log.Printf("[hls] probing subtitles using direct URL for path: %s", path)
			return m.probeSubtitleStreamsFromURL(ctx, directURL)
		} else if err != nil {
			log.Printf("[hls] failed to get direct URL for subtitle probe: %v", err)
		}
	}

	// Try local WebDAV URL as fallback (for usenet content)
	if webdavURL, ok := m.buildLocalWebDAVURLFromPath(path); ok {
		log.Printf("[hls] probing subtitles using local WebDAV URL for path: %s", path)
		return m.probeSubtitleStreamsFromURL(ctx, webdavURL)
	}

	// Fall back to pipe-based probe with first 16MB
	log.Printf("[hls] probing subtitles using pipe method for path: %s", path)
	request := streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215",
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		log.Printf("[hls] failed to probe subtitle streams: %v", err)
		return nil, err
	}
	if resp.Body == nil {
		resp.Close()
		return nil, fmt.Errorf("subtitle probe returned empty body")
	}
	defer resp.Close()

	pr, pw := io.Pipe()
	copyDone := make(chan error, 1)

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		copyDone <- copyErr
	}()

	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		log.Printf("[hls] ffprobe subtitle failed: %v", err)
		return nil, err
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe subtitle output: %v", err)
		return nil, err
	}

	// Text-based subtitle codecs that can be converted to WebVTT
	textSubtitleCodecs := map[string]bool{
		"subrip": true, "srt": true, "ass": true, "ssa": true,
		"webvtt": true, "vtt": true, "mov_text": true, "text": true,
		"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
		"mpl2": true, "pjs": true, "realtext": true, "stl": true,
		"subviewer": true, "subviewer1": true, "vplayer": true,
	}
	// Bitmap subtitle codecs (cannot extract to VTT, but native player can render)
	bitmapSubtitleCodecs := map[string]bool{
		"hdmv_pgs_subtitle": true, "pgssub": true, "pgs": true,
		"dvd_subtitle": true, "dvdsub": true,
		"dvb_subtitle": true, "dvbsub": true,
		"xsub": true,
	}

	streams = make([]subtitleStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		isBitmap := bitmapSubtitleCodecs[codec]
		// Skip completely unknown subtitle formats
		if !textSubtitleCodecs[codec] && !isBitmap {
			continue
		}
		streams = append(streams, subtitleStreamInfo{Index: stream.Index, Codec: codec, IsBitmap: isBitmap})
	}

	log.Printf("[hls] subtitle probe results: streams=%d", len(streams))
	return streams, nil
}

// probeSubtitleStreamsFromURL probes subtitle streams directly from an external URL
func (m *HLSManager) probeSubtitleStreamsFromURL(ctx context.Context, url string) (streams []subtitleStreamInfo, err error) {
	log.Printf("[hls] probing subtitle streams from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[hls] ffprobe subtitle from URL failed: %v", err)
		return nil, err
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe subtitle output from URL: %v", err)
		return nil, err
	}

	// Text-based subtitle codecs that can be converted to WebVTT
	textSubtitleCodecs := map[string]bool{
		"subrip": true, "srt": true, "ass": true, "ssa": true,
		"webvtt": true, "vtt": true, "mov_text": true, "text": true,
		"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
		"mpl2": true, "pjs": true, "realtext": true, "stl": true,
		"subviewer": true, "subviewer1": true, "vplayer": true,
	}
	// Bitmap subtitle codecs (cannot extract to VTT, but native player can render)
	bitmapSubtitleCodecs := map[string]bool{
		"hdmv_pgs_subtitle": true, "pgssub": true, "pgs": true,
		"dvd_subtitle": true, "dvdsub": true,
		"dvb_subtitle": true, "dvbsub": true,
		"xsub": true,
	}

	streams = make([]subtitleStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		isBitmap := bitmapSubtitleCodecs[codec]
		// Skip completely unknown subtitle formats
		if !textSubtitleCodecs[codec] && !isBitmap {
			continue
		}
		streams = append(streams, subtitleStreamInfo{Index: stream.Index, Codec: codec, IsBitmap: isBitmap})
	}

	log.Printf("[hls] subtitle probe from URL results: streams=%d", len(streams))
	return streams, nil
}

// probeDuration attempts to get the file duration using ffprobe
func (m *HLSManager) probeDuration(ctx context.Context, cleanPath string) (float64, error) {
	if m.ffprobePath == "" {
		return 0, fmt.Errorf("ffprobe not configured")
	}

	// For external URLs, probe directly
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		return m.probeDurationFromURL(ctx, cleanPath)
	}

	if m.streamer == nil {
		return 0, fmt.Errorf("stream provider not configured")
	}

	// Request first 16MB to probe
	request := streaming.Request{
		Path:        cleanPath,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		return 0, fmt.Errorf("provider stream: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return 0, fmt.Errorf("provider stream returned empty body")
	}
	defer resp.Close()

	// Create pipe to feed data to ffprobe
	pr, pw := io.Pipe()
	copyDone := make(chan error, 1)

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		copyDone <- copyErr
	}()

	// Run ffprobe with timeout
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		return 0, fmt.Errorf("ffprobe execution: %w", err)
	}

	// Parse duration from output
	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", durationStr, err)
	}

	return duration, nil
}

// probeDurationFromURL probes duration directly from an external URL
func (m *HLSManager) probeDurationFromURL(ctx context.Context, url string) (float64, error) {
	log.Printf("[hls] probing duration from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe execution: %w", err)
	}

	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", durationStr, err)
	}

	log.Printf("[hls] probed duration from URL: %.2f seconds", duration)
	return duration, nil
}

// probeColorMetadata checks if the video has correct HDR color tagging
// Returns color_transfer value (e.g., "smpte2084" for HDR, "bt709" for SDR/incorrect)
func (m *HLSManager) probeColorMetadata(ctx context.Context, cleanPath string) (string, error) {
	if m.ffprobePath == "" {
		return "", fmt.Errorf("ffprobe not configured")
	}

	// For external URLs, probe directly
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		return m.probeColorMetadataFromURL(ctx, cleanPath)
	}

	if m.streamer == nil {
		return "", fmt.Errorf("stream provider not configured")
	}

	// Request first 16MB to probe
	request := streaming.Request{
		Path:        cleanPath,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		return "", fmt.Errorf("provider stream: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return "", fmt.Errorf("provider stream returned empty body")
	}
	defer resp.Close()

	// Create pipe to feed data to ffprobe
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		io.CopyBuffer(pw, resp.Body, buf)
	}()

	// Run ffprobe with timeout
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-select_streams", "v:0",
		"-show_entries", "stream=color_transfer",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		return "", fmt.Errorf("ffprobe execution: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// probeColorMetadataFromURL probes color metadata directly from an external URL
func (m *HLSManager) probeColorMetadataFromURL(ctx context.Context, url string) (string, error) {
	log.Printf("[hls] probing color metadata from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-select_streams", "v:0",
		"-show_entries", "stream=color_transfer",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe execution: %w", err)
	}

	colorTransfer := strings.TrimSpace(string(output))
	log.Printf("[hls] probed color_transfer from URL: %q", colorTransfer)
	return colorTransfer, nil
}

// probeKeyframePosition finds the actual keyframe position FFmpeg will seek to for a given time.
// When FFmpeg uses input seeking (-ss before -i), it seeks to the nearest keyframe at or before
// the requested time. This function determines that actual keyframe position so we can extract
// subtitles from the same position for proper sync.
// Returns the keyframe PTS in seconds, or the original seekTime if probing fails.
func (m *HLSManager) probeKeyframePosition(ctx context.Context, path string, seekTime float64) float64 {
	if m.ffprobePath == "" || seekTime <= 0 {
		return seekTime
	}

	// For external URLs, probe directly
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return m.probeKeyframePositionFromURL(ctx, path, seekTime)
	}

	// Try to get a direct URL for probing
	if m.streamer != nil {
		if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
			if directURL, err := directProvider.GetDirectURL(ctx, path); err == nil && directURL != "" {
				log.Printf("[hls] probing keyframe position using direct URL for path: %s seekTime: %.3f", path, seekTime)
				return m.probeKeyframePositionFromURL(ctx, directURL, seekTime)
			}
		}
	}

	// Try local WebDAV URL as fallback
	if webdavURL, ok := m.buildLocalWebDAVURLFromPath(path); ok {
		log.Printf("[hls] probing keyframe position using local WebDAV URL for path: %s seekTime: %.3f", path, seekTime)
		return m.probeKeyframePositionFromURL(ctx, webdavURL, seekTime)
	}

	log.Printf("[hls] no direct URL available for keyframe probe, using requested time: %.3f", seekTime)
	return seekTime
}

// probeKeyframePositionFromURL probes the actual keyframe position from a URL.
// Uses ffprobe to find the nearest keyframe at or after the requested time.
// This tells us where FFmpeg will actually start when using input seeking.
func (m *HLSManager) probeKeyframePositionFromURL(ctx context.Context, url string, seekTime float64) float64 {
	log.Printf("[hls] probing keyframe position from URL at %.3fs", seekTime)

	probeCtx, probeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer probeCancel()

	// Use ffprobe with -read_intervals to start reading from seekTime
	// -skip_frame nokey skips non-keyframes so we only see keyframes
	// -show_frames is required for frame-level queries
	// Format: -read_intervals START%+#COUNT means "read COUNT frames starting from START seconds"
	args := []string{
		"-v", "error",
		"-probesize", "1000000",      // 1MB (faster startup)
		"-analyzeduration", "500000", // 0.5s (faster startup)
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-i", url,
		"-select_streams", "v:0",
		"-skip_frame", "nokey",
		"-show_frames",
		"-show_entries", "frame=pts_time",
		"-read_intervals", fmt.Sprintf("%.3f%%+#1", seekTime),
		"-of", "csv=p=0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		// Try alternative approach without -skip_frame (some formats like HEVC may not support it well)
		log.Printf("[hls] keyframe probe with skip_frame failed: %v, trying without skip_frame", err)
		args = []string{
			"-v", "error",
			"-probesize", "1000000",      // 1MB (faster startup)
			"-analyzeduration", "500000", // 0.5s (faster startup)
			"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
			"-i", url,
			"-select_streams", "v:0",
			"-show_frames",
			"-show_entries", "frame=pts_time,key_frame",
			"-read_intervals", fmt.Sprintf("%.3f%%+#5", seekTime),
			"-of", "csv=p=0",
		}
		cmd = exec.CommandContext(probeCtx, m.ffprobePath, args...)
		output, err = cmd.Output()
		if err != nil {
			log.Printf("[hls] keyframe probe failed: %v, using requested time: %.3f", err, seekTime)
			return seekTime
		}

		// Parse output to find first keyframe (key_frame=1)
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			parts := strings.Split(line, ",")
			if len(parts) >= 2 && strings.TrimSpace(parts[1]) == "1" {
				if pts, parseErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); parseErr == nil {
					delta := pts - seekTime
					log.Printf("[hls] keyframe probe (fallback): requested=%.3fs actual=%.3fs delta=%.3fs", seekTime, pts, delta)
					return pts
				}
			}
		}
		log.Printf("[hls] keyframe probe fallback found no keyframes, using requested time: %.3f", seekTime)
		return seekTime
	}

	ptsStr := strings.TrimSpace(string(output))
	if ptsStr == "" {
		log.Printf("[hls] keyframe probe returned empty PTS, using requested time: %.3f", seekTime)
		return seekTime
	}

	// Handle output format: might include HDR metadata after the PTS
	// e.g., "765.123" or "765.123,Mastering display metadata,..."
	parts := strings.Split(ptsStr, ",")
	ptsStr = strings.TrimSpace(parts[0])

	keyframePTS, err := strconv.ParseFloat(ptsStr, 64)
	if err != nil {
		log.Printf("[hls] failed to parse keyframe PTS %q: %v, using requested time: %.3f", ptsStr, err, seekTime)
		return seekTime
	}

	delta := keyframePTS - seekTime
	log.Printf("[hls] keyframe probe: requested=%.3fs actual=%.3fs delta=%.3fs", seekTime, keyframePTS, delta)

	return keyframePTS
}
