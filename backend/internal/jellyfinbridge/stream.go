package jellyfinbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"novastream/models"
	"novastream/services/playback"
)

// lazyResolveWait bounds how long the HLS endpoint waits for a stream that
// PlaybackInfo returned before resolution finished.
const lazyResolveWait = 45 * time.Second

type hlsStartResponse struct {
	SessionID   string  `json:"sessionId"`
	PlaylistURL string  `json:"playlistUrl"`
	Duration    float64 `json:"duration"`
}

// handleMasterPlaylist is the TranscodingUrl target. It creates (or reuses) a
// mediastorm HLS session for the play session and redirects to its playlist,
// served through the bridge's /hls/ passthrough so relative segment URIs work.
func (s *Server) handleMasterPlaylist(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	itemID := normalizeID(pathVar(r, "id"))
	sess, ok := s.plays.get(queryValue(r, "PlaySessionId"))
	if !ok {
		// The play session ended (a stop report, a track change) while the
		// player still holds its URL: open a new one for the same source, as
		// Jellyfin starts a new transcode for a stale PlaySessionId.
		log.Printf("[jellyfin-bridge] master playlist for %s: play session %s unknown, opening a new one", itemID, queryValue(r, "PlaySessionId"))
		sess, ok = s.openStreamSession(r, p, itemID, queryValue(r, "MediaSourceId"))
	}
	if !ok || sess.ProfileID != p.Profile.ID || sess.ItemID != itemID {
		log.Printf("[jellyfin-bridge] master playlist for %s: no play session (found=%v)", itemID, ok)
		writeError(w, http.StatusNotFound, "Unknown play session")
		return
	}
	audio := parseIntDefault(queryValue(r, "AudioStreamIndex"), -1)

	var hlsID, playlist string
	var err error
	if sess.Live {
		hlsID, playlist, err = s.ensureLiveHLS(r.Context(), p, sess)
	} else {
		hlsID, playlist, err = s.ensureVODHLS(r.Context(), p, sess, audio)
	}
	if err != nil {
		log.Printf("[jellyfin-bridge] HLS for %s: %v", sess.ItemID, err)
		// A definitively dead stream is forgotten so the next PlaybackInfo
		// resolves a fresh one. Aborted requests (hls.js gives up after 20s
		// and retries) and still-resolving streams keep their session.
		if errors.Is(err, errStreamFailed) && r.Context().Err() == nil {
			s.stopHLS(context.Background(), p, sess)
			s.plays.remove(sess.ID)
		}
		writeError(w, http.StatusServiceUnavailable, "Stream not available")
		return
	}
	sess.mu.Lock()
	sess.currentHLS = hlsID
	sess.mu.Unlock()
	file := playlistFile(playlist)
	q := url.Values{"ApiKey": {p.Session.Token}, "token": {p.Session.Token}}
	http.Redirect(w, r, fmt.Sprintf("/videos/%s/hls/%s/%s?%s", sess.ItemID, hlsID, file, q.Encode()), http.StatusFound)
}

