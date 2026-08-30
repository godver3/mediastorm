package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
	"novastream/services/peartube"
	"novastream/services/streaming"
)

type fakeLibrary struct {
	item      *models.LocalMediaItem
	libraries []models.LocalMediaLibrary
}

func (f fakeLibrary) GetItem(context.Context, string) (*models.LocalMediaItem, error) {
	if f.item == nil {
		return nil, os.ErrNotExist
	}
	return f.item, nil
}

func (f fakeLibrary) ListLibraries(context.Context) ([]models.LocalMediaLibrary, error) {
	return f.libraries, nil
}

// fakeStreamResolver stands in for the composite streaming provider. It does
// both jobs the real one does for a seed: it says what a /debrid/... stream path
// currently resolves to, and it serves the byte ranges a granted remote source
// is pulled through.
type fakeStreamResolver struct {
	url   string
	err   error
	asked string
	// content is the remote body served by range. Empty means a small default.
	content []byte
	ranges  []string
}

func (f *fakeStreamResolver) GetDirectURL(_ context.Context, path string) (string, error) {
	f.asked = path
	return f.url, f.err
}

func (f *fakeStreamResolver) body() []byte {
	if len(f.content) > 0 {
		return f.content
	}
	return bytes.Repeat([]byte("remote-source-bytes\n"), 8)
}

func (f *fakeStreamResolver) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	f.ranges = append(f.ranges, req.RangeHeader)
	body := f.body()
	var start, end int64
	if _, err := fmt.Sscanf(req.RangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		return nil, fmt.Errorf("fake resolver got an unusable range %q", req.RangeHeader)
	}
	if start < 0 || end < start || end >= int64(len(body)) {
		return nil, fmt.Errorf("fake resolver got an out-of-bounds range %q", req.RangeHeader)
	}
	headers := http.Header{}
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
	return &streaming.Response{
		Status:        http.StatusPartialContent,
		Headers:       headers,
		ContentLength: end - start + 1,
		Body:          io.NopCloser(bytes.NewReader(body[start : end+1])),
	}, nil
}

type seedCapture struct {
	fields map[string]string
	body   []byte
	// json is the decoded body of a URL seed; refusal, when set, is the error
	// envelope the relay answers a URL seed with instead of 202.
	json            map[string]any
	refusal         string
	idempotencyKeys []string
	policies        []map[string]any
	events          []string
}

