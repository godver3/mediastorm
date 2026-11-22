package jellyfinbridge

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/services/playback"
	"novastream/services/recordings"
)

// Jellyfin's DVR on top of mediastorm recordings, through mediastorm's
// recordings handlers (profile ownership, channel access and padding
// defaults stay theirs):
//
//   - a Timer is a scheduled or running recording (record one programme);
//   - a SeriesTimer is a recording rule (record every airing of a title);
//   - a Recording is a running or finished recording file, played through
//     the shared pipeline as "recording:<id>/<file>" like a local file.
//
// Timer and series timer IDs are the recording and rule UUIDs without
// dashes, so they need no stored mapping.

func (s *Server) dvrEnabled(p *principal) bool {
	return s.dvrAvailable(p.Profile)
}

// dvrAvailable reports whether the profile can record Live TV (kids profiles
// cannot).
func (s *Server) dvrAvailable(profile models.User) bool {
	return s.deps.Handlers.RecordingsList != nil && s.deps.Handlers.RecordingCreateEPG != nil &&
		s.deps.Handlers.LiveChannels != nil && !profile.IsKidsProfile
}

func (s *Server) recordingsOf(ctx context.Context, p *principal) []models.Recording {
	if !s.dvrEnabled(p) {
		return nil
	}
	// userId narrows an administrator's list to this profile, profileId is
	// what a regular account's request is checked against.
	q := url.Values{"profileId": {p.Profile.ID}, "userId": {p.Profile.ID}}
	var resp struct {
		Recordings []models.Recording `json:"recordings"`
	}
	if _, err := callJSON(s.deps.Handlers.RecordingsList, internalRequest(ctx, p, http.MethodGet, "/api/live/recordings?"+q.Encode(), nil, nil), &resp); err != nil {
		return nil
	}
	out := resp.Recordings[:0]
	for _, rec := range resp.Recordings {
		if rec.UserID == p.Profile.ID {
			out = append(out, rec)
		}
	}
	return out
}

func (s *Server) rulesOf(ctx context.Context, p *principal) []models.RecordingRule {
	if !s.dvrEnabled(p) || s.deps.Handlers.RecordingRules == nil {
		return nil
	}
	q := url.Values{"profileId": {p.Profile.ID}}
	var resp struct {
		Rules []models.RecordingRule `json:"rules"`
	}
	if _, err := callJSON(s.deps.Handlers.RecordingRules, internalRequest(ctx, p, http.MethodGet, "/api/live/recording-rules?"+q.Encode(), nil, nil), &resp); err != nil {
		return nil
	}
	out := resp.Rules[:0]
	for _, rule := range resp.Rules {
		if rule.UserID == p.Profile.ID {
			out = append(out, rule)
		}
	}
	return out
}

// recordingByCompactID finds one of the profile's recordings by timer ID,
// recording UUID or recording item source.
func (s *Server) recordingByCompactID(ctx context.Context, p *principal, id string) (models.Recording, bool) {
	want := compactID(id)
	for _, rec := range s.recordingsOf(ctx, p) {
		if compactID(rec.ID) == want {
			return rec, true
		}
	}
	return models.Recording{}, false
}

func (s *Server) ruleByCompactID(ctx context.Context, p *principal, id string) (models.RecordingRule, bool) {
	want := compactID(id)
	for _, rule := range s.rulesOf(ctx, p) {
		if compactID(rule.ID) == want {
			return rule, true
		}
	}
	return models.RecordingRule{}, false
}

func (s *Server) recordingPadding(ctx context.Context, p *principal) (int, int) {
	if s.deps.Handlers.RecordingSettings == nil {
		return 0, 0
	}
	var settings models.RecordingSettings
	q := url.Values{"profileId": {p.Profile.ID}}
	if _, err := callJSON(s.deps.Handlers.RecordingSettings, internalRequest(ctx, p, http.MethodGet, "/api/live/recording-settings?"+q.Encode(), nil, nil), &settings); err != nil {
		return 0, 0
	}
	before, after := 0, 0
	if settings.PaddingBeforeSeconds != nil {
		before = *settings.PaddingBeforeSeconds
	}
	if settings.PaddingAfterSeconds != nil {
		after = *settings.PaddingAfterSeconds
	}
	return before, after
}

// --- DTOs ------------------------------------------------------------------