// ensureVODHLS starts (or reuses) the HLS session for a play session. The
// playlist always covers the whole title from 0: Jellyfin clients resume by
// seeking inside it (StartTimeTicks only positions the player), so the
// session must not be offset to the resume point.
func (s *Server) ensureVODHLS(ctx context.Context, p *principal, sess *playSession, audio int) (string, string, error) {
	sess.mu.Lock()
	owner := sess.owner
	sess.mu.Unlock()
	status := s.awaitPrequeue(ctx, p, sess, resolveWaitFor(owner, lazyResolveWait))
	if status != nil && status.Status == playback.PrequeueStatusFailed {
		return "", "", fmt.Errorf("%w: %s", errStreamFailed, firstNonEmpty(status.Error, "no playable release"))
	}
	if status == nil || status.Status != playback.PrequeueStatusReady || status.StreamPath == "" {
		return "", "", fmt.Errorf("stream still resolving")
	}
	s.startThumbnails(p, sess, status)
	key := fmt.Sprintf("a%d", audio)
	// One creation at a time per play session: clients fire duplicate
	// playlist requests, and each would otherwise start its own FFmpeg.
	sess.createMu.Lock()
	defer sess.createMu.Unlock()
	if id, file, ok := s.liveHLSSession(ctx, p, sess, key); ok {
		return id, file, nil
	}

	q := url.Values{}
	q.Set("path", status.StreamPath)
	if status.HasDolbyVision {
		q.Set("dv", "true")
		if status.DolbyVisionProfile != "" {
			q.Set("dvProfile", status.DolbyVisionProfile)
		}
	}
	if status.HasHDR10 {
		q.Set("hdr", "true")
	}
	if audio >= 0 {
		q.Set("audioTrack", strconv.Itoa(audio))
	}
	if status.Duration > 0 {
		q.Set("durationHint", strconv.FormatFloat(status.Duration, 'f', 3, 64))
	}
	// web-stable asks for a full-length, seekable timeline: Jellyfin clients
	// seek inside the playlist instead of re-requesting the stream. mediastorm
	// copies browser-compatible H.264 (cutting at the source keyframes) and
	// only re-encodes what a browser cannot play (HEVC, AV1, 10-bit, HDR/DV).
	q.Set("target", "web-stable")
	if caps := s.clientCapsParam(sess, status, audio); caps != "" {
		q.Set("clientCaps", caps)
	}
	q.Set("profileId", p.Profile.ID)
	q.Set("profileName", p.Profile.Name)
	if p.Client.DeviceID != "" {
		q.Set("clientId", "jellyfin-"+p.Client.DeviceID)
	}
	s.addMediaMetadata(q, sess)

	var resp hlsStartResponse
	if code, err := callJSON(s.deps.Handlers.HLSStart, internalRequest(ctx, p, http.MethodGet, "/api/video/hls/start?"+q.Encode(), nil, nil), &resp); err != nil {
		return "", "", classifyStartError(code, err)
	}
	if resp.SessionID == "" {
		return "", "", fmt.Errorf("no HLS session returned")
	}
	sess.mu.Lock()
	sess.hls[key] = resp.SessionID
	sess.hlsFiles[resp.SessionID] = playlistFile(resp.PlaylistURL)
	sess.hlsOwners[resp.SessionID] = true
	if resp.Duration > 0 {
		sess.duration = resp.Duration
	}
	sess.mu.Unlock()
	return resp.SessionID, resp.PlaylistURL, nil
}

// addMediaMetadata passes title info so mediastorm's Active Streams dashboard
// and notifications show what is playing.
func (s *Server) addMediaMetadata(q url.Values, sess *playSession) {
	ref := sess.Ref
	switch ref.Kind {
	case refMovie:
		t := titleFromRef(ref)
		q.Set("mediaType", "movie")
		q.Set("title", t.Name)
		q.Set("movieName", t.Name)
		q.Set("itemId", t.source()) // as in progress reports: Active Streams stops by it
		if t.Year > 0 {
			q.Set("year", strconv.Itoa(t.Year))
		}
		setExternalIDs(q, sourceExternalIDs(t))
		if t.Poster != "" {
			q.Set("posterUrl", t.Poster)
		}
	case refEpisode:
		series := s.seriesWithArtwork(seriesInfoFromSeasonRef(ref))
		q.Set("mediaType", "episode")
		q.Set("itemId", ref.Source)
		q.Set("title", series.Name)
		q.Set("seriesName", series.Name)
		q.Set("seriesId", series.source())
		series.ExternalIDs = sourceExternalIDs(series)
		q.Set("seasonNumber", strconv.Itoa(ref.Ctx.Season))
		q.Set("episodeNumber", strconv.Itoa(ref.Ctx.Episode))
		if ref.Ctx.Name != "" {
			q.Set("episodeName", ref.Ctx.Name)
		}
		setExternalIDs(q, series.ExternalIDs)
		if series.Poster != "" {
			q.Set("posterUrl", series.Poster)
		}
	}
}

func setExternalIDs(q url.Values, ext map[string]string) {
	for _, key := range []string{"imdb", "tmdb", "tvdb"} {
		if v := ext[key]; v != "" {
			q.Set(key, v)
		}
	}
}

