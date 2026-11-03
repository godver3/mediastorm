package jellyfinbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/playback"
)

// playSession ties a Jellyfin PlaySessionId to a resolved mediastorm stream.
type playSession struct {
	ID            string
	MediaSourceID string
	ItemID        string
	ProfileID     string
	DeviceID      string // client device; play sessions are never shared across devices
	Ref           itemRef
	Live          bool
	ReleaseKey    string // a release the user picked as version ("" = automatic)
	// rankGen is mediastorm's ranking generation the stream was chosen
	// under; after a ranking settings change it is not reused.
	rankGen uint64

	mu                sync.Mutex
	createMu          sync.Mutex // serializes HLS session creation for this play session
	prequeueID        string
	status            *playback.PrequeueStatusResponse
	liveURL           string
	live              *liveChannel
	owner             *principal        // principal that created the session; authorizes token-less media requests
	hls               map[string]string // option key -> mediastorm HLS session ID
	hlsFiles          map[string]string // HLS session ID -> playlist file (stream/master)
	hlsOwners         map[string]bool   // HLS session IDs created for this play session
	lastSeen          time.Time
	created           time.Time
	duration          float64
	currentHLS        string                // HLS session the client was last handed
	probe             *mediaProbe           // technical details of the resolved stream
	segments          []dto.MediaSegmentDto // resolved skip segments
	thumbnailsStarted bool
	caps              *hlsClientCaps // what the client plays inside HLS (DeviceProfile)
	copyFailed        bool           // a stream copy failed or was refused: transcode
	segmentsDone      bool
	state             *playState // last playback report; nil until playback starts
	reissued          time.Time  // last time PlaybackInfo handed this session out again
	// audioPick and subtitlePick tell the viewer's picks from the defaults
	// PlaybackInfo handed out; only picks are remembered per series.
	audioPick    pickState
	subtitlePick pickState
}

// playState is the client's last playback report, for GET /Sessions.
type playState struct {
	PlayMethod    string
	MediaSourceID string
	PositionTicks *int64
	Paused        bool
	Muted         bool
	CanSeek       bool
	Audio         *int
	Subtitle      *int
	At            time.Time
}

type playSessions struct {
	mu   sync.Mutex
	byID map[string]*playSession
}

const playSessionTTL = 8 * time.Hour

func newPlaySessions() *playSessions { return &playSessions{byID: map[string]*playSession{}} }

func (ps *playSessions) add(sess *playSession) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	for id, old := range ps.byID {
		old.mu.Lock()
		stale := now.Sub(old.lastSeen) > playSessionTTL
		old.mu.Unlock()
		if stale {
			delete(ps.byID, id)
		}
	}
	sess.created, sess.lastSeen = now, now
	ps.byID[sess.ID] = sess
}

func (ps *playSessions) get(id string) (*playSession, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	sess, ok := ps.byID[strings.ToLower(strings.TrimSpace(id))]
	if ok {
		sess.mu.Lock()
		sess.lastSeen = time.Now()
		sess.mu.Unlock()
	}
	return sess, ok
}

// byHLS finds the play session that owns a mediastorm HLS session.
func (ps *playSessions) byHLS(hlsID string) (*playSession, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, sess := range ps.byID {
		sess.mu.Lock()
		owned := sess.hlsOwners[hlsID]
		sess.mu.Unlock()
		if owned {
			return sess, true
		}
	}
	return nil, false
}

// latestForProfileItem returns the newest play session of a profile for an
// item, on any device.
func (ps *playSessions) latestForProfileItem(profileID, itemID string) (*playSession, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var best *playSession
	for _, sess := range ps.byID {
		if sess.ProfileID == profileID && sess.ItemID == itemID && (best == nil || sess.created.After(best.created)) {
			best = sess
		}
	}
	return best, best != nil
}

// latestForItem returns the newest play session for (profile, item, device),
// preferring one of mediaSourceID when it is set (Auto and the versions have
// their own sessions). Reports without PlaySessionId are matched this way:
// Plezy leaves the id out, and Jellyfin itself matches such reports by
// device and item.
func (ps *playSessions) latestForItem(profileID, itemID, deviceID, mediaSourceID string) (*playSession, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	mediaSourceID = normalizeID(mediaSourceID)
	var best *playSession
	bestMatches := false
	for _, sess := range ps.byID {
		if sess.ProfileID != profileID || sess.DeviceID != deviceID || sess.ItemID != itemID {
			continue
		}
		matches := mediaSourceID != "" && normalizeID(sess.MediaSourceID) == mediaSourceID
		switch {
		case best == nil, matches && !bestMatches, matches == bestMatches && sess.created.After(best.created):
			best, bestMatches = sess, matches
		}
	}
	return best, best != nil
}

func (ps *playSessions) remove(id string) (*playSession, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	sess, ok := ps.byID[id]
	delete(ps.byID, id)
	return sess, ok
}

func (ps *playSessions) closeAll(s *Server) {
	ps.mu.Lock()
	sessions := make([]*playSession, 0, len(ps.byID))
	for _, sess := range ps.byID {
		sessions = append(sessions, sess)
	}
	ps.byID = map[string]*playSession{}
	ps.mu.Unlock()
	for _, sess := range sessions {
		s.stopHLS(context.Background(), nil, sess)
	}
}

