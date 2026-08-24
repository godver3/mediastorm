package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
	"novastream/services/peartube"
)

// autoSeedRelay is a relay that serves a catalog and counts seed submissions,
// which is exactly what an automatic seed exercises: read the catalog, then
// submit only what the swarm is missing.
type autoSeedRelay struct {
	// catalog is the entities array served at GET /api/v1/catalog. An empty
	// string means the relay carries nothing.
	catalog string
	// catalogStatus and catalogBody override the catalog answer, so a gated or
	// broken relay can be simulated.
	catalogStatus int
	catalogBody   string
	// archiveStatus overrides the seed answer, so a refusal can be simulated. It
	// applies to both seed transports: the legacy URL archive and the granted
	// ingest job that replaced it for remote sources.
	archiveStatus int
	// archiveDelay holds the seed submission open, so a caller that waits on it
	// is visible as a caller that waits.
	archiveDelay time.Duration

	mu           sync.Mutex
	catalogReads int
	archives     []map[string]any
	ingests      []map[string]any
	// cancelledJobs are the ingest jobs the relay was asked to cancel. An
	// archive that outlives its playback is only proved by nothing arriving
	// here, so cancellation has to be observable.
	cancelledJobs []string
}

func (relay *autoSeedRelay) archiveCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.archives)
}

func (relay *autoSeedRelay) ingestCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.ingests)
}

func (relay *autoSeedRelay) cancelledCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.cancelledJobs)
}

func (relay *autoSeedRelay) lastArchive() map[string]any {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.archives) == 0 {
		return nil
	}
	return relay.archives[len(relay.archives)-1]
}

func (relay *autoSeedRelay) lastIngest() map[string]any {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.ingests) == 0 {
		return nil
	}
	return relay.ingests[len(relay.ingests)-1]
}