// handleHLSFile passes HLS playlists, segments and subtitle renditions through
// to mediastorm's HLS manager, after checking the session belongs to the caller.
func (s *Server) handleHLSFile(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	hlsID := pathVar(r, "sid")
	sess, ok := s.plays.byHLS(hlsID)
	if !ok || sess.ProfileID != p.Profile.ID {
		writeError(w, http.StatusNotFound, "Unknown stream")
		return
	}
	file := pathVar(r, "file")
	token := p.Session.Token
	target := func(name string) string {
		return "/api/video/hls/" + hlsID + "/" + name + "?token=" + url.QueryEscape(token)
	}
	vars := map[string]string{"sessionID": hlsID}
	switch {
	case file == "master.m3u8":
		forward(w, r, p, s.deps.Handlers.HLSMaster, target(file), vars)
	case file == "stream.m3u8":
		forward(w, r, p, s.deps.Handlers.HLSPlaylist, target(file), vars)
	case strings.HasPrefix(file, "subtitle-") && strings.HasSuffix(file, ".m3u8"):
		vars["track"] = strings.TrimSuffix(strings.TrimPrefix(file, "subtitle-"), ".m3u8")
		forward(w, r, p, s.deps.Handlers.HLSSubPlaylist, target(file), vars)
	case strings.HasPrefix(file, "subtitles-") && strings.HasSuffix(file, ".vtt"):
		vars["track"] = strings.TrimSuffix(strings.TrimPrefix(file, "subtitles-"), ".vtt")
		forward(w, r, p, s.deps.Handlers.HLSSubTrack, target(file), vars)
	default:
		vars["segment"] = file
		forward(w, r, p, s.deps.Handlers.HLSSegment, target(file), vars)
	}
}

func (s *Server) keepAliveHLS(ctx context.Context, p *principal, sess *playSession) {
	if s.deps.Handlers.HLSKeepAlive == nil {
		return
	}
	sess.mu.Lock()
	ids := make([]string, 0, len(sess.hlsOwners))
	for id := range sess.hlsOwners {
		ids = append(ids, id)
	}
	sess.mu.Unlock()
	for _, id := range ids {
		_, _ = callJSON(s.deps.Handlers.HLSKeepAlive, internalRequest(ctx, p, http.MethodPost, "/api/video/hls/"+id+"/keepalive", nil, map[string]string{"sessionID": id}), nil)
	}
}

func (s *Server) stopHLS(ctx context.Context, p *principal, sess *playSession) {
	sess.mu.Lock()
	ids := make([]string, 0, len(sess.hlsOwners))
	for id := range sess.hlsOwners {
		ids = append(ids, id)
	}
	sess.hls = map[string]string{}
	sess.hlsOwners = map[string]bool{}
	sess.mu.Unlock()
	if s.deps.Handlers.HLSStop == nil {
		return
	}
	for _, id := range ids {
		_, _ = callJSON(s.deps.Handlers.HLSStop, internalRequest(ctx, p, http.MethodPost, "/api/video/hls/"+id+"/stop", nil, map[string]string{"sessionID": id}), nil)
	}
}

// --- direct play -----------------------------------------------------------

// directPlayAllowed decides whether to offer the raw file. Browsers cannot
// play most release containers/codecs, so only native clients (Android TV,
// Swiftfin, Findroid, ...) get direct play; they also fall back to the HLS
// TranscodingUrl on failure. audio is the audio track playback starts with.
func (s *Server) directPlayAllowed(r *http.Request, status *playback.PrequeueStatusResponse, req playbackInfoRequest, videoCodec string, audio int) bool {
	if status == nil || status.Status != playback.PrequeueStatusReady || status.StreamPath == "" {
		return false
	}
	// Clients retry with EnableDirectPlay=false after a failed attempt;
	// offering it again would loop forever.
	if req.EnableDirectPlay != nil && !*req.EnableDirectPlay {
		return false
	}
	client := strings.ToLower(requestClientInfo(r).Client)
	if client == "" || strings.Contains(client, "web") {
		return false
	}
	if strings.Contains(client, "android tv") {
		// Jellyfin's Android TV app declares FLAC, but on Fire TV its audio
		// output rejects decoded FLAC (IllegalArgumentException in
		// DefaultAudioSink) and the app's retry then crashes. It switches
		// audio tracks inside the direct played file too, so any FLAC track
		// rules direct play out; HLS copies the video and converts the audio.
		for _, track := range status.AudioTracks {
			if strings.EqualFold(track.Codec, "flac") {
				return false
			}
		}
	}
	return deviceProfileAccepts(req.DeviceProfile, containerFromPath(status), videoCodec, audioCodecOf(status, audio))
}