// timerInfo is Jellyfin's TimerInfoDto; the series fields make it the
// SeriesTimerInfoDto too (GET /LiveTv/Timers/Defaults returns that shape,
// and clients post it back to either endpoint).
type timerInfo struct {
	ID                     string           `json:"Id"`
	Type                   string           `json:"Type,omitempty"`
	ServerID               string           `json:"ServerId"`
	ChannelID              string           `json:"ChannelId,omitempty"`
	ChannelName            string           `json:"ChannelName,omitempty"`
	ChannelPrimaryImageTag string           `json:"ChannelPrimaryImageTag,omitempty"`
	ProgramID              string           `json:"ProgramId,omitempty"`
	Name                   string           `json:"Name"`
	Overview               string           `json:"Overview,omitempty"`
	StartDate              *time.Time       `json:"StartDate,omitempty"`
	EndDate                *time.Time       `json:"EndDate,omitempty"`
	ServiceName            string           `json:"ServiceName"`
	Priority               int              `json:"Priority"`
	PrePaddingSeconds      int              `json:"PrePaddingSeconds"`
	PostPaddingSeconds     int              `json:"PostPaddingSeconds"`
	IsPrePaddingRequired   bool             `json:"IsPrePaddingRequired"`
	IsPostPaddingRequired  bool             `json:"IsPostPaddingRequired"`
	KeepUntil              string           `json:"KeepUntil"`
	Status                 string           `json:"Status,omitempty"`
	SeriesTimerID          string           `json:"SeriesTimerId,omitempty"`
	RunTimeTicks           *int64           `json:"RunTimeTicks,omitempty"`
	ProgramInfo            *dto.BaseItemDto `json:"ProgramInfo,omitempty"`
	// Series timer fields.
	RecordAnyTime         *bool    `json:"RecordAnyTime,omitempty"`
	RecordAnyChannel      *bool    `json:"RecordAnyChannel,omitempty"`
	RecordNewOnly         *bool    `json:"RecordNewOnly,omitempty"`
	SkipEpisodesInLibrary *bool    `json:"SkipEpisodesInLibrary,omitempty"`
	KeepUpTo              *int     `json:"KeepUpTo,omitempty"`
	Days                  []string `json:"Days,omitempty"`
	DayPattern            string   `json:"DayPattern,omitempty"`
}

var allDays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func timerStatus(st models.RecordingStatus) string {
	switch st {
	case models.RecordingStatusPending:
		return "New"
	case models.RecordingStatusStarting, models.RecordingStatusRunning:
		return "InProgress"
	case models.RecordingStatusCompleted:
		return "Completed"
	case models.RecordingStatusCancelled:
		return "Cancelled"
	}
	return "Error"
}

func scheduled(rec models.Recording) bool {
	switch rec.Status {
	case models.RecordingStatusPending, models.RecordingStatusStarting, models.RecordingStatusRunning:
		return true
	}
	return false
}

// channelOf finds the live channel a recording or rule is on.
func (s *Server) channelOf(ctx context.Context, p *principal, channelID, tvgID string) (liveChannel, bool) {
	for _, ch := range s.liveChannels(ctx, p) {
		if (channelID != "" && ch.ID == channelID) || (channelID == "" && tvgID != "" && ch.TvgID == tvgID) {
			return ch, true
		}
	}
	return liveChannel{}, false
}

func (s *Server) timerFor(ctx context.Context, p *principal, rec models.Recording, rules []models.RecordingRule) timerInfo {
	start, end := rec.StartAt.UTC(), rec.EndAt.UTC()
	t := timerInfo{
		ID: compactID(rec.ID), Type: "Timer", ServerID: s.serverID, ChannelName: rec.ChannelName,
		Name: rec.Title, Overview: rec.Description, StartDate: &start, EndDate: &end, ServiceName: "mediastorm",
		PrePaddingSeconds: rec.PaddingBeforeSeconds, PostPaddingSeconds: rec.PaddingAfterSeconds,
		KeepUntil: "UntilDeleted", Status: timerStatus(rec.Status), SeriesTimerID: liveRuleID(rec.RuleID, rules),
		RunTimeTicks: int64Ptr(secondsToTicks(end.Sub(start).Seconds())),
	}
	if ch, ok := s.channelOf(ctx, p, rec.ChannelID, rec.TvgID); ok {
		t.ChannelID = s.channelGUID(ch)
		if ch.Logo != "" {
			t.ChannelPrimaryImageTag = imageTag(ch.Logo)
		}
		// The programme it records, when the guide has it.
		stored := rec.StartAt.Add(-s.epgOffset(p))
		ref := itemRef{Kind: refProgram, Source: ch.ID + "@" + strconv.FormatInt(stored.Unix(), 10)}
		if prog, ok := s.programByRef(ctx, p, ref); ok {
			t.ProgramID = prog.ID
			t.ProgramInfo = &prog
		}
	}
	return t
}

func (s *Server) seriesTimerFor(ctx context.Context, p *principal, rule models.RecordingRule) timerInfo {
	t := timerInfo{
		ID: compactID(rule.ID), Type: "SeriesTimer", ServerID: s.serverID, Name: rule.Pattern, ServiceName: "mediastorm",
		ChannelName: rule.ChannelName, PrePaddingSeconds: rule.PaddingBeforeSeconds, PostPaddingSeconds: rule.PaddingAfterSeconds,
		KeepUntil: "UntilDeleted", RecordAnyTime: boolPtr(true), RecordAnyChannel: boolPtr(rule.AllChannels),
		RecordNewOnly: boolPtr(rule.NewOnly), SkipEpisodesInLibrary: boolPtr(false), KeepUpTo: intPtr(0),
		Days: allDays, DayPattern: "Daily",
	}
	if !rule.AllChannels {
		if ch, ok := s.channelOf(ctx, p, rule.ChannelID, rule.TvgID); ok {
			t.ChannelID = s.channelGUID(ch)
			t.ChannelName = ch.Name
		}
	}
	return t
}