func (relay *autoSeedRelay) client(t *testing.T) *peartube.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Every seed re-reconciles the versioned policy before it sends
		// anything, so a relay that cannot answer this refuses every seed.
		case r.URL.Path == "/api/v2/policy":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"policy":{"policyVersion":2}}`))
			return

		case strings.HasPrefix(r.URL.Path, "/api/v1/catalog"):
			relay.mu.Lock()
			relay.catalogReads++
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if relay.catalogStatus != 0 {
				w.WriteHeader(relay.catalogStatus)
				_, _ = w.Write([]byte(relay.catalogBody))
				return
			}
			_, _ = w.Write([]byte(`{"entities":[` + relay.catalog + `],"nextCursor":null}`))

		case strings.HasPrefix(r.URL.Path, "/api/v1/archive"):
			if relay.archiveDelay > 0 {
				time.Sleep(relay.archiveDelay)
			}
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			relay.mu.Lock()
			relay.archives = append(relay.archives, body)
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if relay.archiveStatus != 0 {
				w.WriteHeader(relay.archiveStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"relay exploded","field":null}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"jobId":"arch_1","status":"queued","entityHint":"movie:603"}`))

		// Remote sources now arrive as granted ingest jobs, so a refusal has to
		// be simulatable on this transport too.
		case r.URL.Path == "/api/v2/ingest/jobs":
			if relay.archiveDelay > 0 {
				time.Sleep(relay.archiveDelay)
			}
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			relay.mu.Lock()
			relay.ingests = append(relay.ingests, body)
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if relay.archiveStatus != 0 {
				w.WriteHeader(relay.archiveStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"relay exploded","field":null}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job":{"jobId":"` + r.Header.Get("X-PearTube-Job-ID") + `","state":"queued"}}`))

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/ingest/jobs/"):
			relay.mu.Lock()
			relay.cancelledJobs = append(relay.cancelledJobs,
				strings.TrimPrefix(r.URL.Path, "/api/v2/ingest/jobs/"))
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"job":{"jobId":"cancelled","state":"cancelled"}}`))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}
	return client
}

// newAutoSeedHandler builds the handler as main.go wires it: the switch on, a
// stream resolver standing in for the composite streaming provider, and a live
// source-grant registry, because both seed transports are now granted sources.
//
// resolved carries the versioned consent the seed closure re-checks before it
// sends anything. Without it every submission fails closed, which would make a
// test that asserts "nothing was submitted" pass for the wrong reason.
//
// archiveOnPlaybackStart is off, which pins the evidence path: a start alone
// contributes nothing, sustained progress qualifies, and abandonment cancels.
// Tests of the start-archive behaviour use newStartArchiveHandler instead.
func newAutoSeedHandler(t *testing.T, relay *autoSeedRelay, resolver *fakeStreamResolver) *PearTubeHandler {
	t.Helper()
	// Set before the client is built: both the companion client and the callback
	// registry read this identity once, at construction.
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	sourceGrants := peartube.NewSourceGrantRegistryFromEnv()
	t.Cleanup(sourceGrants.Close)
	var clockMu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	return &PearTubeHandler{
		relay:                  relay.client(t),
		streams:                resolver,
		sourceGrants:           sourceGrants,
		contributeWatchedMedia: true,
		archiveOnPlaybackStart: false,
		resolved: peartube.Resolved{
			ConsentVersion:         config.PearTubeConsentVersion,
			ContributeWatchedMedia: true,
			ContributionBudget:     1,
		},
		playbackNow: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			now = now.Add(10 * time.Second)
			return now
		},
	}
}

// newStartArchiveHandler is the same install with the operator's choice made:
// consented contribution that archives the whole title as soon as playback
// starts. Only the timing setting differs, so a behaviour difference between
// this and newAutoSeedHandler is attributable to that setting alone.
func newStartArchiveHandler(t *testing.T, relay *autoSeedRelay, resolver *fakeStreamResolver) *PearTubeHandler {
	t.Helper()
	handler := newAutoSeedHandler(t, relay, resolver)
	handler.archiveOnPlaybackStart = true
	handler.resolved.ArchiveOnPlaybackStart = true
	return handler
}

func moviePlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tmdb:movie:603",
		MovieName:  "The Matrix",
		Year:       1999,
		SourcePath: "/debrid/torbox/12345/file/9/The.Matrix.1999.mkv",
		Position:   12,
		Duration:   8160,
	}
}

func episodePlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:1399:s01e02",
		SeriesID:      "tmdb:tv:1399",
		SeriesName:    "Game of Thrones",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		SourcePath:    "/debrid/realdebrid/98765/file/2/GoT.S01E02.mkv",
	}
}

const matrixCatalogEntity = `{"entityId":"tmdb:movie:603","entityKind":"movie","title":"The Matrix","year":1999,` +
	`"sources":[{"publicationId":"pub-matrix","publisherId":"abcdef0123456789","renditionId":"rend-1","byteLength":4096}]}`

// With no relay, or with the switch off, a playback heartbeat must not produce a
// single call — that is what keeps an install which never asked for p2p, or one
// that asked for the relay but not for automatic seeding, behaving as before.
func TestAutoSeedIsInertWhenDisabled(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}

	off := &PearTubeHandler{relay: relay.client(t), streams: resolver, contributeWatchedMedia: false}
	if _, ok := off.planAutoSeed(moviePlayback()); ok {
		t.Fatal("autoseed planned a seed with the switch off")
	}
	off.OnPlaybackStarted(moviePlayback())

	unconfigured := &PearTubeHandler{contributeWatchedMedia: true}
	if _, ok := unconfigured.planAutoSeed(moviePlayback()); ok {
		t.Fatal("autoseed planned a seed without a relay")
	}
	unconfigured.OnPlaybackStarted(moviePlayback())

	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved: %q", resolver.asked)
	}
}

// A playback sends a heartbeat every few seconds and a player opens hundreds of
// byte-range requests, none of which reach this handler. Every heartbeat of one
// playback must collapse to a single submission.
//
// A debrid stream is now published by granting the relay range access, not by
// handing over the address it resolved to, so this also pins the shape of that
// submission: an ingest job carrying an opaque capability, the authoritative
// byte length, and a byte-identity ETag — and no address anywhere. The address
// is likewise absent from the idempotency key, so the same title resolved again
// is recognised as the same seed.
func TestAutoSeedSubmitsOncePerTitleAcrossManyHeartbeats(t *testing.T) {
	t.Setenv(peartube.CompanionClientEnv, "mediastorm-test")
	relay := &autoSeedRelay{}
	const resolvedAddress = "https://cdn.example.net/d/FRESH-TOKEN/The.Matrix.1999.mkv"
	resolver := &fakeStreamResolver{url: resolvedAddress, content: []byte(strings.Repeat("matrix", 512))}
	handler := newAutoSeedHandler(t, relay, resolver)

	update := moviePlayback()
	for beat := range 50 {
		update.Position = float64(beat * 10)
		plan, ok := handler.planAutoSeed(update)
		if !ok {
			continue
		}
		if plan.key != "movie:603" {
			t.Fatalf("claim key = %q", plan.key)
		}
		// The player's own URL is never forwarded: the seed names the stream
		// path and the seed path grants access to it.
		if plan.request.SourceURL != "" || plan.request.StreamPath != update.SourcePath {
			t.Fatalf("seed request = %+v", plan.request)
		}
		plan.submit()
	}

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("a remote source still used the URL archive transport: %d calls", got)
	}
	if got := relay.ingestCount(); got != 1 {
		t.Fatalf("granted ingest submissions = %d, want 1", got)
	}
	submission := relay.lastIngest()
	capability, _ := submission["sourceCapability"].(string)
	if len(capability) != 43 {
		t.Fatalf("submission capability = %q, want an opaque 43-character token", capability)
	}
	request, _ := submission["request"].(map[string]any)
	expected, _ := request["expected"].(map[string]any)
	if expected["byteLength"] != float64(len(resolver.body())) {
		t.Fatalf("declared byte length = %v, want the probed total %d", expected["byteLength"], len(resolver.body()))
	}
	if expected["etag"] != peartube.RemoteSourceETag(update.SourcePath, int64(len(resolver.body()))) {
		t.Fatalf("declared etag = %v, want the stream path's byte identity", expected["etag"])
	}
	// The relay's canonical form always carries sha256, null when the source
	// could not state one, and the job id is hashed over that form - so omitting
	// the key made every granted archive come back as a mismatched job. What must
	// never happen is a remote source CLAIMING a digest it cannot have computed.
	digest, present := expected["sha256"]
	if !present {
		t.Fatal("sha256 was omitted; the relay hashes it as null and the job ids will diverge")
	}
	if digest != nil {
		t.Fatalf("a remote source claimed a whole-file digest it cannot have computed: %v", digest)
	}
	if request["retentionClass"] != "contribution-cache" {
		t.Fatalf("retention class = %v, want the consented contribution budget", request["retentionClass"])
	}
	// The address the player would have used must not appear anywhere in the
	// submission, in any field.
	encoded, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cdn.example.net") || strings.Contains(string(encoded), "FRESH-TOKEN") {
		t.Fatalf("submission leaked the resolved address: %s", encoded)
	}
	if strings.Contains(string(encoded), update.SourcePath) {
		t.Fatalf("submission leaked the internal stream path: %s", encoded)
	}
	if resolver.asked != update.SourcePath {
		t.Fatalf("resolver was asked for %q, want %q", resolver.asked, update.SourcePath)
	}
	// One probe, and nothing more: the relay pulls the body on its own clock.
	if len(resolver.ranges) != 1 || resolver.ranges[0] != "bytes=0-0" {
		t.Fatalf("upstream reads during planning = %v, want one length probe", resolver.ranges)
	}
}

// The local-library path is untouched by the remote change: a stream path that
// resolves to a file this process holds is still published from that open file,
// with the whole-file digest a local source can actually state.
func TestAutoSeedOfALocalStreamPathStillPublishesFromTheFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "The.Matrix.1999.mkv")
	if err := os.WriteFile(path, []byte(strings.Repeat("on-disk", 128)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: path}
	handler := newAutoSeedHandler(t, relay, resolver)
	handler.localMedia = fakeLibrary{libraries: []models.LocalMediaLibrary{{RootPath: root}}}

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("a locally held movie was not seedable")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("the local path used the URL archive transport: %d calls", got)
	}
	if got := relay.ingestCount(); got != 1 {
		t.Fatalf("local granted ingest submissions = %d, want 1", got)
	}
	submission := relay.lastIngest()
	request, _ := submission["request"].(map[string]any)
	expected, _ := request["expected"].(map[string]any)
	digest, _ := expected["sha256"].(string)
	if len(digest) != 64 {
		t.Fatalf("local source digest = %q, want a whole-file SHA-256", digest)
	}
	etag, _ := expected["etag"].(string)
	if etag != `"sha256-`+digest+`"` {
		t.Fatalf("local source etag = %q, want the file's content hash", etag)
	}
	// The local file was never read through the streaming provider.
	if len(resolver.ranges) != 0 {
		t.Fatalf("local seed pulled ranges from the streaming layer: %v", resolver.ranges)
	}
	encoded, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), path) {
		t.Fatalf("submission exposed the local source path: %s", encoded)
	}
}

func TestAutoSeedPublishesAnEpisodeUnderItsSeriesCoordinates(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv"})

	plan, ok := handler.planAutoSeed(episodePlayback())
	if !ok {
		t.Fatal("an episode playback was not seedable")
	}
	if plan.key != "show:1399:s1:e2" {
		t.Fatalf("claim key = %q", plan.key)
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("remote episode entered legacy archive: %d calls", got)
	}
}

// The relay already serves this title, so asking it to fetch the whole file
// again is pure waste.
func TestAutoSeedSkipsATitleTheSwarmAlreadyServes(t *testing.T) {
	relay := &autoSeedRelay{catalog: matrixCatalogEntity}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the plan was refused before the catalog was consulted")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("archive submissions = %d, want 0", got)
	}
	if relay.catalogReads != 1 {
		t.Fatalf("catalog reads = %d, want 1", relay.catalogReads)
	}
	// Nothing was re-resolved either: a debrid resolve can wake a torrent, and
	// there is no reason to pay for that when the swarm already has the title.
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved anyway: %q", resolver.asked)
	}
}

// Two viewers starting the same title at the same moment must produce one seed.
// Only the in-process claim can enforce that: both would read a catalog that
// does not list the title yet.
func TestAutoSeedFiresOnceForConcurrentPlays(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})

	const plays = 8
	var (
		wait   sync.WaitGroup
		start  = make(chan struct{})
		claims = make(chan autoSeedPlan, plays)
	)
	for range plays {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if plan, ok := handler.planAutoSeed(moviePlayback()); ok {
				claims <- plan
			}
		}()
	}
	close(start)
	wait.Wait()
	close(claims)

	claimed := 0
	for plan := range claims {
		claimed++
		plan.submit()
	}
	if claimed != 1 {
		t.Fatalf("%d of %d concurrent plays claimed the title, want 1", claimed, plays)
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("remote concurrent plays entered legacy archive: %d calls", got)
	}
}

// A relay that will not enumerate cannot say whether the swarm already has the
// title. Seeding anyway would mean re-fetching whole files on every catalog
// hiccup, so the attempt is abandoned — but the claim is released, because
// nothing was submitted and a later playback should ask again.
func TestAutoSeedDoesNotSeedWhenTheCatalogIsUnavailable(t *testing.T) {
	relay := &autoSeedRelay{
		catalogStatus: http.StatusForbidden,
		catalogBody:   `{"error":{"code":"OPEN_ACCESS_NOT_ENABLED","message":"not open","field":null}}`,
	}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the plan was refused before the catalog was consulted")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("archive submissions = %d, want 0", got)
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved anyway: %q", resolver.asked)
	}
	if _, ok := handler.planAutoSeed(moviePlayback()); !ok {
		t.Fatal("the claim was held after nothing was submitted")
	}
}

// A source the relay refuses keeps its claim: it would be refused again on the
// next heartbeat, and one log line per guard window is the right amount of noise.
func TestAutoSeedKeepsItsClaimAfterARefusedSeed(t *testing.T) {
	relay := &autoSeedRelay{archiveStatus: http.StatusInternalServerError}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the movie was not seedable")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("remote source entered legacy archive: %d calls", got)
	}
	if _, ok := handler.planAutoSeed(moviePlayback()); ok {
		t.Fatal("a refused seed was retried on the next heartbeat")
	}
}

// Playback with no source to re-resolve, no title, no usable coordinates, or no
// identity at all cannot be published and must not be attempted. A playback
// missing only the TMDB number is not in this list: that is the ordinary app
// case, and it is recovered rather than dropped.
func TestAutoSeedSkipsPlaybackItCannotPublish(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"})

	withoutSource := moviePlayback()
	withoutSource.SourcePath = ""

	unidentified := moviePlayback()
	unidentified.ItemID = ""

	untitled := moviePlayback()
	untitled.MovieName = ""

	partialEpisode := episodePlayback()
	partialEpisode.SeasonNumber = 0

	live := models.PlaybackProgressUpdate{
		MediaType:  "live",
		ItemID:     "live:channel-4",
		SourcePath: "/live/channel-4/stream.ts",
	}

	for name, update := range map[string]models.PlaybackProgressUpdate{
		"no source path":            withoutSource,
		"nothing to identify it by": unidentified,
		"no title":                  untitled,
		"no season number":          partialEpisode,
		"live tv":                   live,
		"no coordinates":            {MediaType: "movie", SourcePath: "/debrid/x/1/file/1"},
		"unknown media type":        {MediaType: "photo", ItemID: "tmdb:movie:603", MovieName: "x", SourcePath: "/debrid/x/1/file/1"},
	} {
		if _, ok := handler.planAutoSeed(update); ok {
			t.Fatalf("%s: planned a seed", name)
		}
	}
	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
}

// An episode whose player reported only external ids still publishes: the `tmdb`
// entry of an episode update is its series id, which is the coordinate the swarm
// keys an episode by.
func TestAutoSeedTakesTheTMDBIDFromExternalIDs(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv"})

	update := episodePlayback()
	update.ItemID = "tvdb:series:121361:s01e02"
	update.SeriesID = "tvdb:series:121361"
	update.ExternalIDs = map[string]string{"TMDB": "1399", "tvdb": "121361"}

	plan, ok := handler.planAutoSeed(update)
	if !ok {
		t.Fatal("an episode identified by external ids was not seedable")
	}
	if plan.key != "show:1399:s1:e2" {
		t.Fatalf("claim key = %q", plan.key)
	}
}

// autoSeedHistoryService answers the one call the progress endpoint makes. The
// embedded nil interface satisfies the rest: a test that reaches them is asking
// the wrong question and will say so loudly.
type autoSeedHistoryService struct{ historyService }

func (autoSeedHistoryService) UpdatePlaybackProgress(string, models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	return models.PlaybackProgress{ID: "movie:tmdb:movie:603", MediaType: "movie", PercentWatched: 2}, nil
}

// The viewer is the point. A relay that is slow and then fails must not delay,
// alter, or break the heartbeat the player is waiting on.
func TestAutoSeedFailureDoesNotAffectThePlaybackResponse(t *testing.T) {
	relay := &autoSeedRelay{
		archiveStatus: http.StatusInternalServerError,
		archiveDelay:  2 * time.Second,
	}
	seeder := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	handler := &HistoryHandler{Service: autoSeedHistoryService{}, AutoSeeder: seeder}

	body, err := json.Marshal(moviePlayback())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/users/user1/history/progress", strings.NewReader(string(body)))
	req = mux.SetURLVars(req, map[string]string{"userID": "user1", "mediaType": "movie", "id": "tmdb:movie:603"})
	rec := httptest.NewRecorder()

	started := time.Now()
	handler.UpdatePlaybackProgress(rec, req)
	elapsed := time.Since(started)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var progress models.PlaybackProgress
	if err := json.Unmarshal(rec.Body.Bytes(), &progress); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if progress.PercentWatched != 2 {
		t.Fatalf("progress = %+v", progress)
	}
	if elapsed >= relay.archiveDelay {
		t.Fatalf("the heartbeat waited %v on the relay", elapsed)
	}
}

// fakeTMDBResolver stands in for the metadata service's id recovery, and records
// what it was asked so the caller can be shown to hand over the ids it holds.
type fakeTMDBResolver struct {
	tmdbID int64

	mu    sync.Mutex
	calls int
	kind  string
	ids   map[string]string
	title string
	year  int
}

func (f *fakeTMDBResolver) TMDBIDForExternalIDs(
	_ context.Context, contentKind string, externalIDs map[string]string, title string, year int,
) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.kind, f.ids, f.title, f.year = contentKind, externalIDs, title, year
	return f.tmdbID
}

func (f *fakeTMDBResolver) observed() (int, string, map[string]string, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.kind, f.ids, f.title, f.year
}

// appPlayback is the progress heartbeat an app actually sends, captured off the
// wire during a real playback on a live install. It is the case that matters:
// the title is named by TVDB and IMDb, sourcePath is present, and there is no
// TMDB number anywhere in the payload.
func appPlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tvdb:movie:343856",
		MovieName:  "Supergirl",
		Year:       2026,
		SourcePath: "/debrid/torbox/65419880/file/2/Supergirl.2026.MULTi.1080p.AMZN.WEB-DL.x264.AC3-KiT.mkv",
		ExternalIDs: map[string]string{
			"imdb":    "tt8814476",
			"tvdb":    "343856",
			"titleId": "tvdb:movie:343856",
		},
		Position: 3410,
		Duration: 6492,
	}
}

// The case every app client is in: a playback that names its title by TVDB and
// IMDb and sends no TMDB id. It must still seed, published under the id the
// server recovers, because the swarm keys entities by TMDB number and no app
// release is going to change what the installed clients send.
func TestAutoSeedRecoversTheTMDBIDAnAppDoesNotSend(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/Supergirl.2026.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)
	tmdb := &fakeTMDBResolver{tmdbID: 402431}
	handler.tmdb = tmdb

	plan, ok := handler.planAutoSeed(appPlayback())
	if !ok {
		t.Fatal("a playback identified by tvdb and imdb was not seedable")
	}
	// The claim is taken before the id is known, on the identity the playback
	// does carry, so a heartbeat storm still costs one submission.
	if plan.pendingKey != "pending:movie:tt8814476" {
		t.Fatalf("pending claim = %q", plan.pendingKey)
	}
	// Nothing may be looked up on the player's request path.
	if calls, _, _, _, _ := tmdb.observed(); calls != 0 {
		t.Fatalf("the id was resolved on the request path (%d calls)", calls)
	}

	plan.submit()

	calls, kind, ids, title, year := tmdb.observed()
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls)
	}
	if kind != "movie" || title != "Supergirl" || year != 2026 || ids["imdb"] != "tt8814476" {
		t.Fatalf("resolver asked with kind=%q title=%q year=%d ids=%#v", kind, title, year, ids)
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("identified remote playback entered legacy archive: %d calls", got)
	}
	if resolver.asked != appPlayback().SourcePath {
		t.Fatalf("re-resolved %q", resolver.asked)
	}
}

// Once the id is recovered, the title is claimed under the swarm's own key too,
// so a second client that does name it by TMDB id cannot seed it again.
func TestAutoSeedClaimsTheRecoveredEntityKey(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"})
	handler.tmdb = &fakeTMDBResolver{tmdbID: 603}

	plan, ok := handler.planAutoSeed(appPlayback())
	if !ok {
		t.Fatal("the app playback was not seedable")
	}
	plan.submit()
	if plan.key != "pending:movie:tt8814476" {
		t.Fatalf("the caller's copy of the plan was mutated: key = %q", plan.key)
	}

	// The same title arriving from a TMDB-native player is already accounted for.
	tmdbNative := moviePlayback()
	tmdbNative.ItemID = "tmdb:movie:603"
	if _, ok := handler.planAutoSeed(tmdbNative); ok {
		t.Fatal("the recovered title was seeded a second time under its tmdb id")
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("recovered remote playback entered legacy archive: %d calls", got)
	}
}

// A title nothing can identify must not reach the relay, and must not be retried
// on every heartbeat for the rest of the guard window.
func TestAutoSeedAbandonsAPlaybackItCannotIdentify(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)
	tmdb := &fakeTMDBResolver{tmdbID: 0}
	handler.tmdb = tmdb

	plan, ok := handler.planAutoSeed(appPlayback())
	if !ok {
		t.Fatal("the playback was dropped before anything was attempted")
	}
	plan.submit()

	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved for a title with no coordinates: %q", resolver.asked)
	}
	if _, ok := handler.planAutoSeed(appPlayback()); ok {
		t.Fatal("an unidentifiable title was retried on the next heartbeat")
	}
	if calls, _, _, _, _ := tmdb.observed(); calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 — the lookup was repeated", calls)
	}
}

// Without a resolver the handler must stay inert on an app playback rather than
// submit coordinates the relay will reject.
func TestAutoSeedWithoutAResolverDoesNotSubmitAnUnidentifiedTitle(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"})

	handler.OnPlaybackStarted(appPlayback())
	waitForSeeds(t, relay, 0)

	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
}

// A player that does send a TMDB id is answered without any lookup at all, so
// the recovery cannot become a cost on the path that already worked.
func TestAutoSeedDoesNotLookUpATitleThatNamesItsTMDBID(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	tmdb := &fakeTMDBResolver{tmdbID: 999999}
	handler.tmdb = tmdb

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("a tmdb-identified playback was not seedable")
	}
	if plan.pendingKey != "" || plan.key != "movie:603" {
		t.Fatalf("claim key = %q, pending = %q", plan.key, plan.pendingKey)
	}
	plan.submit()

	if calls, _, _, _, _ := tmdb.observed(); calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", calls)
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("tmdb-native remote playback entered legacy archive: %d calls", got)
	}
}

// A claim shortened after a transient refusal must actually lapse. Holding it for
// the full guard window locked a title out for six hours, and watching again is
// the only retry trigger this design has - so a relay that was merely restarting
// made that title silently unarchivable for the rest of the day.
func TestShortenedAutoSeedClaimLapses(t *testing.T) {
	handler := &PearTubeHandler{autoSeedClaims: map[string]time.Time{}}
	const key = "movie:603"

	if !handler.claimAutoSeed(key) {
		t.Fatal("the first claim was refused")
	}
	if handler.claimAutoSeed(key) {
		t.Fatal("a held claim was handed out twice")
	}

	// What a transient refusal does: pull the expiry in rather than dropping it.
	handler.shortenAutoSeed(key, time.Now().Add(-time.Second))

	if !handler.claimAutoSeed(key) {
		t.Fatal("a shortened claim never lapsed, so the title stays locked out")
	}
}

// Only the relay's own readiness is transient. A decision it made about this
// request is not, and retrying it on every heartbeat would be pure noise.
func TestAutoSeedRefusalTransienceFollowsTheRelaysAnswer(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		err       error
		transient bool
	}{
		{"relay not ready", &peartube.APIError{Status: http.StatusServiceUnavailable}, true},
		{"relay bug", &peartube.APIError{Status: http.StatusInternalServerError}, true},
		{"rate limited", &peartube.APIError{Status: http.StatusTooManyRequests}, true},
		{"unreachable", errors.New("dial tcp: connection refused"), true},
		{"request refused", &peartube.APIError{Status: http.StatusBadRequest}, false},
		{"consent denied", &peartube.APIError{Status: http.StatusForbidden}, false},
		{"conflict", &peartube.APIError{Status: http.StatusConflict}, false},
	} {
		if got := autoSeedRefusalIsTransient(testCase.err); got != testCase.transient {
			t.Fatalf("%s: transient = %v, want %v", testCase.name, got, testCase.transient)
		}
	}
}

// A usenet title plays from a WebDAV path, and the streaming providers are keyed
// on the path underneath that prefix. Passing the prefixed path through meant no
// provider matched, so every usenet title reported "automatic contribution source
// unavailable" while the same file played perfectly. Usenet is the case worth
// getting right: it is already a local file, so it archives at disk speed with no
// expiring address and no debrid API calls competing with playback.
func TestAutoSeedStreamPathDropsTheWebDAVPrefix(t *testing.T) {
	for _, testCase := range []struct{ in, want string }{
		{"/webdav/1787508427603176000_The.Wild.Robot.2024.mkv", "/1787508427603176000_The.Wild.Robot.2024.mkv"},
		{"webdav/1787508427603176000_The.Wild.Robot.2024.mkv", "/1787508427603176000_The.Wild.Robot.2024.mkv"},
		{"  /webdav/spaced.mkv  ", "/spaced.mkv"},
		// A debrid path has no WebDAV prefix and must be handed over untouched.
		{"/debrid/torbox/12345/File/9/The.Matrix.1999.mkv", "/debrid/torbox/12345/File/9/The.Matrix.1999.mkv"},
		// Not the prefix, just a file that happens to start with the letters.
		{"/webdavish/file.mkv", "/webdavish/file.mkv"},
		{"", ""},
	} {
		if got := normalizeAutoSeedStreamPath(testCase.in); got != testCase.want {
			t.Fatalf("normalize(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

// redriveRelay is a relay that also answers "how is that job doing?", which is
// what a revival turns on and the seed-only harness above never served. It keeps
// every submission in order, because a resume is only a resume if the second one
// lands on the job id the first one created.
type redriveRelay struct {
	mu          sync.Mutex
	submissions []map[string]any
	jobIDs      []string
	// job is what GET /api/v2/ingest/jobs/<id> reports, minus the id itself,
	// which the relay always echoes back from the request.
	job map[string]any
	// queries counts the asks, so "MediaStorm stopped asking" is observable.
	queries int
	// submitStatus refuses submissions with an HTTP status, so a re-drive that
	// arrives while the relay is still coming up can be simulated.
	submitStatus int
}

func (relay *redriveRelay) reports(job map[string]any) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	relay.job = job
}

func (relay *redriveRelay) refuseSubmissions(status int) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	relay.submitStatus = status
}

func (relay *redriveRelay) submissionCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.submissions)
}

func (relay *redriveRelay) queryCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return relay.queries
}

func (relay *redriveRelay) submission(index int) map[string]any {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if index >= len(relay.submissions) {
		return nil
	}
	return relay.submissions[index]
}

func (relay *redriveRelay) jobID(index int) string {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if index >= len(relay.jobIDs) {
		return ""
	}
	return relay.jobIDs[index]
}

func (relay *redriveRelay) client(t *testing.T) *peartube.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v2/policy":
			_, _ = w.Write([]byte(`{"policy":{"policyVersion":2}}`))

		case strings.HasPrefix(r.URL.Path, "/api/v1/catalog"):
			_, _ = w.Write([]byte(`{"entities":[],"nextCursor":null}`))

		case r.URL.Path == "/api/v2/ingest/jobs":
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			jobID := r.Header.Get("X-PearTube-Job-ID")
			relay.mu.Lock()
			relay.submissions = append(relay.submissions, body)
			relay.jobIDs = append(relay.jobIDs, jobID)
			refusal := relay.submitStatus
			relay.mu.Unlock()
			if refusal != 0 {
				w.WriteHeader(refusal)
				_, _ = w.Write([]byte(`{"error":{"code":"UNAVAILABLE","message":"still starting","field":null}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job":{"jobId":"` + jobID + `","state":"queued"}}`))

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/ingest/jobs/"):
			relay.mu.Lock()
			relay.queries++
			report := map[string]any{}
			for name, value := range relay.job {
				report[name] = value
			}
			relay.mu.Unlock()
			report["jobId"] = strings.TrimPrefix(r.URL.Path, "/api/v2/ingest/jobs/")
			encoded, err := json.Marshal(map[string]any{"job": report})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(encoded)

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/ingest/jobs/"):
			_, _ = w.Write([]byte(`{"job":{"jobId":"cancelled","state":"cancelled"}}`))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}
	return client
}