type deviceProfile struct {
	DirectPlayProfiles []struct {
		Container  string `json:"Container"`
		VideoCodec string `json:"VideoCodec"`
		AudioCodec string `json:"AudioCodec"`
		Type       string `json:"Type"`
	} `json:"DirectPlayProfiles"`
}

// deviceProfileAccepts checks the container and codecs against the client's
// DirectPlayProfiles. An unknown video codec (stream not probed) is not held
// against the profile; clients fall back to the HLS TranscodingUrl if
// decoding fails. No profile means the client did not restrict direct play.
func deviceProfileAccepts(raw json.RawMessage, container, videoCodec, audioCodec string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var profile deviceProfile
	if err := json.Unmarshal(raw, &profile); err != nil || profile.DirectPlayProfiles == nil {
		return true
	}
	listed := func(list, v string) bool {
		if strings.TrimSpace(list) == "" || v == "" {
			return true
		}
		for _, item := range strings.Split(list, ",") {
			if strings.EqualFold(strings.TrimSpace(item), v) {
				return true
			}
		}
		return false
	}
	for _, dp := range profile.DirectPlayProfiles {
		if dp.Type != "" && !strings.EqualFold(dp.Type, "Video") {
			continue
		}
		if listed(dp.Container, container) && listed(dp.VideoCodec, videoCodec) && listed(dp.AudioCodec, audioCodec) {
			return true
		}
	}
	return false
}

// audioCodecOf returns the codec of audio track index (the first track when
// there is no such track).
func audioCodecOf(status *playback.PrequeueStatusResponse, index int) string {
	if status == nil || len(status.AudioTracks) == 0 {
		return ""
	}
	for _, track := range status.AudioTracks {
		if track.Index == index {
			return track.Codec
		}
	}
	return status.AudioTracks[0].Codec
}

// handleStream serves /Videos/{id}/stream[.ext] for direct play. Android TV
// sends no credentials on media URLs, so the random PlaySessionId (or the
// MediaSourceId) is the capability that authorizes the request.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	itemID := normalizeID(pathVar(r, "id"))
	sess, ok := s.mediaSession(r, itemID, queryValue(r, "MediaSourceId"))
	if !ok {
		// External players stream without PlaybackInfo: open a session.
		if caller, authed := s.authenticate(r); authed {
			if s.playbackStopped(caller.Profile.ID, itemID) {
				// Opening a session would count as starting the item again.
				writeError(w, http.StatusGone, "Playback was stopped by the server")
				return
			}
			sess, ok = s.openStreamSession(r, caller, itemID, queryValue(r, "MediaSourceId"))
		}
	}
	if !ok || sess.ItemID != itemID {
		writeError(w, http.StatusNotFound, "Unknown play session")
		return
	}
	p, ok := s.principalForSession(r, sess)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if sess.Live {
		s.forwardLiveStream(w, r, p, sess)
		return
	}
	if s.playbackStopped(p.Profile.ID, sess.ItemID) {
		writeError(w, http.StatusGone, "Playback was stopped by the server")
		return
	}
	// The stream may still be resolving: PlaybackInfo answers after a
	// bounded wait, and picking a release (a preset version, Auto: Avoid
	// Video Transcoding) can take longer. Direct players such as mpv retry
	// only briefly, so wait here as the HLS playlist does.
	sess.mu.Lock()
	owner := sess.owner
	sess.mu.Unlock()
	status := s.awaitPrequeue(r.Context(), p, sess, resolveWaitFor(owner, lazyResolveWait))
	if status != nil && status.Status == playback.PrequeueStatusFailed {
		log.Printf("[jellyfin-bridge] stream for %s: %s", sess.ItemID, firstNonEmpty(status.Error, "no playable release"))
		if r.Context().Err() == nil {
			s.plays.remove(sess.ID)
		}
		writeError(w, http.StatusServiceUnavailable, "Stream not available")
		return
	}
	if status == nil || status.StreamPath == "" {
		writeError(w, http.StatusServiceUnavailable, "Stream not resolved yet")
		return
	}
	if sess.Ref.Kind == refRecording && s.streamRunningRecording(w, r, p, sess) {
		return
	}
	// transmux=0: serve the original bytes with Range support; native
	// players demux MKV/MP4 themselves.
	q := url.Values{"path": {status.StreamPath}, "profileId": {p.Profile.ID}, "transmux": {"0"}}
	// The same details as an HLS start, so Active Streams shows the profile
	// and title instead of "Unknown" and the file name.
	q.Set("profileName", p.Profile.Name)
	if p.Client.DeviceID != "" {
		q.Set("clientId", "jellyfin-"+p.Client.DeviceID)
	}
	s.addMediaMetadata(q, sess)
	forward(w, r, p, s.deps.Handlers.Stream, "/api/video/stream?"+q.Encode(), nil)
}