// --- PlaybackInfo --------------------------------------------------------

type playbackInfoRequest struct {
	UserID              string          `json:"UserId"`
	MaxStreamingBitrate *int64          `json:"MaxStreamingBitrate"`
	StartTimeTicks      *int64          `json:"StartTimeTicks"`
	AudioStreamIndex    *int            `json:"AudioStreamIndex"`
	SubtitleStreamIndex *int            `json:"SubtitleStreamIndex"`
	MediaSourceID       string          `json:"MediaSourceId"`
	LiveStreamID        string          `json:"LiveStreamId"`
	EnableDirectPlay    *bool           `json:"EnableDirectPlay"`
	EnableDirectStream  *bool           `json:"EnableDirectStream"`
	EnableTranscoding   *bool           `json:"EnableTranscoding"`
	AllowVideoCopy      *bool           `json:"AllowVideoStreamCopy"`
	DeviceProfile       json.RawMessage `json:"DeviceProfile"`
}

func (s *Server) handlePlaybackInfo(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var req playbackInfoRequest
	if r.Method == http.MethodPost && r.ContentLength != 0 {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if len(body) > 0 {
			req = decodePlaybackInfoRequest(body)
		}
	}
	// Query-string variants (GET /Items/{id}/PlaybackInfo, older clients).
	if req.StartTimeTicks == nil {
		if v := queryValue(r, "StartTimeTicks"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				req.StartTimeTicks = &n
			}
		}
	}
	if req.AudioStreamIndex == nil {
		if v := queryValue(r, "AudioStreamIndex"); v != "" {
			req.AudioStreamIndex = intPtr(parseIntDefault(v, -1))
		}
	}
	if req.SubtitleStreamIndex == nil {
		if v := queryValue(r, "SubtitleStreamIndex"); v != "" {
			req.SubtitleStreamIndex = intPtr(parseIntDefault(v, -1))
		}
	}
	if req.MediaSourceID == "" {
		req.MediaSourceID = queryValue(r, "MediaSourceId")
	}

	itemID := normalizeID(pathVar(r, "id"))
	s.clearStopped(p.Profile.ID, itemID) // started again after a stop
	if v := requestedVersion(r); v != "" && req.MediaSourceID == "" {
		// PlaybackInfo for a version item: the release on its item.
		req.MediaSourceID = v
	}
	ref, ok := s.ids.get(itemID)
	if !ok || !s.kidsAllowsRef(r.Context(), p.Profile, ref) {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	switch ref.Kind {
	case refMovie, refEpisode:
		s.vodPlaybackInfo(w, r, p, itemID, ref, req)
	case refLocal:
		s.localPlaybackInfo(w, r, p, itemID, ref, req)
	case refChannel, refProgram:
		s.livePlaybackInfo(w, r, p, itemID, ref)
	default:
		writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{}, ErrorCode: "NoCompatibleStream"})
	}
}