// ruleMatches tells whether a rule will record an airing, matching titles
// exactly like the recordings service. A "new airings only" rule depends on
// guide flags the programme item does not carry: its airings are marked
// through the recordings it schedules instead.
func ruleMatches(rule models.RecordingRule, title string, ch liveChannel) bool {
	if !rule.Enabled || rule.NewOnly {
		return false
	}
	if !rule.AllChannels && rule.ChannelID != ch.ID && (rule.TvgID == "" || rule.TvgID != ch.TvgID) {
		return false
	}
	return recordings.RuleMatchesTitle(rule, title)
}

// liveRuleID is a recording's rule as a series timer ID, while that rule
// still exists (a running recording keeps the ID of a deleted rule).
func liveRuleID(ruleID string, rules []models.RecordingRule) string {
	for _, rule := range rules {
		if ruleID != "" && rule.ID == ruleID {
			return compactID(ruleID)
		}
	}
	return ""
}

// clampPadding bounds client-sent padding like the recording settings do
// (0 to 1 hour), so padding cannot stretch a recording by days (an explicit
// start and end are bounded by maxTimerDuration).
func clampPadding(seconds int) int {
	return max(0, min(seconds, maxRecordingPadding))
}

const maxRecordingPadding = 3600

// maxTimerDuration bounds a timer posted with its own start and end (no
// programme), like the longest programmes in a guide.
const maxTimerDuration = 24 * time.Hour

// timerEditMargin is how far from its (padded) start a scheduled recording
// must be to be changed.
const timerEditMargin = 2 * time.Minute

