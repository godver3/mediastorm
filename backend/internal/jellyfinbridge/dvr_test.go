package jellyfinbridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// fakeRecordings stands in for mediastorm's recordings handlers: it keeps
// recordings and rules in memory and checks the profile like they do.
type fakeRecordings struct {
	mu      sync.Mutex
	recs    []models.Recording
	rules   []models.RecordingRule
	created []map[string]any
	n       int
}

func (f *fakeRecordings) install(h *harness) {
	hs := &h.srv.deps.Handlers
	hs.RecordingsList = func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []models.Recording
		for _, rec := range f.recs {
			if rec.UserID == r.URL.Query().Get("profileId") {
				out = append(out, rec)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"recordings": out})
	}
	hs.RecordingCreateEPG = func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.created = append(f.created, in)
		start, _ := time.Parse(time.RFC3339, in["start"].(string))
		stop, _ := time.Parse(time.RFC3339, in["stop"].(string))
		f.n++
		rec := models.Recording{ID: fmt.Sprintf("0000000-0000-0000-0000-%012d", f.n), UserID: in["profileId"].(string), Status: models.RecordingStatusPending,
			ChannelID: in["channelId"].(string), ChannelName: in["channelName"].(string), Title: in["title"].(string), StartAt: start, EndAt: stop}
		f.recs = append(f.recs, rec)
		writeJSON(w, http.StatusCreated, rec)
	}
	hs.RecordingCancel = func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.recs {
			if f.recs[i].ID == mux.Vars(r)["recordingID"] {
				f.recs[i].Status = models.RecordingStatusCancelled
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
	hs.RecordingDelete = func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.recs {
			if f.recs[i].ID == mux.Vars(r)["recordingID"] {
				f.recs = append(f.recs[:i], f.recs[i+1:]...)
				break
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
	hs.RecordingRules = func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []models.RecordingRule
		for _, rule := range f.rules {
			if rule.UserID == r.URL.Query().Get("profileId") {
				out = append(out, rule)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"rules": out})
	}
	hs.RecordingRuleCreate = func(w http.ResponseWriter, r *http.Request) {
		var in models.CreateRecordingRuleRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.n++
		rule := models.RecordingRule{ID: fmt.Sprintf("1111111-0000-0000-0000-%012d", f.n), UserID: in.ProfileID, MatchType: in.MatchType, Pattern: in.Pattern,
			ChannelID: in.ChannelID, TvgID: in.TvgID, AllChannels: in.AllChannels, NewOnly: in.NewOnly, Enabled: true}
		f.rules = append(f.rules, rule)
		writeJSON(w, http.StatusCreated, rule)
	}
	hs.RecordingRuleDelete = func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.rules {
			if f.rules[i].ID == mux.Vars(r)["ruleID"] {
				f.rules = append(f.rules[:i], f.rules[i+1:]...)
				break
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
	hs.RecordingRuleUpdate = func(w http.ResponseWriter, r *http.Request) {
		var in models.UpdateRecordingRuleRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.rules {
			if f.rules[i].ID == mux.Vars(r)["ruleID"] {
				if in.NewOnly != nil {
					f.rules[i].NewOnly = *in.NewOnly
				}
				if in.PaddingAfterSeconds != nil {
					f.rules[i].PaddingAfterSeconds = *in.PaddingAfterSeconds
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}
	hs.RecordingSettings = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, models.RecordingSettings{PaddingBeforeSeconds: models.IntPtr(60), PaddingAfterSeconds: models.IntPtr(300)})
	}
}

func dvrHarness(t *testing.T) (*harness, *fakeRecordings, string) {
	t.Helper()
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.EPG = fakeEPG{}
	h.srv.deps.Handlers.LiveChannels = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"channels": []map[string]string{
			{"id": "ch-1", "name": "Channel One", "url": "http://tv.example/1.ts", "tvgId": "one", "logo": "https://img.example/1.png"},
		}})
	}
	liveChannelCache.mu.Lock()
	liveChannelCache.entries = map[string]channelCacheEntry{}
	liveChannelCache.mu.Unlock()
	f := &fakeRecordings{}
	f.install(h)
	return h, f, h.login("Living Room", "alice-pass")
}

func firstProgram(t *testing.T, h *harness, tok string) dto.BaseItemDto {
	t.Helper()
	channels := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Channels", tok, nil))
	progs := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodPost, "/LiveTv/Programs", tok, map[string]any{"ChannelIds": []string{channels.Items[0].ID}}))
	if len(progs.Items) == 0 {
		t.Fatal("no programme")
	}
	return progs.Items[0]
}