func (s *Server) vodPlaybackInfo(w http.ResponseWriter, r *http.Request, p *principal, itemID string, ref itemRef, req playbackInfoRequest) {
	// Playing is the only request that shows a user wants this item (clients
	// fetch items in bulk while browsing), so the release list for the
	// version picker is searched here and shown on later details requests.
	if s.releaseVersionCount() > 0 {
		s.releasesFor(p, itemID, ref)
	}
	// Reuse a resolved stream for the same item (clients call PlaybackInfo again
	// when switching audio/subtitle tracks or after a seek).
	// A version picked in the client: a specific release instead of the
	// automatic choice.
	rel, isRelease := s.releaseByMediaSource(p, itemID, ref, req.MediaSourceID)
	var sess *playSession
	if prev, ok := s.plays.latestForItem(p.Profile.ID, itemID, p.Client.DeviceID, ""); ok && time.Since(prev.created) < 2*time.Hour && prev.ReleaseKey == rel.Key &&
		(prev.ReleaseKey != "" || prev.rankGen == s.rankingGeneration() || prev.playing()) {
		prev.mu.Lock()
		failed := prev.status != nil && prev.status.Status == playback.PrequeueStatusFailed
		prev.mu.Unlock()
		if !failed {
			sess = prev
			// The device may have signed in again since; media requests
			// without a token authenticate as the current owner.
			sess.mu.Lock()
			sess.owner = p
			sess.reissued = time.Now()
			sess.mu.Unlock()
		}
	}
	if sess == nil && isRelease {
		sess = &playSession{
			ID:            randomHex(16),
			MediaSourceID: s.releaseMediaSourceID(p, itemID, rel.Key),
			ItemID:        itemID,
			ProfileID:     p.Profile.ID,
			DeviceID:      p.Client.DeviceID,
			Ref:           ref,
			ReleaseKey:    rel.Key,
			status:        &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusQueued},
			owner:         p,
			hls:           map[string]string{},
			hlsFiles:      map[string]string{},
			hlsOwners:     map[string]bool{},
		}
		s.plays.add(sess)
		titleID := ""
		if pr, err := s.prequeueRequest(r.Context(), p, ref); err == nil {
			titleID = pr.TitleID
		}
		go s.resolveRelease(p, sess, rel, titleID)
	}
	if sess == nil {
		if caps := s.autoCaps(p, itemID, capsFromProfile(req.DeviceProfile)); caps != nil {
			// Auto, avoiding video transcoding: the pick (prequeue, and if
			// needed further releases) runs on its own; PlaybackInfo waits
			// for it like for a prequeue.
			sess = &playSession{
				ID:            randomHex(16),
				MediaSourceID: s.mediaSourceID(p.Profile.ID, p.Client.DeviceID, itemID),
				ItemID:        itemID,
				ProfileID:     p.Profile.ID,
				DeviceID:      p.Client.DeviceID,
				Ref:           ref,
				rankGen:       s.rankingGeneration(),
				status:        &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusSearching},
				owner:         p,
				hls:           map[string]string{},
				hlsFiles:      map[string]string{},
				hlsOwners:     map[string]bool{},
			}
			s.plays.add(sess)
			go s.playAutoPick(p, sess, caps)
		}
	}
	if sess == nil {
		prequeueID, err := s.startPrequeue(r.Context(), p, ref)
		if err != nil {
			log.Printf("[jellyfin-bridge] prequeue for %s failed: %v", ref.Source, err)
			writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{}, ErrorCode: "NoCompatibleStream"})
			return
		}
		sess = &playSession{
			ID:            randomHex(16),
			MediaSourceID: s.mediaSourceID(p.Profile.ID, p.Client.DeviceID, itemID),
			ItemID:        itemID,
			ProfileID:     p.Profile.ID,
			DeviceID:      p.Client.DeviceID,
			Ref:           ref,
			rankGen:       s.rankingGeneration(),
			prequeueID:    prequeueID,
			owner:         p,
			hls:           map[string]string{},
			hlsFiles:      map[string]string{},
			hlsOwners:     map[string]bool{},
		}
		s.plays.add(sess)
	}

	wait := resolveWaitFor(p, time.Duration(s.settings().PlaybackInfoWaitSeconds)*time.Second)
	status := s.awaitPrequeue(r.Context(), p, sess, wait)
	s.startThumbnails(p, sess, status)
	if status != nil && status.Status == playback.PrequeueStatusFailed {
		msg := status.Error
		if msg == "" {
			msg = "no playable release found"
		}
		log.Printf("[jellyfin-bridge] no stream for %s: %s", ref.Source, msg)
		s.plays.remove(sess.ID)
		writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{}, ErrorCode: "NoCompatibleStream"})
		return
	}

	source := s.vodMediaSource(r, p, sess, status, req)
	if strings.EqualFold(strings.TrimSpace(req.MediaSourceID), itemID) {
		// Clients match the answer to the version they asked for (Plezy
		// drops a source with another ID); the automatic version is the item.
		source.ID = itemID
	}
	log.Printf("[jellyfin-bridge] playback info %s (%s): asked source=%q audio=%s subtitle=%s -> session %s, source %s %q, release=%t", itemID, p.Client.Client, req.MediaSourceID, intOrDash(req.AudioStreamIndex), intOrDash(req.SubtitleStreamIndex), sess.ID, source.ID, source.Name, sess.ReleaseKey != "")
	writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{source}, PlaySessionID: sess.ID})
}

// startPrequeue asks mediastorm to search for and resolve a stream.
func (s *Server) startPrequeue(ctx context.Context, p *principal, ref itemRef) (string, error) {
	req, err := s.prequeueRequest(ctx, p, ref)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(req)
	var resp playback.PrequeueResponse
	if _, err := callJSON(s.deps.Handlers.Prequeue, internalRequest(ctx, p, http.MethodPost, "/api/playback/prequeue", body, nil), &resp); err != nil {
		return "", err
	}
	if resp.PrequeueID == "" {
		return "", fmt.Errorf("prequeue returned no id")
	}
	return resp.PrequeueID, nil
}

// prequeueRequest describes an item the way mediastorm searches for it.
func (s *Server) prequeueRequest(ctx context.Context, p *principal, ref itemRef) (playback.PrequeueRequest, error) {
	req := playback.PrequeueRequest{
		UserID:  p.Profile.ID,
		Reason:  "details",
		SkipHLS: true,
	}
	// Identity comes from the canonical source ID plus the metadata service,
	// never from shared ref context, which can carry values another account
	// supplied through its watchlist.
	switch ref.Kind {
	case refMovie:
		t := s.trustedTitle(ctx, "movie", ref.Source, titleFromRef(ref))
		req.TitleID, req.TitleName, req.MediaType, req.Year = t.ID, t.Name, "movie", t.Year
		req.ImdbID, req.TmdbID, req.TvdbID = t.ExternalIDs["imdb"], t.ExternalIDs["tmdb"], t.ExternalIDs["tvdb"]
	case refEpisode:
		seriesSource := ""
		if id, _, _, ok := mediaidentity.ParseEpisodeID(ref.Source); ok {
			seriesSource = id
		}
		series := s.trustedTitle(ctx, "series", seriesSource, seriesInfoFromSeasonRef(ref))
		req.TitleID, req.TitleName, req.MediaType, req.Year = series.ID, series.Name, "series", series.Year
		req.ImdbID, req.TmdbID, req.TvdbID = series.ExternalIDs["imdb"], series.ExternalIDs["tmdb"], series.ExternalIDs["tvdb"]
		req.SeasonNumber, req.EpisodeNumber = ref.Ctx.Season, ref.Ctx.Episode
	default:
		return req, fmt.Errorf("item kind %s is not playable", ref.Kind)
	}
	if strings.TrimSpace(req.TitleID) == "" || strings.TrimSpace(req.TitleName) == "" {
		return req, fmt.Errorf("missing title identity")
	}
	return req, nil
}