// newRedriveHandler is the same consented install newAutoSeedHandler builds,
// pointed at a relay that answers job queries.
func newRedriveHandler(t *testing.T, relay *redriveRelay, resolver *fakeStreamResolver) *PearTubeHandler {
	t.Helper()
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	sourceGrants := peartube.NewSourceGrantRegistryFromEnv()
	t.Cleanup(sourceGrants.Close)
	return &PearTubeHandler{
		relay:                  relay.client(t),
		streams:                resolver,
		sourceGrants:           sourceGrants,
		contributeWatchedMedia: true,
		resolved: peartube.Resolved{
			ConsentVersion:         config.PearTubeConsentVersion,
			ContributeWatchedMedia: true,
			ContributionBudget:     1,
		},
	}
}

// acceptedRedriveJob submits a movie the ordinary way and returns the job id the
// relay derived for it, which is the identity a revival has to land back on.
func acceptedRedriveJob(t *testing.T, handler *PearTubeHandler, relay *redriveRelay) string {
	t.Helper()
	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the movie was not seedable")
	}
	plan.submit()
	if got := relay.submissionCount(); got != 1 {
		t.Fatalf("initial submissions = %d, want 1", got)
	}
	return relay.jobID(0)
}

func requestFingerprint(t *testing.T, submission map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(submission["request"])
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// The archive that has to stop dying. A relay restarted for an update, killed by
// a crash, or bounced by restart=on-failure leaves the job failed and
// recoverable with every confirmed byte still staged, waiting for a capability
// whose grant lifetime is far shorter than the transfer it was serving. Only this
// process can issue one, and nothing ever asked - so three archives died in one
// day at 528, 448 and 100 MiB of real transferred bytes.
func TestAutoSeedRedrivesARecoverableFailureFromItsConfirmedBytes(t *testing.T) {
	relay := &redriveRelay{}
	resolver := &fakeStreamResolver{
		url:     "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv",
		content: []byte(strings.Repeat("matrix", 512)),
	}
	handler := newRedriveHandler(t, relay, resolver)
	accepted := acceptedRedriveJob(t, handler, relay)

	relay.reports(map[string]any{
		"state":         "failed",
		"errorCode":     "PUBLICATION_RESULT_UNAVAILABLE",
		"recoverable":   true,
		"bytesReceived": 553648128,
		"expectedBytes": 1073741824,
	})
	handler.sweepAutoSeedJobs(context.Background())

	if got := relay.queryCount(); got != 1 {
		t.Fatalf("job queries = %d, want the sweep to ask exactly once", got)
	}
	if got := relay.submissionCount(); got != 2 {
		t.Fatalf("submissions = %d, want the interrupted job re-driven once", got)
	}
	// Resuming and restarting are told apart by the job id: the relay derives it
	// from the idempotency key and the request, and reopens a recoverable job at
	// the offset it reached. A different id is a new job at byte zero.
	if got := relay.jobID(1); got != accepted {
		t.Fatalf("re-drive landed on job %q, want %q - a different id discards the staged bytes", got, accepted)
	}
	first, second := relay.submission(0), relay.submission(1)
	if second["idempotencyKey"] != first["idempotencyKey"] {
		t.Fatalf("re-drive idempotency key = %v, want the original %v", second["idempotencyKey"], first["idempotencyKey"])
	}
	if got, want := requestFingerprint(t, second), requestFingerprint(t, first); got != want {
		t.Fatalf("re-drive request changed, so the relay hashes a different job:\n got %s\nwant %s", got, want)
	}
	// A fresh capability is the whole point of asking MediaStorm rather than
	// letting the relay retry itself: the original grant expired long before the
	// relay came back.
	capability, _ := second["sourceCapability"].(string)
	if len(capability) != 43 {
		t.Fatalf("re-drive capability = %q, want an opaque 43-character token", capability)
	}
	if capability == first["sourceCapability"] {
		t.Fatal("re-drive reused the dead capability instead of issuing a fresh one")
	}
}

// A failure the relay calls unrecoverable is a statement that the staged bytes
// cannot become the title this job asked for. No capability answers that, so
// re-driving it would be a resubmit loop with a guaranteed outcome.
func TestAutoSeedDoesNotRedriveAnUnrecoverableFailure(t *testing.T) {
	relay := &redriveRelay{}
	handler := newRedriveHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	acceptedRedriveJob(t, handler, relay)

	relay.reports(map[string]any{
		"state":         "failed",
		"errorCode":     "SOURCE_LENGTH_MISMATCH",
		"recoverable":   false,
		"bytesReceived": 4096,
		"expectedBytes": 1073741824,
	})
	handler.sweepAutoSeedJobs(context.Background())

	if got := relay.submissionCount(); got != 1 {
		t.Fatalf("submissions = %d, want the unrecoverable job left alone", got)
	}
	// And it is not asked about again: a verdict that cannot change is not worth
	// a round trip per sweep for the rest of the process's life.
	handler.sweepAutoSeedJobs(context.Background())
	if got := relay.queryCount(); got != 1 {
		t.Fatalf("job queries = %d, want MediaStorm to stop asking after the verdict", got)
	}
}

// A job that is still working must never be resubmitted: its bytes are already
// moving, and a second submission would only race the first.
func TestAutoSeedDoesNotResubmitALiveJob(t *testing.T) {
	relay := &redriveRelay{}
	handler := newRedriveHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	acceptedRedriveJob(t, handler, relay)

	relay.reports(map[string]any{
		"state":         "acquiring",
		"recoverable":   false,
		"bytesReceived": 104857600,
		"expectedBytes": 1073741824,
	})
	handler.sweepAutoSeedJobs(context.Background())
	handler.sweepAutoSeedJobs(context.Background())

	if got := relay.submissionCount(); got != 1 {
		t.Fatalf("submissions = %d, want a live job left to run", got)
	}
	// It stays answerable, because this is the job that will need reviving if the
	// relay dies while it runs.
	if got := relay.queryCount(); got != 2 {
		t.Fatalf("job queries = %d, want the live job still watched", got)
	}
	// The other half of the guard, and the one that predates this: the title's
	// claim is what stops a heartbeat resubmitting while the job runs.
	if _, ok := handler.planAutoSeed(moviePlayback()); ok {
		t.Fatal("a title with a live job was claimed for a second seed")
	}
}

// One re-drive per failure, and the sweep that follows must not send another.
// This is the loop that would otherwise cost a whole-file fetch attempt every
// sweep, forever, for a title that cannot get past the same offset.
func TestAutoSeedRedrivesOneFailureExactlyOnce(t *testing.T) {
	relay := &redriveRelay{}
	handler := newRedriveHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	acceptedRedriveJob(t, handler, relay)

	stalled := map[string]any{
		"state":         "failed",
		"errorCode":     "PUBLICATION_RESULT_UNAVAILABLE",
		"recoverable":   true,
		"bytesReceived": 104857600,
		"expectedBytes": 1073741824,
	}
	relay.reports(stalled)
	for range 3 {
		handler.sweepAutoSeedJobs(context.Background())
	}

	if got := relay.submissionCount(); got != 2 {
		t.Fatalf("submissions = %d, want one re-drive for one failure", got)
	}
	// Two failures at the same offset are a loop, so the handle goes: the third
	// sweep does not even ask.
	if got := relay.queryCount(); got != 2 {
		t.Fatalf("job queries = %d, want MediaStorm to give up after a second failure at the same offset", got)
	}
}

// Progress is what licenses another attempt. Confirmed bytes moving forward mean
// the last re-drive did real work and the next one starts further along, so a
// feature-length title survives any number of interruptions - which is the whole
// reason the bound is drawn on the offset rather than on an attempt count.
func TestAutoSeedRedrivesAgainOnlyAfterMoreBytesLand(t *testing.T) {
	relay := &redriveRelay{}
	handler := newRedriveHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	accepted := acceptedRedriveJob(t, handler, relay)

	for _, confirmed := range []int{104857600, 553648128} {
		relay.reports(map[string]any{
			"state":         "failed",
			"errorCode":     "PUBLICATION_RESULT_UNAVAILABLE",
			"recoverable":   true,
			"bytesReceived": confirmed,
			"expectedBytes": 1073741824,
		})
		handler.sweepAutoSeedJobs(context.Background())
	}

	if got := relay.submissionCount(); got != 3 {
		t.Fatalf("submissions = %d, want a re-drive for each interruption that moved the offset", got)
	}
	if got := relay.jobID(2); got != accepted {
		t.Fatalf("second re-drive landed on job %q, want %q", got, accepted)
	}
}

// A re-drive the relay refuses because it is not ready yet has not answered the
// question, so the next sweep - a whole interval later, never a hot loop - asks
// again. A refusal the relay decided on stands. The distinction is the one a
// first submission already makes.
//
// Retrying a transient refusal is not licence to retry it forever: a relay that
// keeps refusing costs maxAutoSeedRedriveAttempts round trips at one offset and
// then nothing at all, however many sweeps follow. Without that bound one stuck
// job was resubmitted once a minute for the life of the process.
func TestAutoSeedRedriveRefusalFollowsTheRelaysAnswer(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		status      int
		sweeps      int
		submissions int
	}{
		{"relay still starting is retried", http.StatusServiceUnavailable, 2, 3},
		{"a relay refusing forever is not", http.StatusServiceUnavailable, 20, 1 + maxAutoSeedRedriveAttempts},
		{"relay refused the request", http.StatusBadRequest, 2, 2},
	} {
		relay := &redriveRelay{}
		handler := newRedriveHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
		acceptedRedriveJob(t, handler, relay)

		relay.reports(map[string]any{
			"state":         "failed",
			"errorCode":     "PUBLICATION_RESULT_UNAVAILABLE",
			"recoverable":   true,
			"bytesReceived": 104857600,
			"expectedBytes": 1073741824,
		})
		relay.refuseSubmissions(testCase.status)
		for range testCase.sweeps {
			handler.sweepAutoSeedJobs(context.Background())
		}

		if got := relay.submissionCount(); got != testCase.submissions {
			t.Fatalf("%s: submissions = %d, want %d", testCase.name, got, testCase.submissions)
		}
	}
}