// The record button: defaults for a programme, posted back as a timer, which
// then marks the programme and is listed and cancelled as a timer.
func TestRecordProgramme(t *testing.T) {
	h, f, tok := dvrHarness(t)
	me := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", tok, nil))
	if me.Policy == nil || !me.Policy.EnableLiveTvManagement {
		t.Fatal("recording management not enabled in the user policy")
	}
	prog := firstProgram(t, h, tok)
	defaults := decode[timerInfo](t, h.do(http.MethodGet, "/LiveTv/Timers/Defaults?programId="+prog.ID, tok, nil))
	if defaults.ProgramID != prog.ID || defaults.PrePaddingSeconds != 60 || defaults.PostPaddingSeconds != 300 || defaults.Name != "News at Now" {
		t.Fatalf("defaults = %+v", defaults)
	}
	if rec := h.do(http.MethodPost, "/LiveTv/Timers", tok, defaults); rec.Code != http.StatusNoContent {
		t.Fatalf("create timer: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 1 || f.created[0]["sourceUrl"] != "http://tv.example/1.ts" || f.created[0]["profileId"] != h.alice.ID || f.created[0]["paddingAfterSeconds"] != float64(300) {
		t.Fatalf("created = %+v", f.created)
	}
	marked := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/LiveTv/Programs/"+prog.ID, tok, nil))
	// jellyfin-web needs the status too, or it offers to record again.
	if marked.TimerID == "" || marked.Status != "New" {
		t.Fatalf("programme not marked as recorded: TimerId=%q Status=%q", marked.TimerID, marked.Status)
	}
	timers := decode[dto.QueryResult[timerInfo]](t, h.do(http.MethodGet, "/LiveTv/Timers", tok, nil))
	if len(timers.Items) != 1 || timers.Items[0].ID != marked.TimerID || timers.Items[0].Status != "New" || timers.Items[0].ProgramID != prog.ID {
		t.Fatalf("timers = %+v", timers.Items)
	}
	// Another account sees nothing and cannot cancel it.
	bob := h.login("Living Room", "bob-pass")
	if rec := h.do(http.MethodDelete, "/LiveTv/Timers/"+marked.TimerID, bob, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bob cancel: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/LiveTv/Timers/"+marked.TimerID, tok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d", rec.Code)
	}
	if f.recs[0].Status != models.RecordingStatusCancelled {
		t.Fatal("recording not cancelled")
	}
	if timers := decode[dto.QueryResult[timerInfo]](t, h.do(http.MethodGet, "/LiveTv/Timers", tok, nil)); len(timers.Items) != 0 {
		t.Fatalf("cancelled timer still listed: %+v", timers.Items)
	}
}