// prewarm starts resolving a stream in the background (the next episode
// while one plays), so pressing play is fast.
func (s *Server) prewarm(p *principal, itemID string) {
	ref, ok := s.ids.get(itemID)
	if !ok || (ref.Kind != refMovie && ref.Kind != refEpisode) || s.deps.Handlers.Prequeue == nil {
		return
	}
	if _, exists := s.plays.latestForItem(p.Profile.ID, itemID, p.Client.DeviceID, ""); exists {
		return
	}
	if caps := s.autoCaps(p, itemID, nil); caps != nil {
		// The device's caps are known from its last playback: pick the
		// release for it now.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), autoPrequeueWait+autoPickBudget)
			defer cancel()
			s.pickAuto(ctx, p, itemID, ref, caps)
		}()
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		prequeueID, err := s.startPrequeue(ctx, p, ref)
		if err != nil {
			log.Printf("[jellyfin-bridge] prewarm %s: %v", ref.Source, err)
			return
		}
		s.probePrequeued(p, itemID, prequeueID)
	}()
}

// playAutoPick fills an automatic play session from pickAuto.
func (s *Server) playAutoPick(p *principal, sess *playSession, caps *hlsClientCaps) {
	ctx, cancel := context.WithTimeout(context.Background(), autoPrequeueWait+autoPickBudget+5*time.Second)
	defer cancel()
	status, probe := s.pickAuto(ctx, p, sess.ItemID, sess.Ref, caps)
	if status == nil {
		status = &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusFailed, Error: "no stream found in time"}
	}
	published := *status // every session gets its own copy
	sess.mu.Lock()
	sess.status = &published
	if probe != nil && probe.path == published.StreamPath {
		sess.probe = probe
	}
	if published.Duration > 0 {
		sess.duration = published.Duration
	}
	sess.mu.Unlock()
}