func newSeedRelay(t *testing.T, capture *seedCapture) *peartube.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/policy" {
			capture.idempotencyKeys = append(capture.idempotencyKeys, r.Header.Get("Idempotency-Key"))
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			capture.json = map[string]any{}
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &capture.json); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if r.URL.Path == "/api/v2/policy" {
				capture.policies = append(capture.policies, capture.json)
				w.Header().Set("Content-Type", "application/json")
				capture.events = append(capture.events, "policy")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, `{"policy":{"policyVersion":2}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if capture.refusal != "" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, capture.refusal)
				return
			}
			if r.URL.Path == "/api/v2/ingest/jobs" {
				jobID := r.Header.Get("X-PearTube-Job-ID")
				capture.events = append(capture.events, "ingest")
				w.WriteHeader(http.StatusAccepted)
				io.WriteString(w, `{"job":{"jobId":"`+jobID+`","state":"queued"}}`)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"jobId":"arch_feedfacecafebeef","status":"queued","entityHint":"show:1399:s1:e2"}`)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			body, _ := io.ReadAll(part)
			if part.FileName() != "" {
				capture.body = body
				continue
			}
			capture.fields[part.FormName()] = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"jobId":"job-7","status":"queued","entityHint":"movie:603"}`)
	}))
	t.Cleanup(server.Close)
	client, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}
	return client
}

func postSeed(t *testing.T, handler *PearTubeHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/account/api/p2p/seed", bytes.NewReader(encoded))
	rec := httptest.NewRecorder()
	handler.Seed(rec, req)
	return rec
}

func postSeedWithArchiveConsent(t *testing.T, handler *PearTubeHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	handler.configMu.Lock()
	handler.resolved.ConsentVersion = config.PearTubeConsentVersion
	handler.resolved.MigrationRequired = false
	handler.resolved.ArchiveEnabled = true
	handler.resolved.ArchiveBudget = 1
	handler.configMu.Unlock()
	return postSeed(t, handler, body)
}

func TestSeedRequiresExplicitArchiveConsent(t *testing.T) {
	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{relay: newSeedRelay(t, capture)}

	rec := postSeed(t, handler, SeedRequest{
		SourceURL:   "https://cdn.example.net/movie.mkv",
		ContentKind: "movie",
		TMDBID:      "603",
		TMDBTitle:   "The Matrix",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.json != nil || capture.body != nil {
		t.Fatal("watch-only manual seed reached the relay")
	}
}

func TestSeedPublishesLocalMediaItem(t *testing.T) {
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	root := t.TempDir()
	path := filepath.Join(root, "The.Matrix.1999.mkv")
	if err := os.WriteFile(path, []byte("bytes-on-disk"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	capture := &seedCapture{fields: map[string]string{}}
	sourceGrants := peartube.NewSourceGrantRegistryFromEnv()
	t.Cleanup(sourceGrants.Close)
	handler := &PearTubeHandler{
		relay:        newSeedRelay(t, capture),
		sourceGrants: sourceGrants,
		localMedia: fakeLibrary{
			item: &models.LocalMediaItem{
				FilePath:       path,
				LibraryType:    models.LocalMediaLibraryTypeMovie,
				MatchedTitleID: "603",
				MatchedName:    "The Matrix",
				MatchedYear:    1999,
			},
			libraries: []models.LocalMediaLibrary{{RootPath: root}},
		},
	}

	rec := postSeedWithArchiveConsent(t, handler, SeedRequest{LocalMediaItemID: "item-1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp SeedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.JobID, "ing_") || resp.Status != "queued" || resp.EntityHint != "movie:603" {
		t.Fatalf("response = %+v", resp)
	}
	if len(capture.policies) != 1 {
		t.Fatalf("policy applications = %d, want 1 before ingest", len(capture.policies))
	}
	if len(capture.events) != 2 || capture.events[0] != "policy" || capture.events[1] != "ingest" {
		t.Fatalf("relay mutation order = %v, want policy then ingest", capture.events)
	}
	policy := capture.policies[0]
	if policy["policyVersion"] != float64(2) || policy["consentVersion"] != float64(1) ||
		policy["migrationRequired"] != false || policy["archiveEnabled"] != true ||
		policy["contributeWatchedMedia"] != false {
		t.Fatalf("policy snapshot = %#v", policy)
	}
	if capture.body != nil {
		t.Fatal("local media bytes crossed the companion control request")
	}
	if _, exists := capture.json["sourceCapability"]; !exists {
		t.Fatal("companion submission omitted the opaque source capability")
	}
	idempotencyKey, _ := capture.json["idempotencyKey"].(string)
	if !regexp.MustCompile(`^mediastorm-v1_[0-9a-f]{64}$`).MatchString(idempotencyKey) {
		t.Fatalf("local companion idempotency key = %q", idempotencyKey)
	}
	encodedSubmission, err := json.Marshal(capture.json)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(encodedSubmission, []byte(path)) {
		t.Fatal("companion submission exposed the local source path")
	}
}

func TestPreparedManualSeedCannotCrossConsentWithdrawal(t *testing.T) {
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	root := t.TempDir()
	path := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(path, []byte("bytes-on-disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := &seedCapture{fields: map[string]string{}}
	relay := newSeedRelay(t, capture)
	sourceGrants := peartube.NewSourceGrantRegistryFromEnv()
	t.Cleanup(sourceGrants.Close)
	handler := &PearTubeHandler{
		relay:        relay,
		sourceGrants: sourceGrants,
		localMedia: fakeLibrary{
			libraries: []models.LocalMediaLibrary{{RootPath: root}},
		},
		resolved: peartube.Resolved{
			ConsentVersion: config.PearTubeConsentVersion,
			ArchiveEnabled: true,
			ArchiveBudget:  1,
		},
	}
	submit, err := handler.planSeed(context.Background(), relay, SeedRequest{
		FilePath:    path,
		ContentKind: "movie",
		TMDBID:      "603",
		TMDBTitle:   "The Matrix",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler.configMu.Lock()
	handler.resolved.ArchiveEnabled = false
	handler.configMu.Unlock()
	sourceGrants.RevokeAll()
	if _, err := submit(context.Background()); err == nil {
		t.Fatal("prepared seed crossed archive consent withdrawal")
	}
	if len(capture.policies) != 0 {
		t.Fatal("withdrawn prepared seed reconciled policy before rejecting its stale grant")
	}
	if capture.json != nil || capture.body != nil {
		t.Fatal("withdrawn manual seed created a companion ingest")
	}
}

func TestConsentCutoverWaitsForStartedCompanionHandoff(t *testing.T) {
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	root := t.TempDir()
	path := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(path, []byte("bytes-on-disk"), 0o600); err != nil {
		t.Fatal(err)
	}

	ingestStarted := make(chan struct{})
	releaseIngest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/policy" {
			io.WriteString(w, `{"policy":{"policyVersion":2}}`)
			return
		}
		if r.URL.Path != "/api/v2/ingest/jobs" {
			http.NotFound(w, r)
			return
		}
		close(ingestStarted)
		<-releaseIngest
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"job":{"jobId":"`+r.Header.Get("X-PearTube-Job-ID")+`","state":"queued"}}`)
	}))
	t.Cleanup(server.Close)
	relay, err := peartube.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	sourceGrants := peartube.NewSourceGrantRegistryFromEnv()
	t.Cleanup(sourceGrants.Close)
	handler := &PearTubeHandler{
		relay:        relay,
		sourceGrants: sourceGrants,
		localMedia:   fakeLibrary{libraries: []models.LocalMediaLibrary{{RootPath: root}}},
		resolved: peartube.Resolved{
			RelayURL:          server.URL,
			ConsentVersion:    config.PearTubeConsentVersion,
			ArchiveEnabled:    true,
			ArchiveBudget:     1,
			MigrationRequired: false,
		},
	}
	submit, err := handler.planSeed(context.Background(), relay, SeedRequest{
		FilePath: path, ContentKind: "movie", TMDBID: "603", TMDBTitle: "The Matrix",
	})
	if err != nil {
		t.Fatal(err)
	}
	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := submit(context.Background())
		submitDone <- submitErr
	}()
	<-ingestStarted

	enabled := true
	cutoverStarted := make(chan struct{})
	cutoverDone := make(chan error, 1)
	go func() {
		close(cutoverStarted)
		cutoverDone <- handler.ApplyPearTubeSettings(config.PearTubeSettings{
			RelayURL: server.URL, Enabled: &enabled, ConsentVersion: config.PearTubeConsentVersion,
		})
	}()
	<-cutoverStarted
	select {
	case err := <-cutoverDone:
		close(releaseIngest)
		<-submitDone
		t.Fatalf("consent cutover crossed a started companion handoff: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseIngest)
	if err := <-submitDone; err != nil {
		t.Fatalf("started companion handoff failed: %v", err)
	}
	select {
	case err := <-cutoverDone:
		if err != nil {
			t.Fatalf("consent cutover failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("consent cutover did not finish after the handoff")
	}
}

func TestResolvedPolicySnapshotKeepsRolesAndBudgetsIndependent(t *testing.T) {
	resolved := peartube.Resolved{
		ConsentVersion:         config.PearTubeConsentVersion,
		ContributeWatchedMedia: true,
		ContributionBudget:     4,
		ArchiveEnabled:         false,
		ArchiveBudget:          8,
	}
	policy, err := resolvedPolicySnapshot(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.ContributeWatchedMedia || policy.ArchiveEnabled ||
		policy.ContributionBudgetBytes != 4*bytesPerGiB ||
		policy.ArchiveBudgetBytes != 8*bytesPerGiB ||
		policy.UploadCeilingBytes != 4*bytesPerGiB ||
		policy.UploadPermission != "enabled" {
		t.Fatalf("contributor policy = %#v", policy)
	}

	resolved.MigrationRequired = true
	policy, err = resolvedPolicySnapshot(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if policy.ContributeWatchedMedia || policy.ArchiveEnabled ||
		policy.UploadCeilingBytes != 0 || policy.UploadPermission != "disabled" {
		t.Fatalf("migration policy = %#v", policy)
	}
}

func TestSeedIdempotencyKeyUsesCompanionSafeToken(t *testing.T) {
	coordinates := peartube.ArchiveCoordinates{
		ContentKind: "movie",
		TMDBID:      "603",
	}
	const digest = "41e3f4eca5fe977d4cf54af8b70e45ddb536fa6c463777947d0598c72157b025"
	if got := seedIdempotencyKey(coordinates, "local:item-1"); got != "mediastorm-v1_"+digest {
		t.Fatalf("companion idempotency key = %q", got)
	}
}

func TestSeedRejectsPathOutsideAnyLibrary(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.mkv")
	if err := os.WriteFile(outside, []byte("not yours"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{
		relay:      newSeedRelay(t, capture),
		localMedia: fakeLibrary{libraries: []models.LocalMediaLibrary{{RootPath: t.TempDir()}}},
	}

	rec := postSeedWithArchiveConsent(t, handler, SeedRequest{
		FilePath:    outside,
		ContentKind: "movie",
		TMDBID:      "603",
		TMDBTitle:   "The Matrix",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.body != nil {
		t.Fatal("a file outside every library root reached the relay")
	}
}

func TestSeedRejectsRemoteAndDebridSourcesWithoutRelayMutation(t *testing.T) {
	for name, req := range map[string]SeedRequest{
		"source URL": {
			SourceURL:   "https://cdn.example.net/d/TOKEN/movie.mkv",
			ContentKind: "movie",
			TMDBID:      "9522",
			TMDBTitle:   "Wedding Crashers",
		},
		"debrid stream path": {
			StreamPath:  "/debrid/torbox/12345/file/9/movie.mkv",
			ContentKind: "movie",
			TMDBID:      "9522",
			TMDBTitle:   "Wedding Crashers",
		},
	} {
		t.Run(name, func(t *testing.T) {
			capture := &seedCapture{fields: map[string]string{}}
			resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH-TOKEN/movie.mkv"}
			handler := &PearTubeHandler{relay: newSeedRelay(t, capture), streams: resolver}
			rec := postSeedWithArchiveConsent(t, handler, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if capture.json != nil || capture.body != nil || len(capture.policies) != 0 {
				t.Fatal("remote source caused relay policy, ingest, or upload mutation")
			}
			if resolver.asked != "" {
				t.Fatal("debrid source was resolved despite local-source-only archive policy")
			}
		})
	}
}

func TestSeedRejectsAmbiguousAndMissingSources(t *testing.T) {
	for name, req := range map[string]SeedRequest{
		"both a local item and a url": {
			LocalMediaItemID: "item-1",
			SourceURL:        "https://cdn.example.net/d/TOKEN/movie.mkv",
			ContentKind:      "movie",
			TMDBID:           "9522",
			TMDBTitle:        "Wedding Crashers",
		},
		"no source at all":          {ContentKind: "movie", TMDBID: "9522", TMDBTitle: "Wedding Crashers"},
		"a url with no coordinates": {SourceURL: "https://cdn.example.net/d/TOKEN/movie.mkv"},
		"a stream path with no resolver": {
			StreamPath:  "/debrid/torbox/12345/file/9",
			ContentKind: "movie",
			TMDBID:      "9522",
			TMDBTitle:   "Wedding Crashers",
		},
	} {
		t.Run(name, func(t *testing.T) {
			capture := &seedCapture{fields: map[string]string{}}
			handler := &PearTubeHandler{relay: newSeedRelay(t, capture)}
			rec := postSeedWithArchiveConsent(t, handler, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if capture.json != nil || capture.body != nil {
				t.Fatal("an invalid seed request reached the relay")
			}
		})
	}
}

func TestSeedIsUnavailableWithoutARelay(t *testing.T) {
	handler := &PearTubeHandler{}
	rec := postSeed(t, handler, SeedRequest{LocalMediaItemID: "item-1"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	handler.Status(statusRec, httptest.NewRequest(http.MethodGet, "/account/api/p2p/status", nil))
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Enabled {
		t.Fatal("status reported p2p enabled with no relay configured")
	}
}

func TestSeedStatusProxiesRelayJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/archive/job-7" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jobId":"job-7","status":"published","title":"The Matrix","source":{"publicationId":"pub-1","renditionId":"rend-1"}}`)
	}))
	defer server.Close()
	relay, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}

	router := mux.NewRouter()
	handler := &PearTubeHandler{relay: relay}
	router.HandleFunc("/p2p/seed/{jobId}", handler.SeedStatus).Methods(http.MethodGet)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p2p/seed/job-7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var status peartube.ArchiveStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Status != "published" || status.Source == nil || status.Source.PublicationID != "pub-1" {
		t.Fatalf("status = %+v", status)
	}
}

// The status endpoint is what an operator reads when p2p produces nothing. A
// relay that refuses to enumerate has to say so, in a form a person can act on,
// rather than looking indistinguishable from a relay that is down.
func TestStatusReportsAGatedRelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":"OPEN_ACCESS_NOT_ENABLED","message":"the relay is bound to 0.0.0.0 rather than loopback, so /api/v1/catalog and /api/v1/stream refuse to enumerate or serve media; restart the relay with --api-open (or PEARTUBE_ARCHIVE_API_OPEN=1)","field":null}}`)
	}))
	defer server.Close()
	relay, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}

	rec := httptest.NewRecorder()
	handler := &PearTubeHandler{relay: relay}
	handler.resolved.ConsentVersion = config.PearTubeConsentVersion
	handler.resolved.EffectiveMode = config.PearTubeModeWatchOnly
	handler.Status(rec, httptest.NewRequest(http.MethodGet, "/account/api/p2p/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Enabled                bool   `json:"enabled"`
		State                  string `json:"state"`
		Reachable              bool   `json:"reachable"`
		NotOpen                bool   `json:"notOpen"`
		SeedingAvailable       bool   `json:"seedingAvailable"`
		Remedy                 string `json:"remedy"`
		EffectiveMode          string `json:"effectiveMode"`
		ContributeWatchedMedia bool   `json:"contributeWatchedMedia"`
		ArchiveEnabled         bool   `json:"archiveEnabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Enabled || body.State != "not_open" || !body.Reachable || !body.NotOpen || !body.SeedingAvailable {
		t.Fatalf("gated status = %s", rec.Body.String())
	}
	if body.Remedy != peartube.NotOpenRemedy {
		t.Fatalf("remedy = %q, want %q", body.Remedy, peartube.NotOpenRemedy)
	}
	if body.EffectiveMode != config.PearTubeModeWatchOnly || body.ContributeWatchedMedia || body.ArchiveEnabled {
		t.Fatalf("gated policy = %s", rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw status: %v", err)
	}
	for _, protected := range []string{"relayUrl", "detail"} {
		if _, exposed := raw[protected]; exposed {
			t.Fatalf("status exposed %s: %s", protected, rec.Body.String())
		}
	}
}

func TestStatusReportsAReadyRelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"entities":[],"nextCursor":null}`)
	}))
	defer server.Close()
	relay, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}

	rec := httptest.NewRecorder()
	handler := &PearTubeHandler{relay: relay}
	handler.Status(rec, httptest.NewRequest(http.MethodGet, "/account/api/p2p/status", nil))

	var body struct {
		State   string `json:"state"`
		NotOpen bool   `json:"notOpen"`
		Remedy  string `json:"remedy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.State != "ready" || body.NotOpen || body.Remedy != "" {
		t.Fatalf("ready status = %s", rec.Body.String())
	}
}

func TestAutoSeedCarriesTmdbArtwork(t *testing.T) {
	// A publication seeded without a cover renders as a blank card on every
	// peer that ever sees it, and a consumer cannot look one up: it holds no
	// TMDB credentials. The path travels, not the URL, because the relay
	// fetches the image itself from an origin it chose.
	update := models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "603",
		MovieName:  "The Matrix",
		Year:       1999,
		SourcePath: "/stream/abc",
		PosterURL:  "https://image.tmdb.org/t/p/w780/wr7nrhLIiFqEcOTZ4LBOJd9Kwsw.jpg",
	}
	request, ok := autoSeedRequest(update)
	if !ok {
		t.Fatal("a movie with coordinates and a stream path should be seedable")
	}
	if request.PosterPath != "/wr7nrhLIiFqEcOTZ4LBOJd9Kwsw.jpg" {
		t.Fatalf("posterPath = %q, want the provider's own file path", request.PosterPath)
	}
}

func TestTmdbPosterPathRefusesForeignArtwork(t *testing.T) {
	cases := map[string]string{
		"": "",
		"https://image.tmdb.org/t/p/w780/abc.jpg":   "/abc.jpg",
		"https://IMAGE.TMDB.ORG/t/p/original/x.png": "/x.png",
		// Not TMDB artwork: a local placeholder, a proxy, or a malformed path.
		"https://evil.example/t/p/w780/abc.jpg":   "",
		"/local/poster.jpg":                       "",
		"https://image.tmdb.org/abc.jpg":          "",
		"https://image.tmdb.org/t/p/w780/":        "",
		"https://image.tmdb.org/t/p/w780/a/b.jpg": "",
		"://nonsense":                             "",
	}
	for input, want := range cases {
		if got := tmdbPosterPath(input); got != want {
			t.Errorf("tmdbPosterPath(%q) = %q, want %q", input, got, want)
		}
	}
}