// Record series: a title rule on the programme's channel, shown as a series
// timer and on the programme, removable.
func TestRecordSeries(t *testing.T) {
	h, f, tok := dvrHarness(t)
	prog := firstProgram(t, h, tok)
	defaults := decode[timerInfo](t, h.do(http.MethodGet, "/LiveTv/Timers/Defaults?programId="+prog.ID, tok, nil))
	if rec := h.do(http.MethodPost, "/LiveTv/SeriesTimers", tok, defaults); rec.Code != http.StatusNoContent {
		t.Fatalf("create series timer: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.rules) != 1 || f.rules[0].Pattern != "News at Now" || f.rules[0].ChannelID != "ch-1" || f.rules[0].AllChannels || f.rules[0].NewOnly || f.rules[0].MatchType != models.RecordingRuleMatchTitle {
		t.Fatalf("rules = %+v", f.rules)
	}
	series := decode[dto.QueryResult[timerInfo]](t, h.do(http.MethodGet, "/LiveTv/SeriesTimers", tok, nil))
	if len(series.Items) != 1 || series.Items[0].Name != "News at Now" || series.Items[0].ChannelID == "" {
		t.Fatalf("series timers = %+v", series.Items)
	}
	if marked := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/LiveTv/Programs/"+prog.ID, tok, nil)); marked.SeriesTimerID != series.Items[0].ID {
		t.Fatalf("programme SeriesTimerId = %q", marked.SeriesTimerID)
	}
	if rec := h.do(http.MethodDelete, "/LiveTv/SeriesTimers/"+series.Items[0].ID, tok, nil); rec.Code != http.StatusNoContent || len(f.rules) != 0 {
		t.Fatalf("delete series timer: %d, rules %+v", rec.Code, f.rules)
	}
}

// Finished and running recordings are items that play through the shared
// pipeline as recording:<id>/<file> and can be deleted.
func TestRecordingsPlayAndDelete(t *testing.T) {
	h, f, tok := dvrHarness(t)
	fp := installFakePlayback(t, h)
	_ = fp
	start := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	end := start.Add(time.Hour)
	f.recs = []models.Recording{
		{ID: "aaaaaaaa-0000-0000-0000-000000000001", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Old News", StartAt: start, EndAt: end, ActualStartAt: &start, ActualEndAt: &end, OutputPath: "/data/recordings/old.ts", OutputSizeBytes: 1000},
		{ID: "aaaaaaaa-0000-0000-0000-000000000002", UserID: h.alice.ID, Status: models.RecordingStatusPending, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Later", StartAt: end.Add(5 * time.Hour), EndAt: end.Add(6 * time.Hour)},
		{ID: "aaaaaaaa-0000-0000-0000-000000000003", UserID: "someone-else", Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Not yours", StartAt: start, EndAt: end, OutputPath: "/data/recordings/x.ts", OutputSizeBytes: 5},
	}
	list := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
	if len(list.Items) != 1 || list.Items[0].Name != "Old News" || list.Items[0].CanDelete == nil || !*list.Items[0].CanDelete || list.Items[0].ChannelID == "" {
		t.Fatalf("recordings = %+v", list.Items)
	}
	id := list.Items[0].ID
	if item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+id, tok, nil)); item.Name != "Old News" {
		t.Fatalf("item = %+v", item)
	}
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+id+"/PlaybackInfo", tok, map[string]any{}))
	if len(info.MediaSources) != 1 || info.PlaySessionID == "" {
		t.Fatalf("playback info = %+v", info)
	}
	sess, _ := h.srv.plays.get(info.PlaySessionID)
	if sess == nil || sess.status == nil || sess.status.StreamPath != "recording:aaaaaaaa-0000-0000-0000-000000000001/old.ts" {
		t.Fatalf("session stream path = %+v", sess)
	}
	bob := h.login("Living Room", "bob-pass")
	if rec := h.do(http.MethodGet, "/Items/"+id, bob, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bob sees the recording: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/Items/"+id, bob, nil); rec.Code == http.StatusNoContent {
		t.Fatal("bob deleted the recording")
	}
	if rec := h.do(http.MethodDelete, "/Items/"+id, tok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	for _, rec := range f.recs {
		if strings.HasSuffix(rec.ID, "1") {
			t.Fatal("recording not deleted")
		}
	}
}