// awaitPrequeue polls the prequeue until it is ready, failed, or wait expires.
func (s *Server) awaitPrequeue(ctx context.Context, p *principal, sess *playSession, wait time.Duration) *playback.PrequeueStatusResponse {
	deadline := time.Now().Add(wait)
	for {
		sess.mu.Lock()
		cached := sess.status
		prequeueID := sess.prequeueID
		sess.mu.Unlock()
		if cached != nil && (cached.Status == playback.PrequeueStatusReady || cached.Status == playback.PrequeueStatusFailed) {
			return cached
		}
		if prequeueID == "" {
			// A picked release resolves in its own goroutine.
			if time.Now().After(deadline) {
				return cached
			}
			select {
			case <-ctx.Done():
				return cached
			case <-time.After(300 * time.Millisecond):
			}
			continue
		}
		var status playback.PrequeueStatusResponse
		req := internalRequest(ctx, p, http.MethodGet, "/api/playback/prequeue/"+prequeueID, nil, map[string]string{"prequeueID": prequeueID})
		if code, err := callJSON(s.deps.Handlers.PrequeueStatus, req, &status); err != nil {
			if code == http.StatusNotFound {
				return &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusFailed, Error: "prequeue expired"}
			}
			log.Printf("[jellyfin-bridge] prequeue status: %v", err)
		} else {
			if status.Status == playback.PrequeueStatusExpired {
				status.Status = playback.PrequeueStatusFailed
			}
			published := status // readers get their own copy
			sess.mu.Lock()
			sess.status = &published
			if status.Duration > 0 {
				sess.duration = status.Duration
			}
			sess.mu.Unlock()
			if status.Status == playback.PrequeueStatusReady || status.Status == playback.PrequeueStatusFailed {
				return &status
			}
		}
		if time.Now().After(deadline) {
			return &status
		}
		select {
		case <-ctx.Done():
			return &status
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// vodMediaSource describes the stream to the client. jellyfin-web always gets
// an HLS transcode URL (browsers cannot play MKV/HEVC/TrueHD directly); the
// bridge's HLS endpoint creates the mediastorm session lazily.
func (s *Server) vodMediaSource(r *http.Request, p *principal, sess *playSession, status *playback.PrequeueStatusResponse, req playbackInfoRequest) dto.MediaSourceInfo {
	streams := mediaStreamsFromStatus(status)
	probe := s.probeMedia(r.Context(), p, sess, status)
	s.applyClientCaps(p, sess, req)
	applyProbe(streams, probe)
	var runtime *int64
	if status != nil && status.Duration > 0 {
		runtime = int64Ptr(secondsToTicks(status.Duration))
	} else if probe != nil && probe.DurationSeconds > 0 {
		runtime = int64Ptr(secondsToTicks(probe.DurationSeconds))
	} else if ref := sess.Ref; ref.Kind == refMovie || ref.Kind == refEpisode {
		if item, ok := s.itemByID(r.Context(), p, nil, sess.ItemID, false); ok && item.RunTimeTicks != nil {
			runtime = item.RunTimeTicks
		}
	}

	audio := -1
	subtitle := -1
	audioFrom, subtitleFrom := "mediastorm", "mediastorm"
	if status != nil {
		audio = status.SelectedAudioTrack
		subtitle = status.SelectedSubtitleTrack
	}
	// The audio and subtitle picked earlier in the series (in a previous
	// episode or this one) win over mediastorm's own defaults (Jellyfin's
	// RememberAudioSelections and RememberSubtitleSelections).
	if remembered, ok := s.rememberedAudio(p.Profile.ID, sess.Ref, status); ok {
		audio, audioFrom = remembered, "remembered"
	}
	if remembered, ok := s.rememberedSubtitle(p.Profile.ID, sess.Ref, status); ok {
		subtitle, subtitleFrom = remembered, "remembered"
	}
	fallbackAudio, fallbackSubtitle := audio, subtitle
	if req.AudioStreamIndex != nil && *req.AudioStreamIndex >= 0 {
		audio, audioFrom = *req.AudioStreamIndex, "request"
	}
	if req.SubtitleStreamIndex != nil {
		subtitle, subtitleFrom = *req.SubtitleStreamIndex, "request"
	}
	// Without the stream's tracks the defaults above are placeholders: the
	// stream starts on mediastorm's own pick, which the first report reveals.
	tracksKnown := status != nil && len(status.AudioTracks) > 0
	sess.mu.Lock()
	sess.audioPick.handedOut(audio, fallbackAudio, audioFrom == "request", tracksKnown)
	sess.subtitlePick.handedOut(subtitle, fallbackSubtitle, subtitleFrom == "request", tracksKnown)
	sess.mu.Unlock()
	if trackLogs {
		log.Printf("[jellyfin-bridge] tracks %s (%s): default audio=%s (%s) subtitle=%s (%s); available %s",
			sess.ItemID, p.Client.Client, describeTrack(status, audio, true), audioFrom, describeTrack(status, subtitle, false), subtitleFrom, describeTracks(status))
	}
	var start int64
	if req.StartTimeTicks != nil && *req.StartTimeTicks > 0 {
		start = *req.StartTimeTicks
	}

	q := url.Values{}
	q.Set("PlaySessionId", sess.ID)
	q.Set("MediaSourceId", sess.MediaSourceID)
	q.Set("ApiKey", p.Session.Token)
	q.Set("AudioStreamIndex", strconv.Itoa(audio))
	q.Set("SubtitleStreamIndex", strconv.Itoa(subtitle))
	if start > 0 {
		q.Set("StartTimeTicks", strconv.FormatInt(start, 10))
	}
	if p.Client.DeviceID != "" {
		q.Set("DeviceId", p.Client.DeviceID)
	}

	for i := range streams {
		st := &streams[i]
		switch st.Type {
		case "Audio":
			st.IsDefault = st.Index == audio
		case "Subtitle":
			st.IsDefault = st.Index == subtitle
			if st.IsTextSubtitleStream {
				st.DeliveryMethod = "External"
				sq := url.Values{"ApiKey": {p.Session.Token}, "PlaySessionId": {sess.ID}}
				st.DeliveryURL = fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/0/Stream.vtt?%s", sess.ItemID, sess.MediaSourceID, st.Index, sq.Encode())
				st.SupportsExternalStream = true
				st.IsExternal = false
			} else {
				st.DeliveryMethod = "Encode"
			}
		}
	}

	name := "mediastorm"
	if status != nil && status.DisplayName != "" {
		name = status.DisplayName
	}
	source := dto.MediaSourceInfo{
		Protocol:               "File",
		ID:                     sess.MediaSourceID,
		Type:                   "Default",
		Container:              containerFromPath(status),
		Name:                   name,
		IsRemote:               false,
		RunTimeTicks:           runtime,
		SupportsTranscoding:    true,
		SupportsDirectStream:   false,
		SupportsDirectPlay:     false,
		SupportsProbing:        false,
		MediaStreams:           streams,
		MediaAttachments:       []any{},
		Formats:                []string{},
		RequiredHTTPHeaders:    map[string]string{},
		TranscodingURL:         fmt.Sprintf("/videos/%s/master.m3u8?%s", sess.ItemID, q.Encode()),
		TranscodingSubProtocol: "hls",
		TranscodingContainer:   "ts",
		VideoType:              "VideoFile",
		ETag:                   sess.MediaSourceID,
	}
	if status != nil && status.FileSize > 0 {
		source.Size = int64Ptr(status.FileSize)
	}
	if probe != nil && probe.FormatBitRate > 0 {
		source.Bitrate = intPtr(int(probe.FormatBitRate))
	}
	if audio >= 0 {
		source.DefaultAudioStreamIndex = intPtr(audio)
	}
	// Subtitles turned off by a remembered pick or the request are sent as
	// -1, like Jellyfin does; left out, clients apply their own default.
	if subtitle >= 0 || subtitleFrom != "mediastorm" {
		source.DefaultSubtitleStreamIndex = intPtr(subtitle)
	}
	source.HasSegments = mightHaveSegments(sess, probe)
	if s.directPlayAllowed(r, status, req, probe.videoCodec(), audio) {
		source.SupportsDirectPlay = true
		source.SupportsDirectStream = true
	}
	return source
}

func containerFromPath(status *playback.PrequeueStatusResponse) string {
	if status == nil {
		return "mkv"
	}
	path := strings.ToLower(firstNonEmpty(status.DisplayName, status.StreamPath))
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	for _, ext := range []string{"mkv", "mp4", "m4v", "avi", "ts", "webm", "mov"} {
		if strings.HasSuffix(path, "."+ext) {
			return ext
		}
	}
	return "mkv"
}

// mediaStreamsFromStatus maps prequeue track info to Jellyfin MediaStreams.
// Indexes are container (ffprobe) stream numbers, which is what both
// Jellyfin clients and mediastorm's HLS audioTrack/subtitleTrack use.
func mediaStreamsFromStatus(status *playback.PrequeueStatusResponse) []dto.MediaStream {
	streams := []dto.MediaStream{}
	if status == nil {
		return streams
	}
	video := dto.MediaStream{Type: "Video", Index: 0, Codec: "h264", DisplayTitle: "Video", IsDefault: true}
	switch {
	case status.HasDolbyVision:
		video.VideoRange, video.VideoRangeType, video.DisplayTitle = "HDR", dvRangeType(status), "Dolby Vision"
	case status.HasHDR10:
		video.VideoRange, video.VideoRangeType, video.DisplayTitle = "HDR", "HDR10", "HDR10"
	default:
		video.VideoRange, video.VideoRangeType = "SDR", "SDR"
	}
	streams = append(streams, video)
	for _, a := range status.AudioTracks {
		streams = append(streams, dto.MediaStream{
			Type:         "Audio",
			Index:        a.Index,
			Codec:        a.Codec,
			Language:     a.Language,
			Title:        a.Title,
			DisplayTitle: trackTitle(a.Language, a.Title, strings.ToUpper(a.Codec)),
			Profile:      a.Profile,
		})
	}
	for _, sub := range status.SubtitleTracks {
		idx := subtitleTrackIndex(sub)
		streams = append(streams, dto.MediaStream{
			Type:                 "Subtitle",
			Index:                idx,
			Codec:                sub.Codec,
			Language:             sub.Language,
			Title:                sub.Title,
			DisplayTitle:         trackTitle(sub.Language, sub.Title, strings.ToUpper(sub.Codec)),
			IsForced:             sub.Forced,
			IsTextSubtitleStream: !sub.IsBitmap,
		})
	}
	return streams
}

func trackTitle(lang, title, codec string) string {
	parts := []string{}
	if lang != "" && lang != "und" {
		parts = append(parts, strings.ToUpper(lang))
	}
	if title != "" {
		parts = append(parts, title)
	}
	if codec != "" {
		parts = append(parts, codec)
	}
	if len(parts) == 0 {
		return "Unknown"
	}
	return strings.Join(parts, " - ")
}

// --- reporting -----------------------------------------------------------

type playbackReport struct {
	ItemID        string `json:"ItemId"`
	PlaySessionID string `json:"PlaySessionId"`
	MediaSourceID string `json:"MediaSourceId"`
	PositionTicks *int64 `json:"PositionTicks"`
	IsPaused      bool   `json:"IsPaused"`
	IsMuted       bool   `json:"IsMuted"`
	CanSeek       bool   `json:"CanSeek"`
	PlayMethod    string `json:"PlayMethod"`
	AudioIndex    *int   `json:"AudioStreamIndex"`
	SubtitleIndex *int   `json:"SubtitleStreamIndex"`
	EventName     string `json:"EventName"`
	Failed        bool   `json:"Failed"`
}

// reissueGrace is how long before a stop report a PlaybackInfo that handed
// the session out again still counts as racing it.
const reissueGrace = 5 * time.Second

func (s *Server) handlePlaybackReport(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		received := time.Now()
		p := principalFrom(r)
		var rep playbackReport
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&rep)
		if rep.ItemID == "" {
			rep.ItemID = queryValue(r, "ItemId")
		}
		if rep.PlaySessionID == "" {
			rep.PlaySessionID = queryValue(r, "PlaySessionId")
		}
		rep.ItemID = s.itemIDOf(rep.ItemID)
		if rep.PositionTicks == nil {
			if v := queryValue(r, "PositionTicks"); v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					rep.PositionTicks = &n
				}
			}
		}
		sess, _ := s.plays.get(rep.PlaySessionID)
		if sess == nil && rep.PlaySessionID == "" && rep.ItemID != "" {
			sess, _ = s.plays.latestForItem(p.Profile.ID, normalizeID(rep.ItemID), p.Client.DeviceID, rep.MediaSourceID)
		}
		if sess != nil && sess.ProfileID != p.Profile.ID {
			sess = nil
		}
		if sess != nil && kind == "stopped" {
			// Ending the session comes first: on a track switch Android TV
			// reports the old playback stopped while it already asks
			// PlaybackInfo again, which hands out this same session; the two
			// requests race. A session handed out again just before or since
			// this report arrived lives on (unused, it idles out).
			s.plays.remove(sess.ID)
			sess.mu.Lock()
			reissued := sess.reissued.After(received.Add(-reissueGrace))
			sess.mu.Unlock()
			if reissued {
				s.plays.add(sess)
			} else {
				if rep.Failed {
					s.noteCopyFailure(p, sess)
				}
				s.stopHLS(r.Context(), p, sess)
			}
		}
		if rep.PositionTicks != nil {
			s.recordProgress(r.Context(), p, normalizeID(rep.ItemID), sess, ticksToSeconds(*rep.PositionTicks), rep.IsPaused, kind == "stopped")
			if kind == "stopped" {
				s.invalidateUserState(p.Profile.ID)
				s.notifyUserData(r.Context(), p, normalizeID(rep.ItemID))
			}
		}
		if sess == nil && kind == "start" && trackLogs {
			log.Printf("[jellyfin-bridge] report start %s (%s): unknown play session %q; track picks are not learned", rep.ItemID, p.Client.Client, rep.PlaySessionID)
		}
		if sess != nil && kind != "stopped" {
			changed := trackLogs && sess.tracksChanged(rep)
			sess.recordState(rep)
			audioOutcome, subtitleOutcome := "-", "-"
			if rep.AudioIndex != nil {
				audioOutcome = s.rememberAudio(p, sess, *rep.AudioIndex)
			}
			if rep.SubtitleIndex != nil {
				subtitleOutcome = s.rememberSubtitle(p, sess, *rep.SubtitleIndex)
			}
			if changed {
				sess.mu.Lock()
				status := sess.status
				sess.mu.Unlock()
				log.Printf("[jellyfin-bridge] report %s %s (%s): audio=%s subtitle=%s; audio memory: %s, subtitle memory: %s",
					kind, sess.ItemID, p.Client.Client, describeReported(status, rep.AudioIndex, true), describeReported(status, rep.SubtitleIndex, false), audioOutcome, subtitleOutcome)
			}
			s.keepAliveHLS(r.Context(), p, sess)
			if kind == "start" && sess.Ref.Kind == refEpisode {
				go s.prefetchNextEpisode(p, sess.ItemID)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// playingReportWindow is how recent a playback report must be for a play
// session to count as still playing.
const playingReportWindow = 2 * time.Minute

// playing reports whether the client is still playing the session. A
// PlaybackInfo during playback (a track switch, a seek) keeps its stream even
// after a ranking change, so the release does not change under the player.
func (sess *playSession) playing() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.state != nil && time.Since(sess.state.At) < playingReportWindow
}

// recordState keeps the client's latest report. Progress reports repeat the
// whole state, but fields a client leaves out keep their previous value.
func (sess *playSession) recordState(rep playbackReport) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	st := playState{}
	if sess.state != nil {
		st = *sess.state
	}
	if rep.PlayMethod != "" {
		st.PlayMethod = rep.PlayMethod
	}
	if rep.MediaSourceID != "" {
		st.MediaSourceID = rep.MediaSourceID
	}
	if rep.PositionTicks != nil {
		st.PositionTicks = rep.PositionTicks
	}
	if rep.AudioIndex != nil {
		st.Audio = rep.AudioIndex
	}
	if rep.SubtitleIndex != nil {
		st.Subtitle = rep.SubtitleIndex
	}
	st.Paused, st.Muted, st.CanSeek, st.At = rep.IsPaused, rep.IsMuted, rep.CanSeek, time.Now()
	sess.state = &st
}