// Progress earns a fresh round of attempts even when the earlier ones were
// refused in transit: bytes past the offset those attempts were spent on are
// evidence a re-drive did real work, and that is the only thing that licenses
// asking again. The passage of time is not.
func TestAutoSeedRedrivesAgainAfterTransientRefusalsWhenBytesAdvance(t *testing.T) {
	relay := &redriveRelay{}
	handler := newRedriveHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	accepted := acceptedRedriveJob(t, handler, relay)

	stalled := func(confirmed int) map[string]any {
		return map[string]any{
			"state":         "failed",
			"errorCode":     "PUBLICATION_RESULT_UNAVAILABLE",
			"recoverable":   true,
			"bytesReceived": confirmed,
			"expectedBytes": 1073741824,
		}
	}

	// One attempt refused in transit. It is counted, not given back.
	relay.reports(stalled(104857600))
	relay.refuseSubmissions(http.StatusServiceUnavailable)
	handler.sweepAutoSeedJobs(context.Background())
	if got := relay.submissionCount(); got != 2 {
		t.Fatalf("submissions = %d, want the transient refusal to have been attempted", got)
	}

	// The relay comes back and confirms more bytes: real progress, so the job is
	// owed a fresh round of attempts from the new offset.
	relay.refuseSubmissions(0)
	relay.reports(stalled(553648128))
	handler.sweepAutoSeedJobs(context.Background())

	if got := relay.submissionCount(); got != 3 {
		t.Fatalf("submissions = %d, want a re-drive licensed by the advanced offset", got)
	}
	if got := relay.jobID(2); got != accepted {
		t.Fatalf("the licensed re-drive landed on job %q, want %q", got, accepted)
	}

	// And that landed submission spends the new offset outright: no progress, no
	// further attempt, however many sweeps follow.
	for range 5 {
		handler.sweepAutoSeedJobs(context.Background())
	}
	if got := relay.submissionCount(); got != 3 {
		t.Fatalf("submissions = %d, want a landed re-drive to spend its offset", got)
	}
}

// The sweep must be free while nothing is wrong, and must never make the player
// wait: a heartbeat arrives every few seconds and the relay round trip belongs to
// nobody's request.
func TestAutoSeedJobSweepIsBoundedAndOffTheRequestPath(t *testing.T) {
	relay := &redriveRelay{}
	handler := newRedriveHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})

	// Nothing submitted yet: a heartbeat costs no relay call at all.
	handler.maybeSweepAutoSeedJobs()
	if got := relay.queryCount(); got != 0 {
		t.Fatalf("job queries with nothing to watch = %d, want 0", got)
	}

	acceptedRedriveJob(t, handler, relay)
	relay.reports(map[string]any{
		"state":         "acquiring",
		"bytesReceived": 4096,
		"expectedBytes": 1073741824,
	})
	for range 100 {
		handler.maybeSweepAutoSeedJobs()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		handler.autoSeedWatchMu.Lock()
		sweeping := handler.autoSeedSweeping
		handler.autoSeedWatchMu.Unlock()
		if !sweeping {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job sweep never finished")
		}
		time.Sleep(time.Millisecond)
	}
	if got := relay.queryCount(); got != 1 {
		t.Fatalf("job queries across 100 heartbeats = %d, want one sweep per interval", got)
	}
}
