package jellyfinbridge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// liveChannel mirrors the fields of handlers.LiveChannel the bridge uses.
type liveChannel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Logo       string `json:"logo"`
	Group      string `json:"group"`
	TvgID      string `json:"tvgId"`
	SourceID   string `json:"sourceId"`
	PlaybackID string `json:"playbackId"`
}

type liveChannelsResponse struct {
	Channels []liveChannel `json:"channels"`
}

// channelCache keeps each profile's channel list briefly; fetching it can hit
// remote playlists.
type channelCache struct {
	mu      sync.Mutex
	entries map[string]channelCacheEntry
}

type channelCacheEntry struct {
	at       time.Time
	channels []liveChannel
}

var liveChannelCache = &channelCache{entries: map[string]channelCacheEntry{}}

const channelCacheTTL = 5 * time.Minute

func (s *Server) liveChannels(ctx context.Context, p *principal) []liveChannel {
	if s.deps.Handlers.LiveChannels == nil || p.Profile.IsKidsProfile {
		return nil
	}
	liveChannelCache.mu.Lock()
	if e, ok := liveChannelCache.entries[p.Profile.ID]; ok && time.Since(e.at) < channelCacheTTL {
		liveChannelCache.mu.Unlock()
		return e.channels
	}
	liveChannelCache.mu.Unlock()

	q := url.Values{"profileId": {p.Profile.ID}}
	var resp liveChannelsResponse
	if _, err := callJSON(s.deps.Handlers.LiveChannels, internalRequest(ctx, p, http.MethodGet, "/api/live/channels?"+q.Encode(), nil, nil), &resp); err != nil {
		return nil
	}
	liveChannelCache.mu.Lock()
	liveChannelCache.entries[p.Profile.ID] = channelCacheEntry{at: time.Now(), channels: resp.Channels}
	liveChannelCache.mu.Unlock()
	return resp.Channels
}

func (s *Server) channelGUID(ch liveChannel) string {
	return s.ids.putTrusted(refChannel, ch.ID, refContext{Name: ch.Name, Poster: ch.Logo, Extra: ch.TvgID})
}

func (s *Server) channelItem(ch liveChannel, number int, current *dto.BaseItemDto) dto.BaseItemDto {
	id := s.channelGUID(ch)
	item := dto.BaseItemDto{
		Name:           ch.Name,
		ServerID:       s.serverID,
		ID:             id,
		Type:           dto.KindTvChannel,
		ChannelType:    "TV",
		IsFolder:       boolPtr(false),
		MediaType:      dto.MediaTypeVideo,
		LocationType:   dto.LocationFileSystem,
		PlayAccess:     "Full",
		SortName:       strings.ToLower(ch.Name),
		Number:         strconv.Itoa(number),
		ChannelNumber:  strconv.Itoa(number),
		UserData:       &dto.UserItemData{Key: "channel:" + ch.ID, ItemID: id},
		CurrentProgram: current,
	}
	if ch.Logo != "" {
		item.ImageTags = map[string]string{dto.ImagePrimary: imageTag(ch.Logo)}
		item.PrimaryImageAspectRatio = floatPtr(logoAspectRatio(ch.Logo))
	}
	return item
}

// channelItems lists channels, optionally with their current programme.
func (s *Server) channelItems(ctx context.Context, p *principal, withCurrent bool) []dto.BaseItemDto {
	channels := s.liveChannels(ctx, p)
	var current map[string][]models.EPGProgram
	offset := s.epgOffset(p)
	if withCurrent {
		now := time.Now()
		current = s.schedule(p, tvgIDs(channels), now.Add(-time.Minute), now.Add(time.Minute))
	}
	out := make([]dto.BaseItemDto, 0, len(channels))
	for i, ch := range channels {
		var cur *dto.BaseItemDto
		if progs := current[ch.TvgID]; len(progs) > 0 && ch.TvgID != "" {
			prog := s.programItem(ch, i+1, progs[0], offset)
			cur = &prog
		}
		out = append(out, s.channelItem(ch, i+1, cur))
	}
	return out
}

func tvgIDs(channels []liveChannel) []string {
	seen := map[string]bool{}
	ids := make([]string, 0, len(channels))
	for _, ch := range channels {
		if ch.TvgID != "" && !seen[ch.TvgID] {
			seen[ch.TvgID] = true
			ids = append(ids, ch.TvgID)
		}
	}
	return ids
}