// recordProgress writes a progress heartbeat into mediastorm history (which
// also drives Trakt/Simkl/... scrobbling).
func (s *Server) recordProgress(ctx context.Context, p *principal, itemID string, sess *playSession, position float64, paused, ended bool) {
	if s.deps.History == nil {
		return
	}
	ref, ok := s.ids.get(itemID)
	if !ok && sess != nil {
		ref, ok = sess.Ref, true
	}
	if !ok || (ref.Kind != refMovie && ref.Kind != refEpisode && ref.Kind != refLocal) {
		return
	}
	duration := 0.0
	if sess != nil {
		sess.mu.Lock()
		duration = sess.duration
		sess.mu.Unlock()
	}
	if duration <= 0 {
		// Reports without a live session (none sent, or it idled out) use
		// the length of the stream last played for this item.
		duration = s.knownDuration(p.Profile.ID, itemID)
	}
	// Only a measured length is remembered (items report it as their
	// runtime); metadata runtimes are whole minutes.
	s.rememberDuration(p.Profile.ID, itemID, duration)
	if duration <= 0 {
		if item, ok := s.itemByID(ctx, p, nil, itemID, false); ok && item.RunTimeTicks != nil {
			duration = ticksToSeconds(*item.RunTimeTicks)
		}
	}
	update := models.PlaybackProgressUpdate{
		Position:      position,
		Duration:      duration,
		Timestamp:     time.Now().UTC(),
		IsPaused:      paused,
		PlaybackEnded: ended,
	}
	switch ref.Kind {
	case refLocal:
		mediaType, id, ok := localProgressIdentity(ref)
		if !ok {
			return
		}
		update.MediaType, update.ItemID, update.ExternalIDs = mediaType, id, ref.Ctx.ExternalIDs
		if mediaType == "episode" {
			update.SeasonNumber, update.EpisodeNumber = ref.Ctx.Season, ref.Ctx.Episode
			update.SeriesName, update.EpisodeName = ref.Ctx.SeriesName, ref.Ctx.Name
		} else {
			update.MovieName, update.Year = ref.Ctx.Name, ref.Ctx.Year
		}
	case refMovie:
		t := titleFromRef(ref)
		update.MediaType, update.ItemID, update.MovieName, update.Year, update.ExternalIDs = "movie", t.source(), t.Name, t.Year, sourceExternalIDs(t)
		update.PosterURL = t.Poster
	case refEpisode:
		series := s.seriesWithArtwork(seriesInfoFromSeasonRef(ref))
		update.MediaType, update.ItemID = "episode", ref.Source
		update.SeasonNumber, update.EpisodeNumber = ref.Ctx.Season, ref.Ctx.Episode
		update.SeriesID, update.SeriesName, update.EpisodeName = series.source(), series.Name, ref.Ctx.Name
		update.ExternalIDs = sourceExternalIDs(series)
		update.PosterURL = series.Poster
	}
	if duration > 0 {
		update.PercentWatched = position / duration * 100
	}
	if !ended && s.deps.StopRequested != nil && s.deps.StopRequested(p.Profile.ID, update) {
		s.stopPlayback(ctx, p, itemID, sess)
	}
	if _, err := s.deps.History.UpdatePlaybackProgressContext(ctx, p.Profile.ID, update); err != nil {
		log.Printf("[jellyfin-bridge] progress for %s: %v", ref.Source, err)
	}
	s.invalidateUserState(p.Profile.ID)
}