// withTimers marks programmes that will be recorded: TimerId and Status for
// a scheduled recording of that airing, SeriesTimerId for a matching rule.
func (s *Server) withTimers(ctx context.Context, p *principal, items []dto.BaseItemDto) {
	hasProgram := false
	for _, it := range items {
		hasProgram = hasProgram || it.Type == dto.KindProgram
	}
	if !hasProgram || !s.dvrEnabled(p) {
		return
	}
	recs, rules := s.recordingsOf(ctx, p), s.rulesOf(ctx, p)
	if len(recs) == 0 && len(rules) == 0 {
		return
	}
	channels := map[string]liveChannel{}
	for _, ch := range s.liveChannels(ctx, p) {
		channels[ch.ID] = ch
	}
	for i := range items {
		it := &items[i]
		ref, ok := s.ids.get(it.ID)
		if it.Type != dto.KindProgram || !ok || it.StartDate == nil {
			continue
		}
		ch := channels[ref.Ctx.Extra]
		for _, rec := range recs {
			if scheduled(rec) && rec.ChannelID == ch.ID && absDuration(rec.StartAt.Sub(*it.StartDate)) < 2*time.Minute {
				it.TimerID = compactID(rec.ID)
				it.SeriesTimerID = liveRuleID(rec.RuleID, rules)
				// jellyfin-web counts a programme as recorded only with the
				// timer's status; without it the button offers to record again.
				it.Status = timerStatus(rec.Status)
			}
		}
		for _, rule := range rules {
			if it.SeriesTimerID == "" && ruleMatches(rule, it.Name, ch) {
				it.SeriesTimerID = compactID(rule.ID)
			}
		}
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// --- recordings as items ---------------------------------------------------

// playable reports whether a recording has a file to play: finished,
// running, or stopped early with something written.
func playable(rec models.Recording) bool {
	switch rec.Status {
	case models.RecordingStatusCompleted, models.RecordingStatusRunning:
		return rec.OutputPath != ""
	case models.RecordingStatusCancelled, models.RecordingStatusFailed:
		return rec.OutputPath != "" && rec.OutputSizeBytes > 0
	}
	return false
}

func (s *Server) recordingItem(ctx context.Context, p *principal, rec models.Recording) dto.BaseItemDto {
	start, end := rec.StartAt.UTC(), rec.EndAt.UTC()
	if rec.ActualStartAt != nil {
		start = rec.ActualStartAt.UTC()
	}
	if rec.ActualEndAt != nil {
		end = rec.ActualEndAt.UTC()
	}
	ch, hasChannel := s.channelOf(ctx, p, rec.ChannelID, rec.TvgID)
	image, tag := s.recordingImage(rec)
	if image == "" {
		image, tag = ch.Logo, imageTag(ch.Logo)
	}
	id := s.ids.putTrusted(refRecording, rec.ID, refContext{Name: rec.Title, ProfileID: p.Profile.ID, Poster: image, Thumb: image})
	created := rec.CreatedAt.UTC()
	item := dto.BaseItemDto{
		Name: rec.Title, ServerID: s.serverID, ID: id, Type: dto.KindVideo, Overview: rec.Description,
		ChannelName: rec.ChannelName, StartDate: &start, EndDate: &end, DateCreated: &created,
		IsFolder: boolPtr(false), MediaType: dto.MediaTypeVideo, LocationType: dto.LocationFileSystem,
		RunTimeTicks: int64Ptr(secondsToTicks(end.Sub(start).Seconds())), PlayAccess: "Full",
		CanDelete: boolPtr(true), SortName: sortName(rec.Title),
		UserData: &dto.UserItemData{Key: "recording:" + rec.ID, ItemID: id},
		Status:   map[bool]string{true: "InProgress", false: "Completed"}[scheduled(rec)],
	}
	if scheduled(rec) {
		// A timer only while it records: jellyfin-web marks every item with a
		// TimerId with the red recording dot, and Jellyfin's finished
		// recordings have none.
		item.TimerID = compactID(rec.ID)
	}
	applyRecordingGuide(&item, rec)
	if source, ok := recordingSource(id, rec, item.RunTimeTicks); ok {
		item.MediaSources = []dto.MediaSourceInfo{source}
	}
	if image != "" {
		item.ImageTags = map[string]string{dto.ImagePrimary: tag}
		if image != ch.Logo {
			// A programme image or a still: a landscape picture.
			item.ImageTags[dto.ImageThumb] = tag
			item.PrimaryImageAspectRatio = floatPtr(16.0 / 9.0)
		}
	}
	if hasChannel {
		item.ChannelID = s.channelGUID(ch)
		if ch.Logo != "" {
			item.ChannelPrimaryImageTag = imageTag(ch.Logo)
		}
	}
	return item
}

func (s *Server) recordingByRef(ctx context.Context, p *principal, ref itemRef) (dto.BaseItemDto, bool) {
	if ref.Ctx.ProfileID != p.Profile.ID {
		return dto.BaseItemDto{}, false
	}
	rec, ok := s.recordingByCompactID(ctx, p, ref.Source)
	if !ok {
		return dto.BaseItemDto{}, false
	}
	return s.recordingItem(ctx, p, rec), true
}

// recordingStreamPath is the streaming path the recordings provider serves
// (see recordings.BuildStreamPath).
func recordingStreamPath(rec models.Recording) string {
	name := filepath.Base(strings.TrimSpace(rec.OutputPath))
	if name == "" || name == "." {
		name = "recording.ts"
	}
	return "recording:" + rec.ID + "/" + name
}

func (s *Server) recordingPlaybackInfo(w http.ResponseWriter, r *http.Request, p *principal, itemID string, ref itemRef, req playbackInfoRequest) {
	rec, ok := s.recordingByCompactID(r.Context(), p, ref.Source)
	if !ok || ref.Ctx.ProfileID != p.Profile.ID || !playable(rec) {
		writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{}, ErrorCode: "NoCompatibleStream"})
		return
	}
	duration := 0.0
	// Finished (also stopped early or failed with a file): its real length.
	// A running recording grows; it has none yet.
	if rec.Status != models.RecordingStatusRunning && rec.ActualStartAt != nil && rec.ActualEndAt != nil {
		duration = rec.ActualEndAt.Sub(*rec.ActualStartAt).Seconds()
	}
	status := &playback.PrequeueStatusResponse{
		Status: playback.PrequeueStatusReady, StreamPath: recordingStreamPath(rec), DisplayName: rec.Title,
		Duration: duration, SelectedAudioTrack: -1, SelectedSubtitleTrack: -1,
	}
	if source, ok := recordingSource(itemID, rec, nil); ok && source.Size != nil {
		status.FileSize = *source.Size // the player's stats show it
	}
	sess := &playSession{
		ID: randomHex(16), MediaSourceID: s.mediaSourceID(p.Profile.ID, p.Client.DeviceID, itemID), ItemID: itemID,
		ProfileID: p.Profile.ID, DeviceID: p.Client.DeviceID, Ref: ref, owner: p, status: status, duration: duration,
		hls: map[string]string{}, hlsFiles: map[string]string{}, hlsOwners: map[string]bool{},
	}
	s.plays.add(sess)
	source := s.vodMediaSource(r, p, sess, status, req)
	if s.deps.RecordingCommercials != nil && len(s.deps.RecordingCommercials(rec.OutputPath)) > 0 {
		source.HasSegments = true // jellyfin-web asks for segments only then
	}
	if rec.Status == models.RecordingStatusRunning {
		// The file is still being written: played directly it ends where
		// the recording is now. HLS follows it as it grows.
		source.SupportsDirectPlay, source.SupportsDirectStream = false, false
		// Its length is not known yet: what a probe of the partial file
		// says would fix the client's seek bar at that length. Without one,
		// jellyfin-web takes the player's duration, which grows with the
		// EVENT playlist.
		source.RunTimeTicks = nil
		// Progress reports must not measure against it either.
		sess.mu.Lock()
		sess.duration = 0
		sess.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{source}, PlaySessionID: sess.ID})
}

// recordingItems lists the profile's playable recordings (finished and
// running) as items, newest first like Jellyfin's recordings view. keep, when
// set, narrows them.
func (s *Server) recordingItems(ctx context.Context, p *principal, keep func(models.Recording, dto.BaseItemDto) bool) []dto.BaseItemDto {
	var items []dto.BaseItemDto
	for _, rec := range s.recordingsOf(ctx, p) {
		if !playable(rec) {
			continue
		}
		item := s.recordingItem(ctx, p, rec)
		if keep != nil && !keep(rec, item) {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return timeOf(items[i].StartDate).After(timeOf(items[j].StartDate)) })
	return items
}

