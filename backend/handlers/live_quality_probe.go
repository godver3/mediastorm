package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"novastream/internal/auth"
	"novastream/internal/livecapacity"
	"novastream/internal/streamheaders"
)

// Bound process pressure across clients, independently of the client's serial queue.
var liveQualitySlots = make(chan struct{}, 2)

type liveQualityRequest struct {
	URL         string `json:"url"`
	ProfileID   string `json:"profileId"`
	SourceID    string `json:"sourceId"`
	ChannelID   string `json:"channelId"`
	StreamIndex *int   `json:"stremioStreamIndex"`
}
type liveQualityAudio struct {
	Codec      string `json:"codec,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Layout     string `json:"layout,omitempty"`
	Language   string `json:"language,omitempty"`
	Bitrate    int64  `json:"bitrateBps,omitempty"`
	SampleRate int64  `json:"sampleRate,omitempty"`
}
type liveQualityResult struct {
	Width     int                `json:"width,omitempty"`
	Height    int                `json:"height,omitempty"`
	Codec     string             `json:"codec,omitempty"`
	FrameRate float64            `json:"frameRate,omitempty"`
	Bitrate   int64              `json:"bitrateBps,omitempty"`
	Audio     []liveQualityAudio `json:"audio"`
	Adaptive  bool               `json:"adaptive"`
	CheckedAt time.Time          `json:"checkedAt"`
}

func (h *VideoHandler) requireKnownLiveQualityChannel(w http.ResponseWriter, r *http.Request, req liveQualityRequest) bool {
	if h.liveChannels == nil {
		http.Error(w, "Live channel catalog unavailable", http.StatusServiceUnavailable)
		return false
	}
	request := r.Clone(r.Context())
	request.URL = cloneURL(r.URL)
	query := request.URL.Query()
	if profileID := strings.TrimSpace(req.ProfileID); profileID != "" {
		query.Set("profileId", profileID)
	}
	if sourceID := strings.TrimSpace(req.SourceID); sourceID != "" {
		query.Set("sourceId", sourceID)
	}
	request.URL.RawQuery = query.Encode()
	channels, err := h.liveChannels.FetchFilteredChannelsForRequest(request)
	if err != nil {
		http.Error(w, "Unable to verify live channel", http.StatusBadGateway)
		return false
	}
	wantedURL := strings.TrimSpace(req.URL)
	wantedSource := strings.TrimSpace(req.SourceID)
	wantedChannel := strings.TrimSpace(req.ChannelID)
	for _, channel := range channels {
		channelIDMatches := wantedChannel != "" && (wantedChannel == channel.ID || wantedChannel == channel.PlaybackID)
		if channelIDMatches && wantedSource == channel.SourceID && wantedURL == strings.TrimSpace(channel.URL) {
			return true
		}
	}
	http.Error(w, "Live channel not found", http.StatusNotFound)
	return false
}

func cloneURL(source *url.URL) *url.URL {
	if source == nil {
		return &url.URL{}
	}
	cloned := *source
	return &cloned
}

// ProbeLiveQuality never creates a playback/transcode session. Cancelling the HTTP
// request cancels addon resolution and kills ffprobe, releasing the provider connection.
func (h *VideoHandler) ProbeLiveQuality(w http.ResponseWriter, r *http.Request) {
	var req liveQualityRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		http.Error(w, "invalid probe request", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		http.Error(w, "invalid stream URL", http.StatusBadRequest)
		return
	}
	if req.ProfileID != "" && h.usersSvc != nil && !auth.IsMaster(r) {
		profile, ok := h.usersSvc.Get(req.ProfileID)
		if !ok || auth.GetAccountID(r) == "" || profile.AccountID != auth.GetAccountID(r) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	}
	if !h.requireKnownLiveQualityChannel(w, r, req) {
		return
	}
	authorizationRequest := r.Clone(r.Context())
	authorizationRequest.URL = cloneURL(r.URL)
	query := authorizationRequest.URL.Query()
	query.Set("profileId", req.ProfileID)
	query.Set("sourceId", req.SourceID)
	query.Set("channelId", req.ChannelID)
	authorizationRequest.URL.RawQuery = query.Encode()
	hdHomeRunInput, authorizeErr := authorizeLiveStreamURL(authorizationRequest, req.URL, h.configManager, h.liveChannels)
	if authorizeErr != nil {
		http.Error(w, "external media URL is not allowed", http.StatusBadRequest)
		return
	}
	select {
	case liveQualitySlots <- struct{}{}:
		defer func() { <-liveQualitySlots }()
	default:
		http.Error(w, "Quality checker busy. Try again shortly.", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	target, targetErr := h.liveCapacityTarget(authorizationRequest, req.URL)
	if targetErr != nil {
		http.Error(w, "live provider unavailable", http.StatusServiceUnavailable)
		return
	}
	lease, capacityErr := livecapacity.Default.Acquire(target.BucketKey, "", target.MaxStreams)
	if capacityErr != nil {
		writeLiveCapacityError(w, target)
		return
	}
	defer lease.Release()
	streamURL := req.URL
	var headers map[string]string
	if normalizeLiveProvider(target.Provider) == "stalker" {
		var err error
		streamURL, headers, err = resolveStalkerChannel(ctx, target.Stalker, req.ChannelID)
		if err != nil {
			http.Error(w, "Unable to resolve provider stream", http.StatusBadGateway)
			return
		}
	}
	index := -1
	if req.StreamIndex != nil {
		index = *req.StreamIndex
	}
	resolved, err := resolveStremioLiveStreamResource(ctx, streamURL, target.ProxyURL, index)
	if err != nil {
		http.Error(w, "Unable to resolve addon stream", http.StatusBadGateway)
		return
	}
	streamURL = resolved.URL
	if len(resolved.RequestHeaders) > 0 {
		headers = resolved.RequestHeaders
	}
	if (!hdHomeRunInput || streamURL != req.URL) && !h.requireAllowedExternalPath(w, r, streamURL) {
		return
	}
	var result liveQualityResult
	if hdHomeRunInput && streamURL == req.URL {
		result, err = h.probeHDHomeRunQuality(ctx, streamURL, target.ProxyURL)
	} else {
		result, err = h.probeLiveQuality(ctx, streamURL, target.ProxyURL, headers, resolved.IsHLS || inputLooksLikeHLS(streamURL))
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			http.Error(w, "Quality check timed out", http.StatusGatewayTimeout)
			return
		}
		// ffprobe errors may contain provider URLs and credentials. Never expose them.
		http.Error(w, "Stream quality could not be detected", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *VideoHandler) probeLiveQuality(ctx context.Context, streamURL, proxy string, headers map[string]string, hlsInput bool) (liveQualityResult, error) {
	if h.ffprobePath == "" {
		return liveQualityResult{}, errors.New("ffprobe unavailable")
	}
	// Playback carries safe provider headers in a private URL fragment. Extract
	// them for ffprobe too; otherwise protected streams can play but fail analysis.
	cleanURL, embedded := streamheaders.Extract(streamURL)
	streamURL = cleanURL
	merged := make(map[string]string, len(embedded)+len(headers))
	for key, value := range embedded {
		merged[key] = value
	}
	for key, value := range headers {
		merged[http.CanonicalHeaderKey(key)] = value
	}
	headers = merged
	args := []string{"-v", "error", "-probesize", "5000000", "-analyzeduration", "5000000", "-rw_timeout", "10000000",
		"-protocol_whitelist", "http,https,tcp,tls,crypto", "-print_format", "json", "-show_streams", "-show_format"}
	if proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return liveQualityResult{}, errors.New("unsupported probe proxy")
		}
	}
	args = append(args, ffmpegHTTPProxyArgs(proxy)...)
	if !hasRequestHeader(headers, "User-Agent") {
		args = append(args, "-user_agent", liveStreamUserAgent)
	}
	if hlsInput {
		args = append(args, "-allowed_extensions", "ALL")
	}
	if value := ffmpegHeadersArg(headers); value != "" {
		args = append(args, "-headers", value)
	}
	args = append(args, "-i", streamURL)
	return h.runLiveQualityProbe(ctx, args, nil)
}

func (h *VideoHandler) runLiveQualityProbe(ctx context.Context, args []string, input io.Reader) (liveQualityResult, error) {
	cmd := exec.CommandContext(ctx, h.ffprobePath, args...)
	cmd.Stdin = input
	cmd.Stderr = io.Discard
	output, err := cmd.Output()
	if err != nil {
		return liveQualityResult{}, err
	}
	var meta ffprobeOutput
	if err := json.Unmarshal(output, &meta); err != nil {
		return liveQualityResult{}, err
	}
	return summarizeLiveQuality(meta), nil
}
func summarizeLiveQuality(meta ffprobeOutput) liveQualityResult {
	result := liveQualityResult{Audio: []liveQualityAudio{}, CheckedAt: time.Now().UTC(), Adaptive: strings.Contains(meta.Format.FormatName, "hls") || strings.Contains(meta.Format.FormatName, "dash")}
	for _, stream := range meta.Streams {
		if stream.CodecType == "video" && stream.Disposition["attached_pic"] != 1 && (stream.Height > result.Height || stream.Height == result.Height && stream.Width > result.Width) {
			result.Width, result.Height, result.Codec = stream.Width, stream.Height, stream.CodecName
			result.FrameRate, _ = parseVideoFrameRate(stream.AvgFrameRate)
			result.Bitrate = parseInt64(stream.BitRate)
		}
		if stream.CodecType == "audio" {
			result.Audio = append(result.Audio, liveQualityAudio{Codec: stream.CodecName, Channels: stream.Channels, Layout: stream.ChannelLayout, Language: stream.Tags["language"], Bitrate: parseInt64(stream.BitRate), SampleRate: parseInt64(stream.SampleRate)})
		}
	}
	// Do not substitute a master playlist's aggregate bandwidth for video bitrate.
	return result
}