// epgOffset is the guide correction configured for the profile (global
// Live.EPG.TimeOffsetMinutes, overridable per profile), as in the main app.
func (s *Server) epgOffset(p *principal) time.Duration {
	if s.deps.EPGTimeOffset == nil {
		return 0
	}
	return s.deps.EPGTimeOffset(p.Profile.ID)
}

// schedule returns programmes overlapping [start, end) in display time. The
// stored guide is searched with the window shifted by -offset; programme
// times are left as stored and shifted when rendered (programItem), so
// programme IDs stay stable whatever the offset.
func (s *Server) schedule(p *principal, ids []string, start, end time.Time) map[string][]models.EPGProgram {
	if s.deps.EPG == nil || len(ids) == 0 {
		return nil
	}
	offset := s.epgOffset(p)
	return s.cleanSchedule(ids, start.Add(-offset), end.Add(-offset))
}

// scheduleMargin widens a schedule read so that a duplicate just outside the
// requested window is seen too: every request, whatever its window, then
// keeps the same entry of a pair.
const scheduleMargin = 3 * time.Hour

// cleanSchedule reads the stored schedule for [start, end) without duplicate
// or overlapping entries (see dedupePrograms).
func (s *Server) cleanSchedule(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	sched := s.deps.EPG.GetScheduleMultiple(ids, start.Add(-scheduleMargin), end.Add(scheduleMargin))
	for id, progs := range sched {
		progs = dedupePrograms(progs)
		kept := progs[:0]
		for _, prog := range progs {
			if prog.Stop.After(start) && prog.Start.Before(end) {
				kept = append(kept, prog)
			}
		}
		sched[id] = kept
	}
	return sched
}