// mediaSession finds the play session of a media request: by PlaySessionId,
// by a media source ID the bridge handed out, or, for a request with the
// user's token, the user's newest play session of the item. Clients such as
// Plezy build static stream and subtitle URLs themselves, with the item's own
// ID as media source and no play session.
func (s *Server) mediaSession(r *http.Request, itemID, mediaSourceID string) (*playSession, bool) {
	if sess, ok := s.plays.get(queryValue(r, "PlaySessionId")); ok && sess.ItemID == itemID {
		return sess, true
	}
	if sess, ok := s.playByMediaSource(mediaSourceID); ok && sess.ItemID == itemID {
		return sess, true
	}
	// The media source ETag is the per-device source ID (a capability) even
	// when the source carries the item ID; Android TV passes it as tag on its
	// token-less direct play URL.
	if sess, ok := s.playByMediaSource(queryValue(r, "Tag")); ok && sess.ItemID == itemID {
		return sess, true
	}
	p, ok := s.authenticate(r)
	if !ok {
		return nil, false
	}
	if sess, ok := s.plays.latestForItem(p.Profile.ID, itemID, p.Client.DeviceID, ""); ok {
		return sess, true
	}
	return s.plays.latestForProfileItem(p.Profile.ID, itemID)
}

func (s *Server) playByMediaSource(mediaSourceID string) (*playSession, bool) {
	mediaSourceID = strings.ToLower(strings.TrimSpace(mediaSourceID))
	if mediaSourceID == "" {
		return nil, false
	}
	s.plays.mu.Lock()
	defer s.plays.mu.Unlock()
	// VOD media source IDs are stable per device and item, so a replaced
	// play session can share one; the newest is the live one.
	var best *playSession
	for _, sess := range s.plays.byID {
		if sess.MediaSourceID == mediaSourceID && (best == nil || sess.created.After(best.created)) {
			best = sess
		}
	}
	return best, best != nil
}

// principalForSession authenticates a media request: a token if present,
// otherwise the play session's owner (the session ID is the capability).
// The owner's token is re-validated so logout, revocation, password changes
// and account expiry also end token-less streaming.
func (s *Server) principalForSession(r *http.Request, sess *playSession) (*principal, bool) {
	if p, ok := s.authenticate(r); ok {
		return p, p.Profile.ID == sess.ProfileID
	}
	sess.mu.Lock()
	owner := sess.owner
	sess.mu.Unlock()
	if owner == nil {
		return nil, false
	}
	session, err := s.deps.Sessions.ValidateScoped(owner.Session.Token, models.SessionScopeJellyfin)
	if err != nil || (s.deps.Accounts != nil && s.deps.Accounts.IsExpired(session.AccountID)) {
		s.plays.remove(sess.ID)
		return nil, false
	}
	profile, ok := s.deps.Users.Get(sess.ProfileID)
	if !ok || !s.deps.Users.BelongsToAccount(profile.ID, session.AccountID) {
		return nil, false
	}
	return &principal{Session: session, Profile: profile, Client: owner.Client}, true
}