func (s *Server) handleStopEncodings(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if sess, ok := s.plays.get(queryValue(r, "PlaySessionId")); ok && sess.ProfileID == p.Profile.ID {
		s.stopHLS(r.Context(), p, sess)
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodePlaybackInfoRequest parses a PlaybackInfo body leniently: clients send
// numeric fields both as numbers and as strings (jellyfin-web sends
// "SubtitleStreamIndex":"-1"), which strict decoding rejects wholesale.
func decodePlaybackInfoRequest(body []byte) playbackInfoRequest {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		log.Printf("[jellyfin-bridge] PlaybackInfo body: %v", err)
		return playbackInfoRequest{}
	}
	field := func(name string) (json.RawMessage, bool) {
		for k, v := range raw {
			if strings.EqualFold(k, name) {
				return v, true
			}
		}
		return nil, false
	}
	int64Field := func(name string) *int64 {
		v, ok := field(name)
		if !ok {
			return nil
		}
		s := strings.Trim(strings.TrimSpace(string(v)), `"`)
		if s == "" || s == "null" {
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	intField := func(name string) *int {
		if n := int64Field(name); n != nil {
			v := int(*n)
			return &v
		}
		return nil
	}
	boolField := func(name string) *bool {
		v, ok := field(name)
		if !ok {
			return nil
		}
		b, err := strconv.ParseBool(strings.Trim(strings.TrimSpace(string(v)), `"`))
		if err != nil {
			return nil
		}
		return &b
	}
	stringField := func(name string) string {
		v, ok := field(name)
		if !ok {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
		return ""
	}
	req := playbackInfoRequest{
		UserID:              stringField("UserId"),
		MaxStreamingBitrate: int64Field("MaxStreamingBitrate"),
		StartTimeTicks:      int64Field("StartTimeTicks"),
		AudioStreamIndex:    intField("AudioStreamIndex"),
		SubtitleStreamIndex: intField("SubtitleStreamIndex"),
		MediaSourceID:       stringField("MediaSourceId"),
		LiveStreamID:        stringField("LiveStreamId"),
		EnableDirectPlay:    boolField("EnableDirectPlay"),
		EnableDirectStream:  boolField("EnableDirectStream"),
		EnableTranscoding:   boolField("EnableTranscoding"),
	}
	if v, ok := field("DeviceProfile"); ok {
		req.DeviceProfile = v
	}
	return req
}

// handlePlaybackPing keeps the caller's HLS sessions alive while paused
// (clients ping instead of sending progress when nothing changes).
func (s *Server) handlePlaybackPing(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if sess, ok := s.plays.get(queryValue(r, "PlaySessionId")); ok && sess.ProfileID == p.Profile.ID {
		s.keepAliveHLS(r.Context(), p, sess)
	}
	w.WriteHeader(http.StatusNoContent)
}

func intOrDash(v *int) string {
	if v == nil {
		return "-"
	}
	return strconv.Itoa(*v)
}
