package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/notifications"
	"novastream/services/peartube"
)

// newAutoSeedTracker builds a stream tracker wired the way main wires the global
// one: the notification observer first, then the p2p integration on both the
// matched-activity fanout and the playback-start signal.
func newAutoSeedTracker(seeder *PearTubeHandler, observers ...PlaybackActivityObserver) *StreamTracker {
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	for _, observer := range observers {
		tracker.AddPlaybackActivityObserver(observer)
	}
	tracker.AddPlaybackActivityObserver(seeder)
	tracker.SetPlaybackAutoSeeder(seeder)
	return tracker
}

// appPlaybackRequest is the byte-range request an app makes: a stream path plus
// the media identity the player was launched with. This is the only playback
// signal an app produces — it never calls the progress endpoint.
func appPlaybackRequest(rangeStart int64) *http.Request {
	request := httptest.NewRequest(http.MethodGet,
		"/video/stream?profileId=profile&mediaType=movie&itemId=tmdb:movie:603"+
			"&movieName=The+Matrix&year=1999", nil)
	request.Header.Set("Range", "bytes="+strconv.FormatInt(rangeStart, 10)+"-")
	return request
}

const appStreamPath = "/debrid/torbox/55944852/file/0/The.Matrix.1999.mkv"

// waitForSeeds waits for the relay to receive want granted ingest submissions
// and then confirms no further one arrives, so "exactly once" is asserted rather
// than "at least once". The URL archive transport must stay at zero either way:
// a remote source is published by granting range access to it, never by handing
// over the address it currently resolves to.
func waitForSeeds(t *testing.T, relay *autoSeedRelay, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for relay.ingestCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("granted ingest submissions = %d, want %d", relay.ingestCount(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	if got := relay.ingestCount(); got != want {
		t.Fatalf("granted ingest submissions = %d, want %d", got, want)
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("a seed used the URL archive transport: %d calls", got)
	}
}

// One playback from an app is hundreds of byte-range requests, each of which
// opens and closes its own tracked stream, plus whatever heartbeats the player
// sends. Every one of them is a playback-start signal, and all of them together
// must produce a single seed.
func TestAutoSeedFiresOncePerPlaybackAcrossEveryPlaybackSignal(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv"}
	seeder := newAutoSeedHandler(t, relay, resolver)
	tracker := newAutoSeedTracker(seeder)

	// The app's transport: a fresh range request every few seconds, each ending
	// before the next begins, so every single one looks like a new playback to
	// the tracker and only the seeder's claim can collapse them.
	for chunk := range 200 {
		id, _, _ := tracker.StartStreamWithAccount(
			appPlaybackRequest(int64(chunk)*4194304), appStreamPath, 4194304, int64(chunk)*4194304, 0, "acct1")
		tracker.EndStream(id)
	}

	// The same playback also reaches the matched-activity fanout, which is what
	// the web player's heartbeat and the HLS keepalive feed.
	update := moviePlayback()
	update.SourcePath = appStreamPath
	for beat := range 50 {
		update.Position = float64(beat * 10)
		seeder.HandlePlaybackUpdate("profile", update, float64(beat))
	}

	// Two hundred range requests and fifty heartbeats, and exactly one seed —
	// through a grant, so the transfer no longer depends on this playback.
	waitForSeeds(t, relay, 1)
	if relay.catalogReads != 1 {
		t.Fatalf("catalog reads = %d, want 1", relay.catalogReads)
	}
	if resolver.asked != appStreamPath {
		t.Fatalf("resolver was asked for %q, want %q", resolver.asked, appStreamPath)
	}
}

// An episode played from an app has to reach the swarm under its series' TMDB
// coordinates, which the stream request carries as seriesId — and, when the
// player's own ids came from another provider, as the tmdb external id.
func TestAppEpisodePlaybackSeedsUnderItsSeriesCoordinates(t *testing.T) {
	relay := &autoSeedRelay{}
	const streamPath = "/debrid/realdebrid/98765/file/2/GoT.S01E02.mkv"
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/GoT.S01E02.mkv"}
	seeder := newAutoSeedHandler(t, relay, resolver)
	tracker := newAutoSeedTracker(seeder)

	request := httptest.NewRequest(http.MethodGet,
		"/video/stream?profileId=profile&mediaType=episode&itemId=tvdb:series:121361:s01e02"+
			"&seriesId=tvdb:series:121361&seriesName=Game+of+Thrones"+
			"&seasonNumber=1&episodeNumber=2&tmdb=1399", nil)
	tracker.StartStreamWithAccount(request, streamPath, 4194304, 0, 0, "acct1")

	waitForSeeds(t, relay, 0)
	if resolver.asked != "" {
		t.Fatalf("range-only playback resolved a contribution source: %q", resolver.asked)
	}
}

// The web player behind a transcode reports progress to the HLS keepalive rather
// than to the tracker, and that payload used to name no source at all. Both
// consumers must see it, and what they see must be seedable.
func TestHLSKeepAliveFeedsEveryObserverASeedableUpdate(t *testing.T) {
	watcher := &capturePlaybackActivityObserver{calls: make(chan models.PlaybackProgressUpdate, 2)}
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv"}
	seeder := newAutoSeedHandler(t, relay, resolver)

	manager := &HLSManager{
		sessions: map[string]*HLSSession{
			"session-1": {
				ID:                 "session-1",
				ProfileID:          "profile",
				Path:               appStreamPath,
				Duration:           8160,
				LastSegmentRequest: time.Now(),
				MediaMetadata: StreamMediaMetadata{
					MediaType: "movie",
					ItemID:    "tmdb:movie:603",
					MovieName: "The Matrix",
					Year:      1999,
				},
			},
		},
	}
	manager.AddPlaybackActivityObserver(watcher)
	manager.AddPlaybackActivityObserver(seeder)

	for beat := 1; beat <= 2; beat++ {
		request := httptest.NewRequest(http.MethodPost,
			"/keepalive?time="+strconv.Itoa(beat*10), nil)
		response := httptest.NewRecorder()
		manager.KeepAlive(response, request, "session-1")
		if response.Code != http.StatusOK {
			t.Fatalf("KeepAlive() status = %d", response.Code)
		}
	}

	select {
	case update := <-watcher.calls:
		if update.PlaybackSessionID != "hls:session-1" {
			t.Fatalf("PlaybackSessionID = %q", update.PlaybackSessionID)
		}
		if update.SourcePath != appStreamPath {
			t.Fatalf("SourcePath = %q, want %q", update.SourcePath, appStreamPath)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first observer lost its callback to the fanout")
	}
	waitForSeeds(t, relay, 0)
}

// A playback the tracker is already following must not re-announce itself on
// every overlapping range connection. This is the cheap half of the dedupe: it
// keeps the seeder from being called at all, whereas the claim keeps it from
// submitting twice.
func TestPlaybackStartIsAnnouncedOncePerConcurrentPlaybackSlot(t *testing.T) {
	starts := make(chan models.PlaybackProgressUpdate, 8)
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	tracker.SetPlaybackAutoSeeder(capturePlaybackStarts{starts})

	for chunk := range 5 {
		// Overlapping connections: nothing is ended, exactly as a native player
		// keeping several ranges open at once.
		tracker.StartStreamWithAccount(
			appPlaybackRequest(int64(chunk)*4194304), appStreamPath, 4194304, int64(chunk)*4194304, 0, "acct1")
	}

	var first models.PlaybackProgressUpdate
	select {
	case first = <-starts:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the playback start")
	}
	select {
	case extra := <-starts:
		t.Fatalf("a second range connection re-announced the playback: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	// The payload has to carry both halves of what a seed needs: the TMDB
	// coordinates the app launched with, and a stream path that can be resolved
	// again server-side.
	if first.MediaType != "movie" || first.ItemID != "tmdb:movie:603" {
		t.Fatalf("coordinates = %+v", first)
	}
	if first.MovieName != "The Matrix" || first.Year != 1999 {
		t.Fatalf("title = %q (%d)", first.MovieName, first.Year)
	}
	if first.SourcePath != appStreamPath {
		t.Fatalf("SourcePath = %q, want %q", first.SourcePath, appStreamPath)
	}
	if first.PlaybackSessionID != "direct:profile|movie:tmdb:movie:603" {
		t.Fatalf("PlaybackSessionID = %q", first.PlaybackSessionID)
	}
}

// A stream nothing identifies cannot be published, and a live channel has no
// TMDB coordinates at all. Neither may reach the seeder.
func TestPlaybackStartIsNotAnnouncedWithoutMediaIdentity(t *testing.T) {
	starts := make(chan models.PlaybackProgressUpdate, 4)
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	tracker.SetPlaybackAutoSeeder(capturePlaybackStarts{starts})

	probe := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=profile", nil)
	tracker.StartStreamWithAccount(probe, "/webdav/nzbs/unknown.mkv", 1000, 0, 0, "acct1")

	live := httptest.NewRequest(http.MethodGet, "/video/live?profileId=profile&mediaType=live&itemId=channel:1", nil)
	tracker.StartStreamWithAccount(live, "http://iptv.example/live.ts", 0, 0, 0, "acct1")

	select {
	case update := <-starts:
		t.Fatalf("an unpublishable playback reached the seeder: %+v", update)
	case <-time.After(100 * time.Millisecond):
	}
}

type capturePlaybackStarts struct {
	starts chan models.PlaybackProgressUpdate
}

func (c capturePlaybackStarts) OnPlaybackStarted(update models.PlaybackProgressUpdate) {
	c.starts <- update
}

// The notification service was the only consumer of matched playback activity.
// Adding the p2p integration must not cost it a single callback.
func TestPlaybackObserverFanoutKeepsNotificationsAndSeeding(t *testing.T) {
	events := make(chan string, 8)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		events <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	notifier := notifications.New(watchStartedRepo{url: webhook.URL})
	defer notifier.Close()

	relay := &autoSeedRelay{}
	seeder := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	tracker := newAutoSeedTracker(seeder, notifier)

	tracker.mu.Lock()
	tracker.streams["range-1"] = &TrackedStream{
		ID:           "range-1",
		ProfileID:    "profile",
		Path:         appStreamPath,
		LastActivity: time.Now(),
		MediaMetadata: StreamMediaMetadata{
			MediaType: "movie",
			ItemID:    "tmdb:movie:603",
			MovieName: "The Matrix",
			Year:      1999,
		},
	}
	tracker.mu.Unlock()

	// A heartbeat carrying no source path of its own: the matched stream names
	// one, which is what makes this payload seedable.
	matched := tracker.ObservePlaybackActivity("profile", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:movie:603",
		Duration:  8160,
		Position:  12,
	}, 1)
	if matched != 1 {
		t.Fatalf("ObservePlaybackActivity() matched %d streams, want 1", matched)
	}

	select {
	case event := <-events:
		if event != models.NotificationEventWatchStarted {
			t.Fatalf("notification event = %q, want %q", event, models.NotificationEventWatchStarted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notification service lost its callback to the fanout")
	}
	waitForSeeds(t, relay, 0)
}

// The viewer and the notification service are the point; the swarm is a bonus. A
// relay that hangs and then refuses must cost neither of them anything.
func TestSeedFailureDisturbsNeitherPlaybackNorNotifications(t *testing.T) {
	events := make(chan string, 8)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		events <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	notifier := notifications.New(watchStartedRepo{url: webhook.URL})
	defer notifier.Close()

	relay := &autoSeedRelay{
		archiveStatus: http.StatusInternalServerError,
		archiveDelay:  2 * time.Second,
	}
	seeder := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	tracker := newAutoSeedTracker(seeder, notifier)

	started := time.Now()
	id, _, _ := tracker.StartStreamWithAccount(appPlaybackRequest(0), appStreamPath, 4194304, 0, 0, "acct1")
	if id == "" {
		t.Fatal("the stream was not tracked")
	}
	matched := tracker.ObservePlaybackActivity("profile", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:movie:603",
		Duration:  8160,
		Position:  12,
	}, 1)
	elapsed := time.Since(started)

	if matched != 1 {
		t.Fatalf("ObservePlaybackActivity() matched %d streams, want 1", matched)
	}
	if elapsed >= relay.archiveDelay {
		t.Fatalf("playback waited %v on the relay", elapsed)
	}
	select {
	case event := <-events:
		if event != models.NotificationEventWatchStarted {
			t.Fatalf("notification event = %q, want %q", event, models.NotificationEventWatchStarted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failing seed cost the notification service its callback")
	}

	// A remote source is skipped before any legacy archive submission, and the
	// outcome never leaves the detached observer.
	waitForSeeds(t, relay, 0)
}

// watchStartedRepo is the smallest notification store that produces a watch
// event: one enabled webhook channel, and nothing persisted.
type watchStartedRepo struct{ url string }

func (r watchStartedRepo) ListChannels(context.Context, string) ([]models.NotificationChannel, error) {
	return []models.NotificationChannel{{
		ID:        "channel",
		ProfileID: "profile",
		Type:      models.NotificationChannelWebhook,
		URL:       r.url,
		Enabled:   true,
		Events:    []string{models.NotificationEventWatchStarted},
	}}, nil
}

// The repository grew a profile-agnostic listing; this fixture owns exactly one
func TestQualifiedPlaybackAbandonCancelsOnlyItsActiveAcquisitionOnce(t *testing.T) {
	relay := &autoSeedRelay{}
	client := relay.client(t)
	sourceID := autoSeedSourceID(appStreamPath)
	start := time.Unix(1_700_000_000, 0)
	observer := peartube.NewPlaybackObserver(peartube.PlaybackObserverConfig{
		MeaningfulWatchDuration: 10 * time.Second,
		MeaningfulWatchFraction: 0.01,
	})
	observer.Observe(peartube.PlaybackEvent{
		PlaybackID: "session-1",
		SourceID:   sourceID,
		Position:   0,
		Duration:   100 * time.Second,
		ObservedAt: start,
	})
	qualified := observer.Observe(peartube.PlaybackEvent{
		PlaybackID: "session-1",
		SourceID:   sourceID,
		Position:   10 * time.Second,
		Duration:   100 * time.Second,
		ObservedAt: start.Add(10 * time.Second),
	})
	if !qualified.FirstQualified {
		t.Fatal("test setup did not qualify the playback")
	}
	cancels := 0
	handler := &PearTubeHandler{
		relay:                  client,
		contributeWatchedMedia: true,
		playbackObserver:       observer,
		playbackNow:            func() time.Time { return start.Add(11 * time.Second) },
		activeAcquisitions: map[string]*autoSeedAcquisition{
			"session-1": {cancel: func() { cancels++ }, relay: client},
		},
	}
	update := moviePlayback()
	update.PlaybackSessionID = "session-1"
	update.SourcePath = appStreamPath
	update.PlaybackEnded = true
	if state := handler.observePlayback("profile", update); state != peartube.PlaybackCancelled {
		t.Fatalf("abandon state = %q, want cancelled", state)
	}
	handler.observePlayback("profile", update)
	if cancels != 1 {
		t.Fatalf("active acquisition cancelled %d times, want 1", cancels)
	}
}

func TestAutoSeedJobStatesTransitionOnceAndRetireActiveCapacity(t *testing.T) {
	handler := &PearTubeHandler{
		activeAcquisitions:  make(map[string]*autoSeedAcquisition),
		autoSeedJobsByState: make(map[string]int),
	}

	cancelCalls := 0
	accepted := &autoSeedAcquisition{
		cancel:    func() { cancelCalls++ },
		state:     "queued",
		createdAt: time.Now(),
	}
	handler.activeAcquisitions["accepted"] = accepted
	handler.autoSeedJobsByState["queued"] = 1
	handler.playbackMu.Lock()
	handler.finishAutoSeedSubmissionLocked("accepted", accepted, &peartube.ArchiveJob{
		JobID:  "ing_accepted",
		Status: "acquiring",
	}, nil)
	handler.playbackMu.Unlock()
	if got := pearTubeStatusBody(t, handler).JobsByState; len(got) != 1 || got["acquiring"] != 1 {
		t.Fatalf("accepted transition counted multiple states: %v", got)
	}
	handler.cancelAutoSeedAcquisition("accepted")
	handler.cancelAutoSeedAcquisition("accepted")
	if got := pearTubeStatusBody(t, handler).JobsByState; len(got) != 1 || got["cancelled"] != 1 {
		t.Fatalf("cancel transition counted multiple states: %v", got)
	}
	if cancelCalls != 1 || len(handler.activeAcquisitions) != 0 {
		t.Fatalf("cancelled acquisition retained capacity: cancels=%d active=%d", cancelCalls, len(handler.activeAcquisitions))
	}

	completed := &autoSeedAcquisition{
		cancel:    func() {},
		state:     "queued",
		createdAt: time.Now(),
	}
	handler.playbackMu.Lock()
	handler.activeAcquisitions["completed"] = completed
	handler.bumpAutoSeedJobStateLocked("queued", 1)
	handler.finishAutoSeedSubmissionLocked("completed", completed, &peartube.ArchiveJob{
		JobID:  "ing_completed",
		Status: "completed",
	}, nil)
	handler.playbackMu.Unlock()
	if got := pearTubeStatusBody(t, handler).JobsByState; len(got) != 2 ||
		got["cancelled"] != 1 || got["completed"] != 1 {
		t.Fatalf("terminal transition counted multiple states: %v", got)
	}
	if len(handler.activeAcquisitions) != 0 {
		t.Fatal("terminal returned state retained active capacity")
	}

	expiryCancels := 0
	expired := &autoSeedAcquisition{
		cancel:    func() { expiryCancels++ },
		state:     "acquiring",
		createdAt: time.Now().Add(-peartube.DefaultPlaybackObservationTTL),
	}
	handler.playbackMu.Lock()
	handler.activeAcquisitions["expired"] = expired
	handler.bumpAutoSeedJobStateLocked("acquiring", 1)
	handler.expireAutoSeedAcquisitionsLocked(time.Now())
	handler.playbackMu.Unlock()
	if got := pearTubeStatusBody(t, handler).JobsByState; len(got) != 2 ||
		got["cancelled"] != 1 || got["completed"] != 1 {
		t.Fatalf("expired acquisition state was not retired: %v", got)
	}
	if expiryCancels != 1 || len(handler.activeAcquisitions) != 0 {
		t.Fatalf("expired acquisition retained capacity: cancels=%d active=%d", expiryCancels, len(handler.activeAcquisitions))
	}
}

// An archive the relay accepted must survive the age sweep. Age says how long a
// PLAYBACK is interesting; it says nothing about a download the relay drives on
// its own clock. A relay-side cancel is terminal - it reclaims the staged blocks
// - so expiring an accepted job by age threw away hours of good work, and on a
// debrid link measured as low as 0.38 MB/s a large title could never finish
// inside ANY window worth holding a viewer's observation open for. Raising the
// window only moves that cliff; the accepted job has to be skipped outright.
func TestAgedArchiveKeepsRunningWhileItsTrackingSlotIsFreed(t *testing.T) {
	handler := &PearTubeHandler{activeAcquisitions: map[string]*autoSeedAcquisition{}}

	cancels := 0
	handler.playbackMu.Lock()
	handler.activeAcquisitions["accepted"] = &autoSeedAcquisition{
		cancel:    func() { cancels++ },
		state:     "acquiring",
		jobID:     "arch_accepted",
		createdAt: time.Now().Add(-100 * peartube.DefaultPlaybackObservationTTL),
	}
	jobs := handler.expireAutoSeedAcquisitionsLocked(time.Now())
	handler.playbackMu.Unlock()

	if len(jobs) != 0 {
		t.Fatalf("an accepted archive was queued for cancellation by age: %d job(s)", len(jobs))
	}
	if cancels != 0 {
		t.Fatalf("an accepted archive had its context cancelled by age: cancels=%d", cancels)
	}
	// The slot is still freed, so the active cap cannot be starved by long work.
	if len(handler.activeAcquisitions) != 0 {
		t.Fatalf("aged acquisition kept its tracking slot: active=%d", len(handler.activeAcquisitions))
	}

	// An acquisition the relay never accepted has nothing worth preserving, so
	// it is still cancelled outright.
	unacceptedCancels := 0
	handler.playbackMu.Lock()
	handler.activeAcquisitions["never-submitted"] = &autoSeedAcquisition{
		cancel:    func() { unacceptedCancels++ },
		state:     "acquiring",
		createdAt: time.Now().Add(-peartube.DefaultPlaybackObservationTTL),
	}
	handler.expireAutoSeedAcquisitionsLocked(time.Now())
	handler.playbackMu.Unlock()
	if unacceptedCancels != 1 || len(handler.activeAcquisitions) != 0 {
		t.Fatalf("an unsubmitted acquisition was not cancelled: cancels=%d active=%d", unacceptedCancels, len(handler.activeAcquisitions))
	}
}

// channel, so both listings return it.
func (r watchStartedRepo) ListAllChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	return r.ListChannels(ctx, "profile")
}

func (watchStartedRepo) GetChannel(context.Context, string) (*models.NotificationChannel, error) {
	return nil, nil
}
func (watchStartedRepo) CreateChannel(context.Context, *models.NotificationChannel) error { return nil }
func (watchStartedRepo) UpdateChannel(context.Context, *models.NotificationChannel) error { return nil }
func (watchStartedRepo) DeleteChannel(context.Context, string, string) error              { return nil }
func (watchStartedRepo) GetObservation(context.Context, string, string) (*models.NotificationObservation, error) {
	return nil, nil
}
func (watchStartedRepo) ListObservations(context.Context, string) ([]models.NotificationObservation, error) {
	return nil, nil
}
func (watchStartedRepo) ListAllObservations(context.Context) ([]models.NotificationObservation, error) {
	return nil, nil
}
func (watchStartedRepo) UpsertObservation(context.Context, *models.NotificationObservation) error {
	return nil
}
func (watchStartedRepo) GetProgressMessage(context.Context, string, string) (*models.NotificationProgressMessage, error) {
	return nil, nil
}
func (watchStartedRepo) ListProgressMessages(context.Context) ([]models.NotificationProgressMessage, error) {
	return nil, nil
}
func (watchStartedRepo) ListProgressMessagesByPlayback(context.Context, string, string) ([]models.NotificationProgressMessage, error) {
	return nil, nil
}
func (watchStartedRepo) UpsertProgressMessage(context.Context, *models.NotificationProgressMessage) error {
	return nil
}
func (watchStartedRepo) TouchProgressMessages(context.Context, string, string, time.Time) error {
	return nil
}
func (watchStartedRepo) DeleteProgressMessage(context.Context, string, string) error { return nil }

var _ datastore.NotificationRepository = watchStartedRepo{}

func TestFallbackPlaybackRestartsAfterEarlyAbandonWithoutSourceDedup(t *testing.T) {
	relay := &autoSeedRelay{}
	times := []time.Time{
		time.Unix(1_700_000_000, 0),
		time.Unix(1_700_000_005, 0),
		time.Unix(1_700_000_010, 0),
		time.Unix(1_700_000_020, 0),
		time.Unix(1_700_000_030, 0),
		time.Unix(1_700_000_040, 0),
	}
	index := 0
	handler := &PearTubeHandler{
		relay:                  relay.client(t),
		contributeWatchedMedia: true,
		streams:                &fakeStreamResolver{url: "https://private-playback.invalid/movie.mkv"},
		playbackNow: func() time.Time {
			value := times[index]
			index++
			return value
		},
	}
	update := moviePlayback()
	update.PlaybackSessionID = ""
	update.Position = 0
	update.PlaybackEnded = false
	if state := handler.observePlayback("profile-1", update); state != peartube.PlaybackUnqualified {
		t.Fatalf("initial state = %q", state)
	}
	update.Position = 5
	update.PlaybackEnded = true
	if state := handler.observePlayback("profile-1", update); state != peartube.PlaybackCancelled {
		t.Fatalf("early abandon state = %q", state)
	}

	update.PlaybackEnded = false
	for _, position := range []float64{0, 10, 20, 30} {
		update.Position = position
		state := handler.observePlayback("profile-1", update)
		if position < 30 && state != peartube.PlaybackUnqualified {
			t.Fatalf("restart position %.0f state = %q", position, state)
		}
		if position == 30 && state != peartube.PlaybackQualified {
			t.Fatalf("restart never qualified: %q", state)
		}
	}
}

// startArchivePlayback is one title being started, named the way the stream
// tracker names a transport start.
func startArchivePlayback() models.PlaybackProgressUpdate {
	update := moviePlayback()
	update.SourcePath = appStreamPath
	update.PlaybackSessionID = "direct:profile|movie|tmdb:movie:603"
	// A start carries no watch evidence at all: position zero, nothing ended.
	update.Position = 0
	update.PlaybackEnded = false
	return update
}

// waitForRelayCancellations waits for want cancellations and then confirms no
// further one arrives, so "never cancelled" is asserted rather than "not
// cancelled yet".
func waitForRelayCancellations(t *testing.T, relay *autoSeedRelay, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for relay.cancelledCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("relay ingest cancellations = %d, want %d", relay.cancelledCount(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	if got := relay.cancelledCount(); got != want {
		t.Fatalf("relay ingest cancellations = %d, want %d", got, want)
	}
}

// The operator's ask: clicking play archives the whole piece of content. One
// start, no progress observations at all, one whole-title submission — and
// starting the same title again, in the same session or a new one, adds nothing.
func TestArchiveOnPlaybackStartArchivesTheWholeTitleFromOneStart(t *testing.T) {
	t.Setenv(peartube.CompanionClientEnv, "mediastorm-test")
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{
		url:     "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv",
		content: []byte(strings.Repeat("matrix", 512)),
	}
	handler := newStartArchiveHandler(t, relay, resolver)

	update := startArchivePlayback()
	if state := handler.observePlayback("", update); state != peartube.PlaybackQualified {
		t.Fatalf("start state = %q, want qualified without any watch evidence", state)
	}
	waitForSeeds(t, relay, 1)

	// The whole title, not the range the player happens to be reading: the
	// submission declares the file's full length and the only upstream read
	// this process made was the one-byte probe that learned it.
	submission := relay.lastIngest()
	descriptor, _ := submission["sourceDescriptor"].(map[string]any)
	if descriptor["provider"] != "torbox" || descriptor["torrentId"] != float64(55944852) {
		t.Fatalf("direct descriptor = %v, want torbox 55944852", descriptor)
	}
	request, _ := submission["request"].(map[string]any)
	if request["retentionClass"] != "contribution-cache" {
		t.Fatalf("retention class = %v, want the consented contribution budget", request["retentionClass"])
	}

	// Idempotency, both carriers. Restarting the same session and a second
	// viewer on the same stream are stopped by the observer's per-source
	// ledger; a second viewer whose provider handed them a different stream for
	// the same title reaches the title claim, which is what stops it.
	handler.observePlayback("", update)
	sameSource := startArchivePlayback()
	sameSource.PlaybackSessionID = "direct:other-profile|movie|tmdb:movie:603"
	handler.observePlayback("", sameSource)
	otherSource := startArchivePlayback()
	otherSource.PlaybackSessionID = "direct:third-profile|movie|tmdb:movie:603"
	otherSource.SourcePath = "/debrid/realdebrid/70001/file/3/The.Matrix.1999.mkv"
	if state := handler.observePlayback("", otherSource); state != peartube.PlaybackQualified {
		t.Fatalf("second source state = %q, want qualified and then claimed away", state)
	}
	waitForSeeds(t, relay, 1)
}

// The point of the change: the archive is a separate task, so the viewer walking
// away must not throw away the bytes the relay has already confirmed.
func TestArchiveOnPlaybackStartSurvivesAbandonedPlayback(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv"}
	handler := newStartArchiveHandler(t, relay, resolver)

	update := startArchivePlayback()
	handler.OnPlaybackStarted(update)
	waitForSeeds(t, relay, 1)

	ended := update
	ended.PlaybackEnded = true
	ended.Position = 4
	handler.observePlayback("", ended)
	// A repeat, because an app reports the end more than once.
	handler.observePlayback("", ended)

	waitForRelayCancellations(t, relay, 0)
	handler.playbackMu.Lock()
	acquisition := handler.activeAcquisitions[update.PlaybackSessionID]
	handler.playbackMu.Unlock()
	if acquisition == nil {
		t.Fatal("the abandoned playback retired its archive")
	}
	if acquisition.cancelled {
		t.Fatal("the abandoned playback cancelled its archive")
	}
}

// Consent is still the whole gate. Withdrawing it cancels the archive at the
// relay and revokes the source grants it was pulling through, whatever the
// start-archive setting says.
func TestArchiveOnPlaybackStartIsCancelledByConsentWithdrawal(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv"}
	handler := newStartArchiveHandler(t, relay, resolver)

	handler.OnPlaybackStarted(startArchivePlayback())
	waitForSeeds(t, relay, 1)
	epochBeforeWithdrawal := handler.sourceGrants.PolicyEpoch()

	// An empty policy is the withdrawal an operator performs by turning
	// contribution off: no relay, no consent, migration required.
	if err := handler.ApplyPearTubeSettings(config.PearTubeSettings{}); err != nil {
		t.Fatalf("withdraw consent: %v", err)
	}

	waitForRelayCancellations(t, relay, 1)
	if handler.sourceGrants.PolicyEpoch() <= epochBeforeWithdrawal {
		t.Fatalf("source grants survived the withdrawal: epoch %d -> %d",
			epochBeforeWithdrawal, handler.sourceGrants.PolicyEpoch())
	}
	handler.playbackMu.Lock()
	remaining := len(handler.activeAcquisitions)
	handler.playbackMu.Unlock()
	if remaining != 0 {
		t.Fatalf("withdrawn consent left %d active acquisitions", remaining)
	}
}

// With the option off nothing about the old contract moves: a start alone is
// zero evidence and contributes nothing, sustained continuous progress is what
// qualifies, and an abandoned playback still takes its archive with it.
func TestWithoutArchiveOnPlaybackStartTheEvidencePathIsUnchanged(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	update := startArchivePlayback()
	if state := handler.observePlayback("", update); state != peartube.PlaybackUnqualified {
		t.Fatalf("start state = %q, want unqualified", state)
	}
	waitForSeeds(t, relay, 0)
	if resolver.asked != "" {
		t.Fatalf("a zero-evidence start resolved a contribution source: %q", resolver.asked)
	}

	// The clock advances ten seconds per observation, so four continuous beats
	// clear the thirty seconds of evidence this playback needs.
	for _, position := range []float64{10, 20, 30, 40} {
		update.Position = position
		handler.observePlayback("", update)
	}
	waitForSeeds(t, relay, 1)

	update.PlaybackEnded = true
	handler.observePlayback("", update)
	waitForRelayCancellations(t, relay, 1)
}