// Recordings are also a library, as a Jellyfin server adds its recording
// folder: Plezy browses it as home videos (Video items, Recursive) and shows
// no other place for finished recordings.
func TestRecordingsLibrary(t *testing.T) {
	h, f, tok := dvrHarness(t)
	older := time.Now().Add(-26 * time.Hour).UTC().Truncate(time.Second)
	newer := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	f.recs = []models.Recording{
		{ID: "bbbbbbbb-0000-0000-0000-000000000001", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Old News", StartAt: older, EndAt: older.Add(time.Hour), OutputPath: "/data/recordings/old.ts", OutputSizeBytes: 1000},
		{ID: "bbbbbbbb-0000-0000-0000-000000000002", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Die Simpsons", StartAt: newer, EndAt: newer.Add(25 * time.Minute), OutputPath: "/data/recordings/simpsons.ts", OutputSizeBytes: 1000},
		{ID: "bbbbbbbb-0000-0000-0000-000000000003", UserID: h.alice.ID, Status: models.RecordingStatusPending, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Later", StartAt: newer.Add(9 * time.Hour), EndAt: newer.Add(10 * time.Hour)},
		{ID: "bbbbbbbb-0000-0000-0000-000000000004", UserID: "someone-else", Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Not yours", StartAt: newer, EndAt: newer.Add(time.Hour), OutputPath: "/data/recordings/x.ts", OutputSizeBytes: 5},
	}

	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", tok, nil))
	var lib dto.BaseItemDto
	for _, v := range views.Items {
		if v.Name == "Recordings" {
			lib = v
		}
	}
	if lib.ID == "" || lib.CollectionType != dto.CollectionHomeVideos || lib.Type != dto.KindCollectionFolder {
		t.Fatalf("recordings library = %+v (views %+v)", lib, views.Items)
	}
	folders := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Library/MediaFolders", tok, nil))
	found := false
	for _, v := range folders.Items {
		found = found || v.ID == lib.ID
	}
	if !found {
		t.Fatalf("recordings library missing from media folders: %+v", folders.Items)
	}

	// Plezy's browse query for a home videos library.
	items := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?ParentId="+lib.ID+"&Recursive=true&IncludeItemTypes=Video,MusicVideo", tok, nil))
	if items.TotalRecordCount != 2 || items.Items[0].Name != "Die Simpsons" || items.Items[1].Name != "Old News" || items.Items[0].Type != dto.KindVideo {
		t.Fatalf("library items = %+v", items.Items)
	}
	// The items are the recordings themselves: they play as recordings.
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+items.Items[0].ID+"/PlaybackInfo", tok, map[string]any{}))
	if sess, _ := h.srv.plays.get(info.PlaySessionID); sess == nil || sess.status == nil || sess.status.StreamPath != "recording:bbbbbbbb-0000-0000-0000-000000000002/simpsons.ts" {
		t.Fatalf("playback of a library recording = %+v", info)
	}
	search := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?ParentId="+lib.ID+"&Recursive=true&SearchTerm=news", tok, nil))
	if search.TotalRecordCount != 1 || search.Items[0].Name != "Old News" {
		t.Fatalf("search = %+v", search.Items)
	}
	latest := decode[[]dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/Latest?ParentId="+lib.ID+"&Limit=1", tok, nil))
	if len(latest) != 1 || latest[0].Name != "Die Simpsons" {
		t.Fatalf("latest = %+v", latest)
	}

	// Kids profiles cannot record and get no recordings library.
	for _, def := range h.srv.viewDefs(models.User{ID: "kid", IsKidsProfile: true}) {
		if def.Key == viewRecordings {
			t.Fatal("kids profile has a recordings library")
		}
	}
}

// Client padding is bounded (a huge value would record for days), and
// editing a scheduled recording's padding replaces it.
func TestTimerPaddingBoundedAndEditable(t *testing.T) {
	h, f, tok := dvrHarness(t)
	prog := firstProgram(t, h, tok)
	defaults := decode[timerInfo](t, h.do(http.MethodGet, "/LiveTv/Timers/Defaults?programId="+prog.ID, tok, nil))
	defaults.PostPaddingSeconds = 2592000
	h.do(http.MethodPost, "/LiveTv/Timers", tok, defaults)
	if len(f.created) != 1 || f.created[0]["paddingAfterSeconds"] != float64(maxRecordingPadding) {
		t.Fatalf("created = %+v", f.created)
	}
	timers := decode[dto.QueryResult[timerInfo]](t, h.do(http.MethodGet, "/LiveTv/Timers", tok, nil))
	edit := timers.Items[0]
	edit.PostPaddingSeconds = 600
	// This airing is on now: too close to its start to be swapped.
	if rec := h.do(http.MethodPost, "/LiveTv/Timers/"+edit.ID, tok, edit); rec.Code != http.StatusBadRequest || len(f.created) != 1 {
		t.Fatalf("edit of a starting recording: %d, created %d", rec.Code, len(f.created))
	}
	// A later one is replaced: the new one is created first, then the old
	// one cancelled.
	later := time.Now().Add(3 * time.Hour).UTC()
	f.recs = append(f.recs, models.Recording{ID: "cccccccc-0000-0000-0000-000000000001", UserID: h.alice.ID, Status: models.RecordingStatusPending,
		ChannelID: "ch-1", ChannelName: "Channel One", Title: "Later", StartAt: later, EndAt: later.Add(time.Hour), PaddingBeforeSeconds: 60})
	edit = timerInfo{PostPaddingSeconds: 600, PrePaddingSeconds: 60}
	if rec := h.do(http.MethodPost, "/LiveTv/Timers/"+compactID("cccccccc-0000-0000-0000-000000000001"), tok, edit); rec.Code != http.StatusNoContent {
		t.Fatalf("edit timer: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 2 || f.created[1]["paddingAfterSeconds"] != float64(600) || f.recs[1].Status != models.RecordingStatusCancelled {
		t.Fatalf("after edit: created %+v, old status %s", f.created, f.recs[1].Status)
	}
	// A timer with its own start and end (no programme) is bounded too.
	start := time.Now().Add(time.Hour).UTC()
	for _, end := range []time.Time{start.Add(maxTimerDuration + time.Minute), start.Add(-time.Minute)} {
		body := map[string]any{"ChannelId": prog.ChannelID, "StartDate": start, "EndDate": end}
		if rec := h.do(http.MethodPost, "/LiveTv/Timers", tok, body); rec.Code != http.StatusNotFound || len(f.created) != 2 {
			t.Fatalf("timer %s..%s: %d, created %d", start, end, rec.Code, len(f.created))
		}
	}
	body := map[string]any{"ChannelId": prog.ChannelID, "StartDate": start, "EndDate": start.Add(2 * time.Hour)}
	if rec := h.do(http.MethodPost, "/LiveTv/Timers", tok, body); rec.Code != http.StatusNoContent || len(f.created) != 3 {
		t.Fatalf("two-hour timer: %d %s, created %d", rec.Code, rec.Body.String(), len(f.created))
	}
}

// Series timers can be edited, guide markers match titles like the
// recordings service does, and a deleted rule is not shown on recordings
// it started.
func TestSeriesTimerEditsAndMatching(t *testing.T) {
	h, f, tok := dvrHarness(t)
	prog := firstProgram(t, h, tok)
	f.rules = []models.RecordingRule{{ID: "2222222-0000-0000-0000-000000000001", UserID: h.alice.ID, MatchType: models.RecordingRuleMatchTitle,
		Pattern: "news at now!", ChannelID: "ch-1", Enabled: true}}
	marked := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/LiveTv/Programs/"+prog.ID, tok, nil))
	if marked.SeriesTimerID != compactID(f.rules[0].ID) {
		t.Fatalf("normalised title rule not shown: %q", marked.SeriesTimerID)
	}
	edit := timerInfo{RecordNewOnly: boolPtr(true), PostPaddingSeconds: 120}
	if rec := h.do(http.MethodPost, "/LiveTv/SeriesTimers/"+compactID(f.rules[0].ID), tok, edit); rec.Code != http.StatusNoContent {
		t.Fatalf("edit series timer: %d", rec.Code)
	}
	if !f.rules[0].NewOnly || f.rules[0].PaddingAfterSeconds != 120 {
		t.Fatalf("rule after edit = %+v", f.rules[0])
	}
	// A running recording of a rule deleted since.
	start := time.Now().Add(-time.Minute).UTC()
	f.recs = []models.Recording{{ID: "aaaaaaaa-0000-0000-0000-00000000000a", UserID: h.alice.ID, RuleID: "gone-rule", Status: models.RecordingStatusRunning,
		ChannelID: "ch-1", ChannelName: "Channel One", Title: "News at Now", StartAt: start, EndAt: start.Add(time.Hour)}}
	timers := decode[dto.QueryResult[timerInfo]](t, h.do(http.MethodGet, "/LiveTv/Timers", tok, nil))
	if len(timers.Items) != 1 || timers.Items[0].SeriesTimerID != "" {
		t.Fatalf("timer of a deleted rule = %+v", timers.Items)
	}
}

// A single airing of a series recording is changed through its series.
func TestRuleRecordingTimerNotEdited(t *testing.T) {
	h, f, tok := dvrHarness(t)
	start := time.Now().Add(time.Hour).UTC()
	f.rules = []models.RecordingRule{{ID: "3333333-0000-0000-0000-000000000001", UserID: h.alice.ID, MatchType: models.RecordingRuleMatchTitle, Pattern: "Later", ChannelID: "ch-1", Enabled: true}}
	f.recs = []models.Recording{{ID: "bbbbbbbb-0000-0000-0000-000000000001", UserID: h.alice.ID, RuleID: f.rules[0].ID, Status: models.RecordingStatusPending,
		ChannelID: "ch-1", ChannelName: "Channel One", Title: "Later", StartAt: start, EndAt: start.Add(time.Hour)}}
	edit := timerInfo{PostPaddingSeconds: 900}
	if rec := h.do(http.MethodPost, "/LiveTv/Timers/"+compactID(f.recs[0].ID), tok, edit); rec.Code != http.StatusBadRequest {
		t.Fatalf("editing a series airing: %d", rec.Code)
	}
	if len(f.created) != 0 || f.recs[0].Status != models.RecordingStatusPending {
		t.Fatalf("series airing changed: created %d, status %s", len(f.created), f.recs[0].Status)
	}
}

// Direct players (Plezy's mpv) open a running recording's file with
// "Range: bytes=0-". As a file it would end where the recording was then,
// so it is served from mediastorm's recording stream as a seekable file of
// the estimated final size, ranges passed on. A finished recording is an
// ordinary file.
func TestRunningRecordingDirectStreamFollowsFile(t *testing.T) {
	h, f, tok := dvrHarness(t)
	var followed []string
	h.srv.deps.Handlers.RecordingStream = func(w http.ResponseWriter, r *http.Request) {
		followed = append(followed, mux.Vars(r)["recordingID"]+"|"+r.URL.Query().Get("seekable")+"|"+r.Header.Get("Range"))
		_, _ = w.Write([]byte("follow"))
	}
	h.srv.deps.Handlers.Stream = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("file"))
	}
	start := time.Now().Add(-10 * time.Minute).UTC()
	end := start.Add(5 * time.Minute)
	f.recs = []models.Recording{
		{ID: "aaaaaaaa-0000-0000-0000-0000000000f1", UserID: h.alice.ID, Status: models.RecordingStatusRunning, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Live Now", StartAt: start, EndAt: start.Add(time.Hour), ActualStartAt: &start, OutputPath: "/data/recordings/live.ts", OutputSizeBytes: 1000},
		{ID: "aaaaaaaa-0000-0000-0000-0000000000f2", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Done", StartAt: start, EndAt: end, ActualStartAt: &start, ActualEndAt: &end, OutputPath: "/data/recordings/done.ts", OutputSizeBytes: 1000},
	}
	list := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
	get := func(item dto.BaseItemDto, rangeHeader string) string {
		t.Helper()
		info := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item.ID+"/PlaybackInfo", tok, "Plezy", map[string]any{}))
		req := httptest.NewRequest(http.MethodGet, "/Videos/"+item.ID+"/stream?static=true&MediaSourceId="+info.MediaSources[0].ID, nil)
		req.Header.Set("Authorization", `MediaBrowser Client="Plezy", Device="Android", DeviceId="dev1", Version="1.0", Token="`+tok+`"`)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		rr := httptest.NewRecorder()
		h.handler.ServeHTTP(rr, req)
		return rr.Body.String()
	}
	for _, item := range list.Items {
		switch item.Name {
		case "Live Now":
			if body := get(item, "bytes=0-"); body != "follow" {
				t.Fatalf("running recording served %q", body)
			}
			if body := get(item, "bytes=5000-"); body != "follow" {
				t.Fatalf("running recording seek served %q", body)
			}
		case "Done":
			if body := get(item, "bytes=0-"); body != "file" {
				t.Fatalf("finished recording served %q", body)
			}
		}
	}
	want := []string{"aaaaaaaa-0000-0000-0000-0000000000f1|estimate|bytes=0-", "aaaaaaaa-0000-0000-0000-0000000000f1|estimate|bytes=5000-"}
	if len(followed) != 2 || followed[0] != want[0] || followed[1] != want[1] {
		t.Fatalf("recording stream requests = %v, want %v", followed, want)
	}
}

// A running recording is never offered for direct play: the file ends where
// the recording is now. HLS follows it as it grows; a finished one plays
// directly as before.
func TestRunningRecordingPlaysThroughHLS(t *testing.T) {
	h, f, tok := dvrHarness(t)
	start := time.Now().Add(-10 * time.Minute).UTC()
	end := start.Add(5 * time.Minute)
	f.recs = []models.Recording{
		{ID: "aaaaaaaa-0000-0000-0000-0000000000b1", UserID: h.alice.ID, Status: models.RecordingStatusRunning, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Live Now", StartAt: start, EndAt: start.Add(time.Hour), ActualStartAt: &start, OutputPath: "/data/recordings/live.ts", OutputSizeBytes: 1000},
		{ID: "aaaaaaaa-0000-0000-0000-0000000000b2", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Done", StartAt: start, EndAt: end, ActualStartAt: &start, ActualEndAt: &end, OutputPath: "/data/recordings/done.ts", OutputSizeBytes: 1000},
	}
	// The probe measures the partial file, as it does on a real server.
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"durationSeconds": 300})
	}
	list := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
	direct := map[string]bool{}
	for _, item := range list.Items {
		info := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item.ID+"/PlaybackInfo", tok, "Findroid", map[string]any{}))
		if len(info.MediaSources) != 1 || info.MediaSources[0].TranscodingURL == "" {
			t.Fatalf("%s: sources = %+v", item.Name, info.MediaSources)
		}
		src := info.MediaSources[0]
		direct[item.Name] = src.SupportsDirectPlay || src.SupportsDirectStream
		// A running recording has no length yet: a fixed one would stop the
		// client's seek bar from growing (jellyfin-web prefers it over the
		// player's growing duration).
		sess, _ := h.srv.plays.get(info.PlaySessionID)
		running := item.Name == "Live Now"
		if running && (src.RunTimeTicks != nil || sess == nil || sess.duration != 0) {
			t.Fatalf("running recording has a length: source %v, session %+v", src.RunTimeTicks, sess)
		}
		if !running && (src.RunTimeTicks == nil || *src.RunTimeTicks <= 0) {
			t.Fatalf("finished recording without a length: %v", src.RunTimeTicks)
		}
	}
	if len(direct) != 2 || direct["Live Now"] || !direct["Done"] {
		t.Fatalf("direct play offered = %v, want only the finished recording", direct)
	}
}