// liveHLSSession returns the cached mediastorm HLS session for key if it is
// still alive. Sessions reaped by the HLS manager (long pause, idle cleanup)
// are dropped so the caller creates a fresh one.
func (s *Server) liveHLSSession(ctx context.Context, p *principal, sess *playSession, key string) (string, string, bool) {
	sess.mu.Lock()
	id, ok := sess.hls[key]
	file := sess.hlsFiles[id]
	sess.mu.Unlock()
	if !ok {
		return "", "", false
	}
	if file == "" {
		file = "stream.m3u8"
	}
	if s.deps.Handlers.HLSKeepAlive != nil {
		req := internalRequest(ctx, p, http.MethodPost, "/api/video/hls/"+id+"/keepalive", nil, map[string]string{"sessionID": id})
		if code, err := callJSON(s.deps.Handlers.HLSKeepAlive, req, nil); err != nil && code == http.StatusNotFound {
			sess.mu.Lock()
			delete(sess.hls, key)
			delete(sess.hlsOwners, id)
			delete(sess.hlsFiles, id)
			sess.mu.Unlock()
			return "", "", false
		}
	}
	return id, file, true
}

// errStreamFailed marks a stream that cannot be played as resolved
// (no release found, expired debrid link, unplayable file).
var errStreamFailed = errors.New("stream failed")

// classifyStartError marks HLS start rejections that retrying the same
// resolved stream cannot fix (bad/expired source: 4xx other than rate
// limits) as definitive; server errors and cancellations stay transient.
func classifyStartError(code int, err error) error {
	if code >= 400 && code < 500 && code != http.StatusTooManyRequests && code != http.StatusRequestTimeout {
		return fmt.Errorf("%w: %v", errStreamFailed, err)
	}
	return err
}

// playlistFile names the playlist inside an HLS session URL.
func playlistFile(playlistURL string) string {
	if strings.HasSuffix(playlistURL, "master.m3u8") {
		return "master.m3u8"
	}
	return "stream.m3u8"
}

// --- external subtitles -----------------------------------------------------

type subtitleStartResponse struct {
	SessionID string `json:"sessionId"`
}

// handleSubtitle serves /Videos/{id}/{msid}/Subtitles/{index}/.../Stream.{fmt}
// by extracting the text track with mediastorm's subtitle extractor.
func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.mediaSession(r, normalizeID(pathVar(r, "id")), pathVar(r, "msid"))
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown play session")
		return
	}
	p, ok := s.principalForSession(r, sess)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	sess.mu.Lock()
	status := sess.status
	sess.mu.Unlock()
	if status == nil || status.StreamPath == "" || s.deps.Handlers.SubtitleStart == nil {
		writeError(w, http.StatusNotFound, "Subtitles not available")
		return
	}
	absolute := parseIntDefault(pathVar(r, "index"), -1)
	relative := -1
	for i, track := range status.SubtitleTracks {
		idx := subtitleTrackIndex(track)
		if idx == absolute {
			relative = i
			break
		}
	}
	if relative < 0 {
		writeError(w, http.StatusNotFound, "Unknown subtitle stream")
		return
	}
	q := url.Values{"path": {status.StreamPath}, "subtitleTrack": {strconv.Itoa(relative)}, "format": {"vtt"}}
	var started subtitleStartResponse
	if _, err := callJSON(s.deps.Handlers.SubtitleStart, internalRequest(r.Context(), p, http.MethodGet, "/api/video/subtitles/start?"+q.Encode(), nil, nil), &started); err != nil || started.SessionID == "" {
		log.Printf("[jellyfin-bridge] subtitle extract: %v", err)
		writeError(w, http.StatusServiceUnavailable, "Subtitle extraction failed")
		return
	}
	// The extractor writes progressively; clients fetch the file once, so wait
	// for it to finish (text tracks extract in seconds).
	if s.deps.SubtitleDone != nil {
		// Clients load external subtitles once, so serve them complete. Text
		// tracks of remote files are interleaved with the video and can take
		// minutes to extract.
		deadline := time.Now().Add(10 * time.Minute)
		for !s.deps.SubtitleDone(started.SessionID) && time.Now().Before(deadline) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	vars := map[string]string{"sessionID": started.SessionID}
	forward(w, r, p, s.deps.Handlers.SubtitleServe, "/api/video/subtitles/"+started.SessionID+"/subtitles.vtt", vars)
}