// --- endpoints ---------------------------------------------------------------

func (s *Server) handleRecordings(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var inProgress *bool
	if v := queryValue(r, "IsInProgress"); v != "" {
		b := strings.EqualFold(v, "true")
		inProgress = &b
	}
	channel := normalizeID(queryValue(r, "ChannelId"))
	items := s.recordingItems(r.Context(), p, func(rec models.Recording, item dto.BaseItemDto) bool {
		return (inProgress == nil || *inProgress == scheduled(rec)) && (channel == "" || item.ChannelID == channel)
	})
	q := parseItemQuery(r)
	page := s.page(items, itemQuery{StartIndex: q.StartIndex, Limit: q.Limit})
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleRecording(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if rec, ok := s.recordingByCompactID(r.Context(), p, pathVar(r, "id")); ok {
		writeJSON(w, http.StatusOK, s.recordingItem(r.Context(), p, rec))
		return
	}
	if ref, ok := s.ids.get(pathVar(r, "id")); ok && ref.Kind == refRecording {
		if item, ok := s.recordingByRef(r.Context(), p, ref); ok {
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	writeError(w, http.StatusNotFound, "Recording not found")
}

// deleteRecording removes one of the profile's recordings (and its file),
// stopping it first if it is still recording.
func (s *Server) deleteRecording(w http.ResponseWriter, r *http.Request, p *principal, id string) {
	rec, ok := s.recordingByCompactID(r.Context(), p, id)
	if !ok {
		writeError(w, http.StatusNotFound, "Recording not found")
		return
	}
	if scheduled(rec) {
		// Deleting a recording in progress stops it first (as in Jellyfin);
		// mediastorm does not delete active recordings.
		cancel := internalRequest(r.Context(), p, http.MethodPost, "/api/live/recordings/"+url.PathEscape(rec.ID)+"/cancel", nil, map[string]string{"recordingID": rec.ID})
		if code, err := callJSON(s.deps.Handlers.RecordingCancel, cancel, nil); err != nil {
			writeError(w, code, err.Error())
			return
		}
	}
	req := internalRequest(r.Context(), p, http.MethodDelete, "/api/live/recordings/"+url.PathEscape(rec.ID), nil, map[string]string{"recordingID": rec.ID})
	if code, err := callJSON(s.deps.Handlers.RecordingDelete, req, nil); err != nil {
		writeError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id := pathVar(r, "id")
	if ref, ok := s.ids.get(id); ok && ref.Kind == refRecording && ref.Ctx.ProfileID == p.Profile.ID {
		id = ref.Source
	}
	s.deleteRecording(w, r, p, id)
}

func (s *Server) handleTimers(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	channel := normalizeID(queryValue(r, "ChannelId"))
	series := compactID(queryValue(r, "SeriesTimerId"))
	timers := []timerInfo{}
	rules := s.rulesOf(r.Context(), p)
	for _, rec := range s.recordingsOf(r.Context(), p) {
		if !scheduled(rec) {
			continue
		}
		t := s.timerFor(r.Context(), p, rec, rules)
		if (channel != "" && t.ChannelID != channel) || (series != "" && t.SeriesTimerID != series) {
			continue
		}
		timers = append(timers, t)
	}
	sort.SliceStable(timers, func(i, j int) bool { return timeOf(timers[i].StartDate).Before(timeOf(timers[j].StartDate)) })
	writeJSON(w, http.StatusOK, dto.QueryResult[timerInfo]{Items: timers, TotalRecordCount: len(timers)})
}

func (s *Server) handleTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rec, ok := s.recordingByCompactID(r.Context(), p, pathVar(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Timer not found")
		return
	}
	writeJSON(w, http.StatusOK, s.timerFor(r.Context(), p, rec, s.rulesOf(r.Context(), p)))
}

// handleTimerDefaults answers GET /LiveTv/Timers/Defaults?programId=: the
// programme with the profile's padding, which the record buttons post back.
func (s *Server) handleTimerDefaults(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	before, after := s.recordingPadding(r.Context(), p)
	t := timerInfo{
		ServerID: s.serverID, ServiceName: "mediastorm", PrePaddingSeconds: before, PostPaddingSeconds: after,
		KeepUntil: "UntilDeleted", RecordAnyTime: boolPtr(false), RecordAnyChannel: boolPtr(false),
		RecordNewOnly: boolPtr(false), SkipEpisodesInLibrary: boolPtr(false), KeepUpTo: intPtr(0), Days: allDays, DayPattern: "Daily",
	}
	if id := queryValue(r, "programId"); id != "" {
		ref, ok := s.ids.get(id)
		if !ok || ref.Kind != refProgram {
			writeError(w, http.StatusNotFound, "Program not found")
			return
		}
		prog, ok := s.programByRef(r.Context(), p, ref)
		if !ok {
			writeError(w, http.StatusNotFound, "Program not found")
			return
		}
		t.ProgramID, t.ChannelID, t.ChannelName = prog.ID, prog.ChannelID, prog.ChannelName
		t.Name, t.Overview, t.StartDate, t.EndDate = prog.Name, prog.Overview, prog.StartDate, prog.EndDate
		t.ProgramInfo = &prog
	}
	writeJSON(w, http.StatusOK, t)
}

// timerRequest is what clients post to create a timer or series timer.
type timerRequest struct {
	ProgramID          string     `json:"ProgramId"`
	ChannelID          string     `json:"ChannelId"`
	Name               string     `json:"Name"`
	Overview           string     `json:"Overview"`
	StartDate          *time.Time `json:"StartDate"`
	EndDate            *time.Time `json:"EndDate"`
	PrePaddingSeconds  *int       `json:"PrePaddingSeconds"`
	PostPaddingSeconds *int       `json:"PostPaddingSeconds"`
	RecordAnyChannel   bool       `json:"RecordAnyChannel"`
	RecordNewOnly      *bool      `json:"RecordNewOnly"` // nil: unchanged on edit
}

// resolveTimer finds the channel and airing a timer request means: its
// programme, or a channel with explicit start and end.
func (s *Server) resolveTimer(ctx context.Context, p *principal, in timerRequest) (liveChannel, dto.BaseItemDto, bool) {
	if ref, ok := s.ids.get(in.ProgramID); ok && ref.Kind == refProgram {
		prog, ok := s.programByRef(ctx, p, ref)
		if !ok {
			return liveChannel{}, dto.BaseItemDto{}, false
		}
		channelSourceID, _, _ := strings.Cut(ref.Source, "@")
		ch, _, ok := s.findChannel(ctx, p, channelSourceID)
		return ch, prog, ok
	}
	ref, ok := s.ids.get(in.ChannelID)
	if !ok || ref.Kind != refChannel || in.StartDate == nil || in.EndDate == nil ||
		!in.EndDate.After(*in.StartDate) || in.EndDate.Sub(*in.StartDate) > maxTimerDuration {
		return liveChannel{}, dto.BaseItemDto{}, false
	}
	ch, _, ok := s.findChannel(ctx, p, ref.Source)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = ch.Name
	}
	return ch, dto.BaseItemDto{Name: name, Overview: in.Overview, StartDate: in.StartDate, EndDate: in.EndDate}, ok
}

func decodeTimerRequest(w http.ResponseWriter, r *http.Request) (timerRequest, bool) {
	var in timerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid timer")
		return in, false
	}
	return in, true
}

func (s *Server) handleCreateTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	in, ok := decodeTimerRequest(w, r)
	if !ok {
		return
	}
	if !s.dvrEnabled(p) {
		writeError(w, http.StatusForbidden, "Recording is not available")
		return
	}
	ch, prog, ok := s.resolveTimer(r.Context(), p, in)
	if !ok {
		writeError(w, http.StatusNotFound, "Program not found")
		return
	}
	payload := map[string]any{
		"profileId": p.Profile.ID, "channelId": ch.ID, "tvgId": ch.TvgID, "channelName": ch.Name,
		"title": prog.Name, "description": prog.Overview, "sourceUrl": ch.URL,
		"start": prog.StartDate.UTC().Format(time.RFC3339), "stop": prog.EndDate.UTC().Format(time.RFC3339),
	}
	if in.PrePaddingSeconds != nil {
		payload["paddingBeforeSeconds"] = clampPadding(*in.PrePaddingSeconds)
	}
	if in.PostPaddingSeconds != nil {
		payload["paddingAfterSeconds"] = clampPadding(*in.PostPaddingSeconds)
	}
	body, _ := json.Marshal(payload)
	if code, err := callJSON(s.deps.Handlers.RecordingCreateEPG, internalRequest(r.Context(), p, http.MethodPost, "/api/live/recordings/epg", body, nil), nil); err != nil {
		writeError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dropRecording removes a recording the bridge just created (undoing a
// replacement that could not take the old one's place). It is cancelled
// first: a replacement inside its padding window may already have started,
// and an active recording cannot be deleted.
func (s *Server) dropRecording(ctx context.Context, p *principal, id string) {
	if id == "" {
		return
	}
	cancel := internalRequest(ctx, p, http.MethodPost, "/api/live/recordings/"+url.PathEscape(id)+"/cancel", nil, map[string]string{"recordingID": id})
	if _, err := callJSON(s.deps.Handlers.RecordingCancel, cancel, nil); err != nil {
		log.Printf("[jellyfin-bridge] recordings: cancelling replacement %s: %v", id, err)
	}
	req := internalRequest(ctx, p, http.MethodDelete, "/api/live/recordings/"+url.PathEscape(id), nil, map[string]string{"recordingID": id})
	if _, err := callJSON(s.deps.Handlers.RecordingDelete, req, nil); err != nil {
		log.Printf("[jellyfin-bridge] recordings: removing replacement %s: %v", id, err)
	}
}

// handleCancelTimer cancels a scheduled recording, or stops a running one
// (what it wrote stays as a recording).
func (s *Server) handleCancelTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rec, ok := s.recordingByCompactID(r.Context(), p, pathVar(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Timer not found")
		return
	}
	req := internalRequest(r.Context(), p, http.MethodPost, "/api/live/recordings/"+url.PathEscape(rec.ID)+"/cancel", nil, map[string]string{"recordingID": rec.ID})
	if code, err := callJSON(s.deps.Handlers.RecordingCancel, req, nil); err != nil {
		writeError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSeriesTimers(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	timers := []timerInfo{}
	for _, rule := range s.rulesOf(r.Context(), p) {
		timers = append(timers, s.seriesTimerFor(r.Context(), p, rule))
	}
	writeJSON(w, http.StatusOK, dto.QueryResult[timerInfo]{Items: timers, TotalRecordCount: len(timers)})
}

func (s *Server) handleSeriesTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rule, ok := s.ruleByCompactID(r.Context(), p, pathVar(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Series timer not found")
		return
	}
	writeJSON(w, http.StatusOK, s.seriesTimerFor(r.Context(), p, rule))
}

// handleCreateSeriesTimer records every airing of the programme's title, on
// its channel or on any channel.
func (s *Server) handleCreateSeriesTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	in, ok := decodeTimerRequest(w, r)
	if !ok {
		return
	}
	if !s.dvrEnabled(p) || s.deps.Handlers.RecordingRuleCreate == nil {
		writeError(w, http.StatusForbidden, "Recording is not available")
		return
	}
	ch, prog, ok := s.resolveTimer(r.Context(), p, in)
	title := strings.TrimSpace(firstNonEmpty(prog.Name, in.Name))
	if (!ok && !in.RecordAnyChannel) || title == "" {
		writeError(w, http.StatusNotFound, "Program not found")
		return
	}
	payload := map[string]any{
		"profileId": p.Profile.ID, "matchType": models.RecordingRuleMatchTitle, "pattern": title,
		"allChannels": in.RecordAnyChannel, "newOnly": in.RecordNewOnly != nil && *in.RecordNewOnly,
	}
	if !in.RecordAnyChannel {
		payload["channelId"], payload["tvgId"], payload["channelName"] = ch.ID, ch.TvgID, ch.Name
	}
	if in.PrePaddingSeconds != nil {
		payload["paddingBeforeSeconds"] = clampPadding(*in.PrePaddingSeconds)
	}
	if in.PostPaddingSeconds != nil {
		payload["paddingAfterSeconds"] = clampPadding(*in.PostPaddingSeconds)
	}
	body, _ := json.Marshal(payload)
	if code, err := callJSON(s.deps.Handlers.RecordingRuleCreate, internalRequest(r.Context(), p, http.MethodPost, "/api/live/recording-rules", body, nil), nil); err != nil {
		writeError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCancelSeriesTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	rule, ok := s.ruleByCompactID(r.Context(), p, pathVar(r, "id"))
	if !ok || s.deps.Handlers.RecordingRuleDelete == nil {
		writeError(w, http.StatusNotFound, "Series timer not found")
		return
	}
	q := url.Values{"profileId": {p.Profile.ID}}
	req := internalRequest(r.Context(), p, http.MethodDelete, "/api/live/recording-rules/"+url.PathEscape(rule.ID)+"?"+q.Encode(), nil, map[string]string{"ruleID": rule.ID})
	if code, err := callJSON(s.deps.Handlers.RecordingRuleDelete, req, nil); err != nil {
		writeError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateTimer applies a client's changes to a scheduled recording
// (jellyfin-web's recording editor: padding). mediastorm recordings cannot
// be edited, so a pending one is replaced; a running one cannot change.
func (s *Server) handleUpdateTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	in, ok := decodeTimerRequest(w, r)
	if !ok {
		return
	}
	rec, found := s.recordingByCompactID(r.Context(), p, pathVar(r, "id"))
	if !found {
		writeError(w, http.StatusNotFound, "Timer not found")
		return
	}
	before, after := rec.PaddingBeforeSeconds, rec.PaddingAfterSeconds
	if in.PrePaddingSeconds != nil {
		before = clampPadding(*in.PrePaddingSeconds)
	}
	if in.PostPaddingSeconds != nil {
		after = clampPadding(*in.PostPaddingSeconds)
	}
	if before == rec.PaddingBeforeSeconds && after == rec.PaddingAfterSeconds {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if rec.Status != models.RecordingStatusPending {
		writeError(w, http.StatusBadRequest, "A recording that has started cannot be changed")
		return
	}
	// Neither the recording nor its replacement may start while they are
	// swapped: both would write the same file, and undoing a started
	// replacement could remove the file the original is writing.
	earliest := rec.StartAt.Add(-time.Duration(max(before, rec.PaddingBeforeSeconds)) * time.Second)
	if time.Until(earliest) < timerEditMargin {
		writeError(w, http.StatusBadRequest, "This recording starts too soon to be changed")
		return
	}
	if rec.RuleID != "" {
		// A replacement would no longer belong to the rule (and the rule
		// would not schedule this airing again): change the series.
		writeError(w, http.StatusBadRequest, "This recording belongs to a series recording; change the series instead")
		return
	}
	ch, ok := s.channelOf(r.Context(), p, rec.ChannelID, rec.TvgID)
	if !ok {
		writeError(w, http.StatusNotFound, "Channel not available")
		return
	}
	// The replacement is created first and the old one cancelled after, so a
	// failure never leaves the airing without a recording; a client hanging
	// up halfway does not stop the swap.
	ctx := context.WithoutCancel(r.Context())
	payload := map[string]any{
		"profileId": p.Profile.ID, "channelId": ch.ID, "tvgId": ch.TvgID, "channelName": ch.Name,
		"title": rec.Title, "description": rec.Description, "sourceUrl": ch.URL,
		"start": rec.StartAt.UTC().Format(time.RFC3339), "stop": rec.EndAt.UTC().Format(time.RFC3339),
		"paddingBeforeSeconds": before, "paddingAfterSeconds": after,
	}
	body, _ := json.Marshal(payload)
	var created models.Recording
	if code, err := callJSON(s.deps.Handlers.RecordingCreateEPG, internalRequest(ctx, p, http.MethodPost, "/api/live/recordings/epg", body, nil), &created); err != nil {
		writeError(w, code, err.Error())
		return
	}
	// Still pending? Pre-padding may have started it meanwhile.
	current, found := s.recordingByCompactID(ctx, p, rec.ID)
	cancelReq := internalRequest(ctx, p, http.MethodPost, "/api/live/recordings/"+url.PathEscape(rec.ID)+"/cancel", nil, map[string]string{"recordingID": rec.ID})
	if !found || current.Status != models.RecordingStatusPending {
		s.dropRecording(ctx, p, created.ID)
		writeError(w, http.StatusBadRequest, "A recording that has started cannot be changed")
		return
	}
	if code, err := callJSON(s.deps.Handlers.RecordingCancel, cancelReq, nil); err != nil {
		s.dropRecording(ctx, p, created.ID)
		writeError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateSeriesTimer applies a client's changes to a recording rule:
// padding and "new airings only".
func (s *Server) handleUpdateSeriesTimer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	in, ok := decodeTimerRequest(w, r)
	if !ok {
		return
	}
	rule, found := s.ruleByCompactID(r.Context(), p, pathVar(r, "id"))
	if !found || s.deps.Handlers.RecordingRuleUpdate == nil {
		writeError(w, http.StatusNotFound, "Series timer not found")
		return
	}
	update := models.UpdateRecordingRuleRequest{ProfileID: p.Profile.ID, NewOnly: in.RecordNewOnly}
	if in.PrePaddingSeconds != nil {
		v := clampPadding(*in.PrePaddingSeconds)
		update.PaddingBeforeSeconds = &v
	}
	if in.PostPaddingSeconds != nil {
		v := clampPadding(*in.PostPaddingSeconds)
		update.PaddingAfterSeconds = &v
	}
	body, _ := json.Marshal(update)
	req := internalRequest(r.Context(), p, http.MethodPut, "/api/live/recording-rules/"+url.PathEscape(rule.ID), body, map[string]string{"ruleID": rule.ID})
	if code, err := callJSON(s.deps.Handlers.RecordingRuleUpdate, req, nil); err != nil {
		writeError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// streamRunningRecording serves a direct player (Plezy, external players) a
// recording that is still being written. Read as a file it would end where
// the recording was when the player opened it, and streamed without a length
// the player (mpv) shows only what it has buffered and cannot seek. So it
// goes to mediastorm's recording stream in its "seekable=estimate" mode: a
// seekable file of the size the recording will probably reach, whose reads
// wait for the recording to catch up (handlers/recordings_growing_estimate.go).
// It reports false for anything but a running recording.
func (s *Server) streamRunningRecording(w http.ResponseWriter, r *http.Request, p *principal, sess *playSession) bool {
	if s.deps.Handlers.RecordingStream == nil {
		return false
	}
	rec, ok := s.recordingByCompactID(r.Context(), p, sess.Ref.Source)
	if !ok || rec.Status != models.RecordingStatusRunning {
		return false
	}
	forward(w, r, p, s.deps.Handlers.RecordingStream, "/api/live/recordings/"+url.PathEscape(rec.ID)+"/stream?seekable=estimate", map[string]string{"recordingID": rec.ID})
	return true
}

// recordingSegments are a recording's commercial breaks, found by comskip
// after the recording finished, as Jellyfin "Commercial" segments (clients
// offer to skip them, or skip them, per their segment settings).
func (s *Server) recordingSegments(ctx context.Context, p *principal, sess *playSession) (segments []dto.MediaSegmentDto, found bool) {
	segments = []dto.MediaSegmentDto{}
	if s.deps.RecordingCommercials == nil {
		return segments, true
	}
	rec, ok := s.recordingByCompactID(ctx, p, sess.Ref.Source)
	if !ok {
		return segments, false // maybe a passing lookup failure: not cached
	}
	for _, b := range s.deps.RecordingCommercials(rec.OutputPath) {
		segments = append(segments, dto.MediaSegmentDto{
			ID:         deriveID("segment", sess.ItemID+"/Commercial/"+strconv.FormatFloat(b.Start, 'f', 3, 64)),
			ItemID:     sess.ItemID,
			Type:       "Commercial",
			StartTicks: secondsToTicks(b.Start),
			EndTicks:   secondsToTicks(b.End),
		})
	}
	return segments, true
}