// dedupePrograms turns a channel's programmes into one timeline. Some XMLTV
// guides merge sources and list a programme twice: at the same time, or a
// minute or two apart with a slightly different title. A programme's ID is
// its channel and start, and jellyfin-web stacks overlapping programmes in
// one guide cell, so:
//   - two programmes starting together, or overlapping by more than half of
//     the shorter one, cannot both air; the more complete entry stays (see
//     programDetail);
//   - a smaller overlap with the next programme (the sources disagree on a
//     minute) ends the earlier one where the next starts.
func dedupePrograms(progs []models.EPGProgram) []models.EPGProgram {
	if len(progs) < 2 {
		return progs
	}
	sorted := append([]models.EPGProgram(nil), progs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	out := make([]models.EPGProgram, 0, len(sorted))
	for _, prog := range sorted {
		if n := len(out); n > 0 {
			last := &out[n-1]
			overlap := minTime(last.Stop, prog.Stop).Sub(maxTime(last.Start, prog.Start))
			shorter := min(last.Stop.Sub(last.Start), prog.Stop.Sub(prog.Start))
			if prog.Start.Equal(last.Start) || (overlap > 0 && 2*overlap > shorter) {
				if programDetail(prog) > programDetail(*last) {
					*last = prog
				}
				continue
			}
			if overlap > 0 && prog.Start.After(last.Start) {
				last.Stop = prog.Start
			}
		}
		out = append(out, prog)
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// programDetail ranks duplicate entries: an episode number first, then the
// longer description, then categories and artwork.
func programDetail(prog models.EPGProgram) int {
	score := len(prog.Description) + 10*len(prog.Categories)
	if prog.Icon != "" {
		score += 10
	}
	if prog.Episode != "" {
		score += 1 << 20
	}
	return score
}

// programItem renders a stored programme; offset shifts it into display time.
func (s *Server) programItem(ch liveChannel, number int, prog models.EPGProgram, offset time.Duration) dto.BaseItemDto {
	channelID := s.channelGUID(ch)
	source := ch.ID + "@" + strconv.FormatInt(prog.Start.Unix(), 10)
	id := s.ids.put(refProgram, source, refContext{Name: prog.Title, Poster: firstNonEmpty(prog.Icon, ch.Logo), Extra: ch.ID})
	start, end := prog.Start.Add(offset).UTC(), prog.Stop.Add(offset).UTC()
	item := dto.BaseItemDto{
		Name:           prog.Title,
		ServerID:       s.serverID,
		ID:             id,
		Type:           dto.KindProgram,
		Overview:       prog.Description,
		ChannelID:      channelID,
		ChannelName:    ch.Name,
		ChannelNumber:  strconv.Itoa(number),
		ParentID:       channelID,
		StartDate:      &start,
		EndDate:        &end,
		IsFolder:       boolPtr(false),
		MediaType:      dto.MediaTypeVideo,
		LocationType:   dto.LocationFileSystem,
		Genres:         prog.Categories,
		OfficialRating: prog.Rating,
		RunTimeTicks:   int64Ptr(secondsToTicks(end.Sub(start).Seconds())),
		EpisodeTitle:   prog.Episode,
		PlayAccess:     "Full",
		UserData:       &dto.UserItemData{Key: "program:" + source, ItemID: id},
	}
	kind := classifyProgram(prog)
	item.IsMovie = boolPtr(kind.movie)
	item.IsSports = boolPtr(kind.sports)
	item.IsNews = boolPtr(kind.news)
	item.IsKids = boolPtr(kind.kids)
	item.IsSeries = boolPtr(kind.series)
	if logo := firstNonEmpty(prog.Icon, ch.Logo); logo != "" {
		item.ImageTags = map[string]string{dto.ImagePrimary: imageTag(logo)}
	}
	if ch.Logo != "" {
		item.ChannelPrimaryImageTag = imageTag(ch.Logo)
	}
	return item
}

func (s *Server) findChannel(ctx context.Context, p *principal, channelSourceID string) (liveChannel, int, bool) {
	for i, ch := range s.liveChannels(ctx, p) {
		if ch.ID == channelSourceID {
			return ch, i + 1, true
		}
	}
	return liveChannel{}, 0, false
}

func (s *Server) channelByRef(ctx context.Context, p *principal, ref itemRef) (dto.BaseItemDto, bool) {
	ch, number, ok := s.findChannel(ctx, p, ref.Source)
	if !ok {
		return dto.BaseItemDto{}, false
	}
	var cur *dto.BaseItemDto
	if ch.TvgID != "" {
		now := time.Now()
		if progs := s.schedule(p, []string{ch.TvgID}, now.Add(-time.Minute), now.Add(time.Minute))[ch.TvgID]; len(progs) > 0 {
			prog := s.programItem(ch, number, progs[0], s.epgOffset(p))
			cur = &prog
		}
	}
	return s.channelItem(ch, number, cur), true
}

func (s *Server) programByRef(ctx context.Context, p *principal, ref itemRef) (dto.BaseItemDto, bool) {
	channelSourceID, startUnix, _ := strings.Cut(ref.Source, "@")
	ch, number, ok := s.findChannel(ctx, p, channelSourceID)
	if !ok || s.deps.EPG == nil {
		return dto.BaseItemDto{}, false
	}
	unix, _ := strconv.ParseInt(startUnix, 10, 64)
	at := time.Unix(unix, 0) // stored (unshifted) start
	for _, prog := range s.cleanSchedule([]string{ch.TvgID}, at.Add(-time.Second), at.Add(time.Second))[ch.TvgID] {
		if prog.Start.Unix() == unix {
			return s.programItem(ch, number, prog, s.epgOffset(p)), true
		}
	}
	return dto.BaseItemDto{}, false
}

func (s *Server) searchChannels(ctx context.Context, p *principal, term string) []dto.BaseItemDto {
	term = strings.ToLower(term)
	var out []dto.BaseItemDto
	for i, ch := range s.liveChannels(ctx, p) {
		if strings.Contains(strings.ToLower(ch.Name), term) {
			out = append(out, s.channelItem(ch, i+1, nil))
		}
	}
	return out
}

// --- endpoints -------------------------------------------------------------

func (s *Server) handleLiveTvInfo(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	enabled := s.deps.Handlers.LiveChannels != nil && !p.Profile.IsKidsProfile
	users := []string{}
	if enabled {
		users = append(users, s.userID(p.Profile))
	}
	writeJSON(w, http.StatusOK, dto.LiveTvInfo{
		Services: []any{map[string]any{
			"Name": "mediastorm", "HomePageUrl": "", "Status": "Ok", "StatusMessage": "",
			"Version": ServerVersion, "HasUpdateAvailable": false, "IsVisible": true, "Tuners": []string{},
		}},
		IsEnabled:    enabled,
		EnabledUsers: users,
	})
}

func (s *Server) handleGuideInfo(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	writeJSON(w, http.StatusOK, dto.GuideInfo{StartDate: now.Add(-time.Hour), EndDate: now.Add(7 * 24 * time.Hour)})
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	withCurrent, _ := queryBool(r, "AddCurrentProgram")
	items := s.channelItems(r.Context(), p, withCurrent)
	q := itemQuery{
		StartIndex: parseIntDefault(queryValue(r, "StartIndex"), 0),
		Limit:      parseIntDefault(queryValue(r, "Limit"), 0),
	}
	if fav, ok := queryBool(r, "IsFavorite"); ok && fav {
		items = nil
	}
	writeJSON(w, http.StatusOK, s.page(items, q))
}

func (s *Server) handleChannel(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ref, ok := s.ids.get(pathVar(r, "id"))
	if !ok || ref.Kind != refChannel {
		writeError(w, http.StatusNotFound, "Channel not found")
		return
	}
	item, ok := s.channelByRef(r.Context(), p, ref)
	if !ok {
		writeError(w, http.StatusNotFound, "Channel not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// numberedChannel pairs a channel with its 1-based guide number.
type numberedChannel struct {
	ch     liveChannel
	number int
}

// channelsByTvg groups guide channels by tvg-id; HD/SD/backup variants of
// one channel commonly share a tvg-id and all need the same guide rows.
func (s *Server) channelsByTvg(channels []liveChannel, wanted map[string]bool) map[string][]numberedChannel {
	byTvg := map[string][]numberedChannel{}
	for i, ch := range channels {
		if ch.TvgID == "" || (len(wanted) > 0 && !wanted[s.channelGUID(ch)]) {
			continue
		}
		byTvg[ch.TvgID] = append(byTvg[ch.TvgID], numberedChannel{ch: ch, number: i + 1})
	}
	return byTvg
}

// handlePrograms serves GET and POST /LiveTv/Programs (POST carries the same
// fields as a JSON body).
func (s *Server) handlePrograms(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	params := programParams(r)
	wanted := map[string]bool{}
	for _, id := range params.ChannelIDs {
		wanted[normalizeID(id)] = true
	}
	byTvg := s.channelsByTvg(s.liveChannels(r.Context(), p), wanted)
	now := time.Now()
	start := now.Add(-time.Hour)
	end := now.Add(24 * time.Hour)
	if params.MinEndDate != nil {
		start = *params.MinEndDate
	}
	if params.MaxStartDate != nil {
		end = *params.MaxStartDate
	}
	offset := s.epgOffset(p)
	var items []dto.BaseItemDto
	if len(byTvg) > 0 {
		ids := make([]string, 0, len(byTvg))
		for tvg := range byTvg {
			ids = append(ids, tvg)
		}
		for tvg, progs := range s.schedule(p, ids, start, end) {
			for _, prog := range progs {
				shownStart, shownStop := prog.Start.Add(offset), prog.Stop.Add(offset)
				if params.IsAiring != nil && *params.IsAiring != (shownStart.Before(now) && shownStop.After(now)) {
					continue
				}
				if params.HasAired != nil && *params.HasAired != shownStop.Before(now) {
					continue
				}
				if !classifyProgram(prog).matches(params) {
					continue
				}
				for _, nc := range byTvg[tvg] {
					items = append(items, s.programItem(nc.ch, nc.number, prog, offset))
				}
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return timeOf(items[i].StartDate).Before(timeOf(items[j].StartDate)) })
	page := s.page(items, itemQuery{StartIndex: params.StartIndex, Limit: params.Limit})
	s.withTimers(r.Context(), p, page.Items)
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleRecommendedPrograms(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	params := programParams(r)
	byTvg := s.channelsByTvg(s.liveChannels(r.Context(), p), nil)
	now := time.Now()
	offset := s.epgOffset(p)
	type numbered struct {
		number int
		item   dto.BaseItemDto
	}
	var rows []numbered
	ids := make([]string, 0, len(byTvg))
	for tvg := range byTvg {
		ids = append(ids, tvg)
	}
	for tvg, progs := range s.schedule(p, ids, now.Add(-time.Minute), now.Add(time.Minute)) {
		if len(progs) == 0 || !classifyProgram(progs[0]).matches(params) {
			continue
		}
		for _, nc := range byTvg[tvg] {
			rows = append(rows, numbered{nc.number, s.programItem(nc.ch, nc.number, progs[0], offset)})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].number < rows[j].number })
	items := make([]dto.BaseItemDto, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.item)
	}
	limit := parseIntDefault(queryValue(r, "Limit"), 0)
	page := s.page(items, itemQuery{Limit: limit})
	s.withTimers(r.Context(), p, page.Items)
	writeJSON(w, http.StatusOK, page)
}

type programQueryParams struct {
	ChannelIDs   []string
	MinEndDate   *time.Time
	MaxStartDate *time.Time
	IsAiring     *bool
	HasAired     *bool
	// Programme kinds (the Live TV page's rows); nil = any.
	IsMovie, IsSeries, IsSports, IsNews, IsKids *bool
	StartIndex                                  int
	Limit                                       int
}

func programParams(r *http.Request) programQueryParams {
	var body struct {
		ChannelIDs   []string   `json:"ChannelIds"`
		MinEndDate   *time.Time `json:"MinEndDate"`
		MaxStartDate *time.Time `json:"MaxStartDate"`
		IsAiring     *bool      `json:"IsAiring"`
		HasAired     *bool      `json:"HasAired"`
		IsMovie      *bool      `json:"IsMovie"`
		IsSeries     *bool      `json:"IsSeries"`
		IsSports     *bool      `json:"IsSports"`
		IsNews       *bool      `json:"IsNews"`
		IsKids       *bool      `json:"IsKids"`
		StartIndex   int        `json:"StartIndex"`
		Limit        int        `json:"Limit"`
	}
	if r.Method == http.MethodPost {
		_ = jsonDecode(r, &body)
	}
	out := programQueryParams{
		ChannelIDs: body.ChannelIDs, MinEndDate: body.MinEndDate, MaxStartDate: body.MaxStartDate,
		IsAiring: body.IsAiring, HasAired: body.HasAired, StartIndex: body.StartIndex, Limit: body.Limit,
		IsMovie: body.IsMovie, IsSeries: body.IsSeries, IsSports: body.IsSports, IsNews: body.IsNews, IsKids: body.IsKids,
	}
	for key, dst := range map[string]**bool{"IsMovie": &out.IsMovie, "IsSeries": &out.IsSeries, "IsSports": &out.IsSports, "IsNews": &out.IsNews, "IsKids": &out.IsKids} {
		if v, ok := queryBool(r, key); ok {
			*dst = &v
		}
	}
	if ids := queryList(r, "ChannelIds"); len(ids) > 0 {
		out.ChannelIDs = ids
	}
	if t := parseDate(queryValue(r, "MinEndDate")); t != nil {
		out.MinEndDate = t
	}
	if t := parseDate(queryValue(r, "MaxStartDate")); t != nil {
		out.MaxStartDate = t
	}
	if v, ok := queryBool(r, "IsAiring"); ok {
		out.IsAiring = &v
	}
	if v, ok := queryBool(r, "HasAired"); ok {
		out.HasAired = &v
	}
	if v := queryValue(r, "StartIndex"); v != "" {
		out.StartIndex = parseIntDefault(v, 0)
	}
	if v := queryValue(r, "Limit"); v != "" {
		out.Limit = parseIntDefault(v, 0)
	}
	return out
}

// --- live playback ----------------------------------------------------------

// livePlaybackInfo returns an HLS source for a channel (or the channel behind
// a currently airing programme). RequiresOpening=false avoids the
// /LiveStreams/Open round trip.
func (s *Server) livePlaybackInfo(w http.ResponseWriter, r *http.Request, p *principal, itemID string, ref itemRef) {
	channelSourceID := ref.Source
	if ref.Kind == refProgram {
		channelSourceID, _, _ = strings.Cut(ref.Source, "@")
	}
	ch, _, ok := s.findChannel(r.Context(), p, channelSourceID)
	if !ok {
		writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{}, ErrorCode: "NoCompatibleStream"})
		return
	}
	channelRef, _ := s.ids.get(s.channelGUID(ch))
	sess := &playSession{
		ID:            randomHex(16),
		MediaSourceID: randomHex(16),
		ItemID:        itemID,
		ProfileID:     p.Profile.ID,
		DeviceID:      p.Client.DeviceID,
		Ref:           channelRef,
		Live:          true,
		liveURL:       ch.URL,
		live:          &ch,
		owner:         p,
		hls:           map[string]string{},
		hlsFiles:      map[string]string{},
		hlsOwners:     map[string]bool{},
	}
	s.plays.add(sess)

	q := url.Values{"PlaySessionId": {sess.ID}, "MediaSourceId": {sess.MediaSourceID}, "ApiKey": {p.Session.Token}}
	source := dto.MediaSourceInfo{
		Protocol:                "File",
		ID:                      sess.MediaSourceID,
		Type:                    "Default",
		Container:               "ts",
		Name:                    ch.Name,
		IsRemote:                false,
		IsInfiniteStream:        true,
		SupportsTranscoding:     true,
		SupportsDirectStream:    false,
		SupportsDirectPlay:      false,
		MediaStreams:            []dto.MediaStream{{Type: "Video", Index: 0, Codec: "h264", IsDefault: true}, {Type: "Audio", Index: 1, Codec: "aac", IsDefault: true}},
		MediaAttachments:        []any{},
		Formats:                 []string{},
		RequiredHTTPHeaders:     map[string]string{},
		TranscodingURL:          fmt.Sprintf("/videos/%s/master.m3u8?%s", itemID, q.Encode()),
		TranscodingSubProtocol:  "hls",
		TranscodingContainer:    "ts",
		DefaultAudioStreamIndex: intPtr(1),
	}
	// Live channels are never offered for direct play: only native clients
	// could use the raw MPEG-TS, and they also play the HLS URL.
	writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{source}, PlaySessionID: sess.ID})
}

func (s *Server) ensureLiveHLS(ctx context.Context, p *principal, sess *playSession) (string, string, error) {
	sess.createMu.Lock()
	defer sess.createMu.Unlock()
	if id, file, ok := s.liveHLSSession(ctx, p, sess, "live"); ok {
		return id, file, nil
	}
	sess.mu.Lock()
	ch := sess.live
	sess.mu.Unlock()
	if ch == nil {
		return "", "", fmt.Errorf("unknown channel")
	}
	q := url.Values{
		"url":         {ch.URL},
		"profileId":   {p.Profile.ID},
		"profileName": {p.Profile.Name},
		"target":      {s.liveTarget()},
		"title":       {ch.Name},
		"mediaType":   {"live"},
	}
	if ch.SourceID != "" {
		q.Set("sourceId", ch.SourceID)
	}
	if ch.PlaybackID != "" {
		q.Set("channelId", ch.PlaybackID)
	}
	if s.liveTimeshift() {
		q.Set("timeshift", "1")
	}
	var resp hlsStartResponse
	if code, err := callJSON(s.deps.Handlers.LiveHLSStart, internalRequest(ctx, p, http.MethodGet, "/api/live/hls/start?"+q.Encode(), nil, nil), &resp); err != nil {
		return "", "", classifyStartError(code, err)
	}
	if resp.SessionID == "" {
		return "", "", fmt.Errorf("no live HLS session returned")
	}
	sess.mu.Lock()
	sess.hls["live"] = resp.SessionID
	sess.hlsFiles[resp.SessionID] = playlistFile(resp.PlaylistURL)
	sess.hlsOwners[resp.SessionID] = true
	sess.mu.Unlock()
	return resp.SessionID, resp.PlaylistURL, nil
}

// forwardLiveStream serves a channel as a continuous MPEG-TS for direct play.
func (s *Server) forwardLiveStream(w http.ResponseWriter, r *http.Request, p *principal, sess *playSession) {
	sess.mu.Lock()
	ch := sess.live
	sess.mu.Unlock()
	if ch == nil || s.deps.Handlers.LiveStream == nil {
		writeError(w, http.StatusNotFound, "Channel not available")
		return
	}
	q := url.Values{"url": {ch.URL}, "profileId": {p.Profile.ID}}
	if ch.SourceID != "" {
		q.Set("liveSourceId", ch.SourceID)
	}
	forward(w, r, p, s.deps.Handlers.LiveStream, "/api/live/stream?"+q.Encode(), nil)
}
