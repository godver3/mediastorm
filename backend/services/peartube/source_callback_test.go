package peartube

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func testSourceSecret() [32]byte {
	var secret [32]byte
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	return secret
}

func newTestSourceRegistry(t *testing.T, now func() time.Time, random io.Reader, maxEntries, maxRangeBytes int) *SourceGrantRegistry {
	t.Helper()
	registry, err := NewSourceGrantRegistry(SourceGrantOptions{
		Now:             now,
		Random:          random,
		MaxEntries:      maxEntries,
		MaxRangeBytes:   int64(maxRangeBytes),
		TTL:             time.Minute,
		MaxClockSkew:    30 * time.Second,
		CompanionID:     "peartube-companion",
		CompanionSecret: testSourceSecret(),
	})
	if err != nil {
		t.Fatalf("NewSourceGrantRegistry: %v", err)
	}
	t.Cleanup(func() { registry.Close() })
	return registry
}

func issueTestSource(t *testing.T, registry *SourceGrantRegistry, body []byte, jobID string, expiresAt time.Time) IssuedSourceGrant {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	prepared, err := registry.Prepare(path, "video/x-matroska")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	grant, err := registry.Issue(prepared, SourceGrantScope{
		CompanionID: "peartube-companion",
		JobID:       jobID,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		prepared.Close()
		t.Fatalf("Issue: %v", err)
	}
	return grant
}

func signedSourceRequest(t *testing.T, method, capability, jobID, etag, byteRange, client string, secret [32]byte, now time.Time, nonce string) *http.Request {
	t.Helper()
	target := sourceCallbackPrefix + capability
	req := httptest.NewRequest(method, target, nil)
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	req.Header.Set("X-PearTube-Client", client)
	req.Header.Set("X-PearTube-Timestamp", timestamp)
	req.Header.Set("X-PearTube-Nonce", nonce)
	req.Header.Set("X-PearTube-MAC", companionRequestMACWithBodyHash(method, target, timestamp, nonce, emptyCompanionBodyHash, secret[:]))
	req.Header.Set("X-PearTube-Job-ID", jobID)
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	return req
}

type blockingSourceWriter struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
	once    sync.Once
	bytes   int
}

func (w *blockingSourceWriter) Header() http.Header {
	return w.header
}

func (w *blockingSourceWriter) WriteHeader(int) {}

func (w *blockingSourceWriter) Write(body []byte) (int, error) {
	w.once.Do(func() {
		close(w.started)
		<-w.release
	})
	w.bytes += len(body)
	return len(body), nil
}

func serveSource(registry *SourceGrantRegistry, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router := mux.NewRouter()
	router.Handle(sourceCallbackPrefix+"{sourceCapability:[A-Za-z0-9_-]{43}}", registry).Methods(http.MethodHead, http.MethodGet, http.MethodDelete)
	router.ServeHTTP(recorder, request)
	return recorder
}

type blockingSourceResponse struct {
	header  http.Header
	started chan struct{}
	release <-chan struct{}
	status  int
}

func (w *blockingSourceResponse) Header() http.Header {
	return w.header
}

func (w *blockingSourceResponse) WriteHeader(status int) {
	w.status = status
}

func (w *blockingSourceResponse) Write(body []byte) (int, error) {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return len(body), nil
}

func TestSourceGrantBindsAuthenticationJobETagAndExactRanges(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestSourceRegistry(t, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{7}, 256)), 8, 4)
	body := []byte("0123456789abcdef")
	grant := issueTestSource(t, registry, body, "ing_job1", now.Add(time.Minute))
	secret := testSourceSecret()

	unauthenticated := httptest.NewRequest(http.MethodHead, sourceCallbackPrefix+grant.Capability, nil)
	if got := serveSource(registry, unauthenticated).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated HEAD = %d, want %d", got, http.StatusUnauthorized)
	}
	wrongCompanion := signedSourceRequest(t, http.MethodHead, grant.Capability, "ing_job1", grant.ETag, "", "another-companion", secret, now, "nonce-wrong-client")
	if got := serveSource(registry, wrongCompanion).Code; got != http.StatusUnauthorized {
		t.Fatalf("wrong companion HEAD = %d, want %d", got, http.StatusUnauthorized)
	}
	wrongJob := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_other", grant.ETag, "bytes=0-3", "peartube-companion", secret, now, "nonce-wrong-job")
	if got := serveSource(registry, wrongJob).Code; got != http.StatusForbidden {
		t.Fatalf("wrong job GET = %d, want %d", got, http.StatusForbidden)
	}
	wrongETag := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_job1", `"wrong"`, "bytes=0-3", "peartube-companion", secret, now, "nonce-wrong-etag")
	if got := serveSource(registry, wrongETag).Code; got != http.StatusPreconditionFailed {
		t.Fatalf("wrong ETag GET = %d, want %d", got, http.StatusPreconditionFailed)
	}

	head := signedSourceRequest(t, http.MethodHead, grant.Capability, "ing_job1", grant.ETag, "", "peartube-companion", secret, now, "nonce-head-valid")
	headResponse := serveSource(registry, head)
	if headResponse.Code != http.StatusOK {
		t.Fatalf("HEAD = %d, body = %s", headResponse.Code, headResponse.Body.String())
	}
	if got := headResponse.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("HEAD Content-Length = %q", got)
	}
	if got := headResponse.Header().Get("ETag"); got != grant.ETag {
		t.Fatalf("HEAD ETag = %q", got)
	}
	replayedHead := signedSourceRequest(t, http.MethodHead, grant.Capability, "ing_job1", grant.ETag, "", "peartube-companion", secret, now, "nonce-head-valid")
	if got := serveSource(registry, replayedHead).Code; got != http.StatusConflict {
		t.Fatalf("replayed authentication nonce = %d, want %d", got, http.StatusConflict)
	}

	for attempt, nonce := range []string{"nonce-range-first", "nonce-range-repeat"} {
		request := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_job1", grant.ETag, "bytes=4-7", "peartube-companion", secret, now, nonce)
		response := serveSource(registry, request)
		if response.Code != http.StatusPartialContent {
			t.Fatalf("GET attempt %d = %d, body = %s", attempt, response.Code, response.Body.String())
		}
		if response.Body.String() != "4567" {
			t.Fatalf("GET attempt %d body = %q", attempt, response.Body.String())
		}
		if got := response.Header().Get("Content-Range"); got != "bytes 4-7/16" {
			t.Fatalf("GET Content-Range = %q", got)
		}
		if got := response.Header().Get("Content-Length"); got != "4" {
			t.Fatalf("GET Content-Length = %q", got)
		}
	}

	for index, byteRange := range []string{"", "bytes=0-4", "bytes=4-3", "bytes=0-3,8-11", "items=0-3", "bytes=0-16"} {
		request := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_job1", grant.ETag, byteRange, "peartube-companion", secret, now, "nonce-invalid-range-"+strconv.Itoa(index))
		if got := serveSource(registry, request).Code; got != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("range %q = %d, want %d", byteRange, got, http.StatusRequestedRangeNotSatisfiable)
		}
	}
}

func TestSourceGrantExpiresRevokesAndDetectsSourceDrift(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestSourceRegistry(t, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{9}, 256)), 8, 8)
	secret := testSourceSecret()

	// A lapsed grant answers 401, not 410. The distinction is load-bearing for a
	// long archive: a companion that reads "expired" re-attaches to the same job
	// and keeps the bytes it has already confirmed, where "gone" would tell it to
	// destroy hours of transfer. Either way no byte of this source is served.
	expired := issueTestSource(t, registry, []byte("expired-source"), "ing_expired", now.Add(time.Second))
	now = now.Add(2 * time.Second)
	expiredRequest := signedSourceRequest(t, http.MethodHead, expired.Capability, "ing_expired", expired.ETag, "", "peartube-companion", secret, now, "nonce-expired")
	expiredResponse := serveSource(registry, expiredRequest)
	if expiredResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expired HEAD = %d, want %d", expiredResponse.Code, http.StatusUnauthorized)
	}
	if expiredResponse.Header().Get("Content-Length") == strconv.Itoa(len("expired-source")) {
		t.Fatal("expired HEAD described the source it no longer grants")
	}

	now = now.Add(time.Second)
	revoked := issueTestSource(t, registry, []byte("revoked-source"), "ing_revoked", now.Add(time.Minute))
	terminal := signedSourceRequest(t, http.MethodDelete, revoked.Capability, "ing_revoked", "", "", "peartube-companion", secret, now, "nonce-terminal")
	if got := serveSource(registry, terminal).Code; got != http.StatusNoContent {
		t.Fatalf("terminal DELETE = %d, want %d", got, http.StatusNoContent)
	}
	replay := signedSourceRequest(t, http.MethodGet, revoked.Capability, "ing_revoked", revoked.ETag, "bytes=0-3", "peartube-companion", secret, now, "nonce-terminal-replay")
	if got := serveSource(registry, replay).Code; got != http.StatusGone {
		t.Fatalf("terminal replay = %d, want %d", got, http.StatusGone)
	}

	path := filepath.Join(t.TempDir(), "drift.mkv")
	if err := os.WriteFile(path, []byte("stable-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := registry.Prepare(path, "video/x-matroska")
	if err != nil {
		t.Fatal(err)
	}
	drift, err := registry.Issue(prepared, SourceGrantScope{CompanionID: "peartube-companion", JobID: "ing_drift", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	driftRequest := signedSourceRequest(t, http.MethodHead, drift.Capability, "ing_drift", drift.ETag, "", "peartube-companion", secret, now, "nonce-source-drift")
	if got := serveSource(registry, driftRequest).Code; got != http.StatusConflict {
		t.Fatalf("drifted source HEAD = %d, want %d", got, http.StatusConflict)
	}
}

func TestSourceGrantCapacityAndDigestOnlyIndex(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestSourceRegistry(t, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 256)), 1, 8)
	grant := issueTestSource(t, registry, []byte("first-source"), "ing_first", now.Add(time.Minute))

	capabilityDigest := sha256.Sum256(append([]byte(sourceCapabilityDigestDomain+"\x00"), []byte(grant.Capability)...))
	registry.mu.Lock()
	_, indexedByDigest := registry.grants[capabilityDigest]
	registry.mu.Unlock()
	if !indexedByDigest {
		t.Fatal("grant is not indexed by its domain-separated digest")
	}
	if _, err := hex.DecodeString(grant.SHA256); err != nil {
		t.Fatalf("source SHA-256 is invalid: %v", err)
	}

	path := filepath.Join(t.TempDir(), "second.mkv")
	if err := os.WriteFile(path, []byte("second-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := registry.Prepare(path, "video/x-matroska")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if _, err := registry.Issue(prepared, SourceGrantScope{CompanionID: "peartube-companion", JobID: "ing_second", ExpiresAt: now.Add(time.Minute)}); err == nil {
		t.Fatal("capacity exhaustion unexpectedly issued a second grant")
	}
}

func TestActiveRevokedAndExpiredGrantsRemainCapacityChargedUntilReaderExits(t *testing.T) {
	for _, mode := range []string{"revoked", "expired"} {
		t.Run(mode, func(t *testing.T) {
			now := time.UnixMilli(1_786_406_400_000)
			registry := newTestSourceRegistry(t, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{5}, 256)), 1, 4)
			expiresAt := now.Add(time.Minute)
			if mode == "expired" {
				expiresAt = now.Add(time.Second)
			}
			grant := issueTestSource(t, registry, []byte("reader-source"), "ing_reader", expiresAt)
			releaseWrite := make(chan struct{})
			writer := &blockingSourceResponse{
				header:  make(http.Header),
				started: make(chan struct{}, 1),
				release: releaseWrite,
			}
			request := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_reader", grant.ETag, "bytes=0-3", "peartube-companion", testSourceSecret(), now, "nonce-reader")
			done := make(chan struct{})
			go func() {
				registry.ServeHTTP(writer, request)
				close(done)
			}()
			<-writer.started

			if mode == "expired" {
				now = now.Add(2 * time.Second)
			} else {
				registry.RevokeJob("ing_reader")
			}
			// Revoked is terminal, so 410. Expired is not, so 401. Both must
			// still refuse the lookup while a reader is mid-flight.
			wantStatus := http.StatusGone
			if mode == "expired" {
				wantStatus = http.StatusUnauthorized
			}
			replay := signedSourceRequest(t, http.MethodHead, grant.Capability, "ing_reader", grant.ETag, "", "peartube-companion", testSourceSecret(), now, "nonce-reader-replay")
			recorder := httptest.NewRecorder()
			registry.ServeHTTP(recorder, replay)
			if recorder.Code != wantStatus {
				t.Fatalf("%s active lookup = %d, want %d", mode, recorder.Code, wantStatus)
			}

			nextPath := filepath.Join(t.TempDir(), "next-source.mkv")
			if err := os.WriteFile(nextPath, []byte("next-source"), 0o600); err != nil {
				t.Fatal(err)
			}
			prepared, err := registry.Prepare(nextPath, "video/x-matroska")
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Close()
			nextScope := SourceGrantScope{CompanionID: "peartube-companion", JobID: "ing_next"}
			if _, err := registry.Issue(prepared, nextScope); err == nil {
				t.Fatal("active revoked source stopped counting toward capacity")
			}

			close(releaseWrite)
			<-done
			if writer.status != http.StatusPartialContent {
				t.Fatalf("blocked GET status = %d", writer.status)
			}
			if _, err := registry.Issue(prepared, nextScope); err != nil {
				t.Fatalf("capacity did not return after reader exit: %v", err)
			}
		})
	}
}

func TestCompanionIngestJobIdentityGoldenVector(t *testing.T) {
	request := companionIngestRequest{
		RetentionClass: "archive-pin",
		MediaContext: companionIngestMediaContext{
			Kind:       "movie",
			Namespace:  "tmdb",
			Identifier: "603",
		},
		MeasuredFacts: companionIngestMeasuredFacts{
			Title:      "Café <>& \u2028 \u2029 / \\ \"",
			ByteLength: 12,
			DurationMS: 7_200_000,
			Container:  "mkv",
		},
		Expected: companionIngestExpected{
			ByteLength: 12,
			SHA256:     optionalDigest(strings.Repeat("ab", 32)),
			ETag:       `"source-immutable-v1"`,
		},
		BundleProvenance: &companionIngestBundleProvenance{
			SourceKind:  "archive",
			ReleaseName: "Nested object",
		},
	}
	canonical, err := canonicalCompanionJSON(request)
	if err != nil {
		t.Fatalf("canonicalCompanionJSON: %v", err)
	}
	if bytes.HasSuffix(canonical, []byte{'\n'}) {
		t.Fatal("canonical request has a trailing newline")
	}
	const canonicalHex = "7b2262756e646c6550726f76656e616e6365223a7b2272656c656173654e616d65223a224e6573746564206f626a656374222c22736f757263654b696e64223a2261726368697665227d2c226578706563746564223a7b22627974654c656e677468223a31322c2265746167223a225c22736f757263652d696d6d757461626c652d76315c22222c22736861323536223a2261626162616261626162616261626162616261626162616261626162616261626162616261626162616261626162616261626162616261626162616261626162227d2c226d656173757265644661637473223a7b22627974654c656e677468223a31322c22636f6e7461696e6572223a226d6b76222c226475726174696f6e4d73223a373230303030302c227469746c65223a22436166c3a9203c3e2620e280a820e280a9202f205c5c205c22227d2c226d65646961436f6e74657874223a7b226964656e746966696572223a22363033222c226b696e64223a226d6f766965222c226e616d657370616365223a22746d6462227d2c22726574656e74696f6e436c617373223a22617263686976652d70696e227d"
	expectedCanonical, err := hex.DecodeString(canonicalHex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, expectedCanonical) {
		t.Fatalf("canonical request bytes drifted:\n got %x\nwant %x", canonical, expectedCanonical)
	}
	fingerprint, err := companionIngestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != "93794f73d757f477f8c02d7e26fba7a12e90c9570f5336f25ae2ee9c8ce03a4b" {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	const idempotencyKey = "mediastorm-v1_1212121212121212121212121212121212121212121212121212121212121212"
	jobID, err := companionIngestJobID(idempotencyKey, request)
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "ing_f8fe3828098633aca86ba6cf16eba692" {
		t.Fatalf("job ID = %q", jobID)
	}
	for _, vector := range []struct {
		title       string
		fingerprint string
		jobID       string
	}{
		{"Alien: Covenant", "7c44c2de36d8d1321ddfebf40eed7ce920781d7e75ab4bf1e3d5a36f2afc52ad", "ing_e6f968c0d313415764791cb0b74cf390"},
		{"Cars: The Movie", "b64b733743df6c57b202e38219c8fb29964b5ca0c57b6650a7577e9cdc46447a", "ing_a7417b2161a2ec2dfc9bb71976eb310f"},
	} {
		request.MeasuredFacts.Title = vector.title
		fingerprint, err := companionIngestFingerprint(request)
		if err != nil {
			t.Fatal(err)
		}
		if fingerprint != vector.fingerprint {
			t.Fatalf("%q fingerprint = %q", vector.title, fingerprint)
		}
		jobID, err := companionIngestJobID(idempotencyKey, request)
		if err != nil {
			t.Fatal(err)
		}
		if jobID != vector.jobID {
			t.Fatalf("%q job ID = %q", vector.title, jobID)
		}
	}
}

func TestArchiveSourceFailureRevokesGrantAndNeverSendsLocalPath(t *testing.T) {
	t.Setenv(CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	sourceBytes := []byte("source-bytes")
	sourcePath := filepath.Join(t.TempDir(), "private-source.mkv")
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var submissionBody []byte
	companion := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		submissionBody, _ = io.ReadAll(request.Body)
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer companion.Close()
	client, err := New(companion.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.companionHTTP = companion.Client()
	registry := newTestSourceRegistry(t, time.Now, rand.Reader, 4, 4)
	request := ArchiveRequest{
		FilePath:       sourcePath,
		IdempotencyKey: "mediastorm-v1_" + strings.Repeat("12", 32),
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "movie",
			TMDBID:      "603",
			TMDBTitle:   "The Matrix",
		},
	}
	if _, err := client.ArchiveSource(context.Background(), request, registry); err == nil {
		t.Fatal("failed companion handoff unexpectedly succeeded")
	}
	registry.mu.Lock()
	remaining := len(registry.grants)
	registry.mu.Unlock()
	if remaining != 0 {
		t.Fatal("failed handoff retained a live source grant")
	}
	if bytes.Contains(submissionBody, []byte(sourcePath)) {
		t.Fatal("companion submission exposed the local source path")
	}
	var submission map[string]any
	if err := json.Unmarshal(submissionBody, &submission); err != nil {
		t.Fatalf("decode submission: %v", err)
	}
	capability, ok := submission["sourceCapability"].(string)
	if !ok || !validSourceCapability(capability) {
		t.Fatal("companion submission omitted the opaque source capability")
	}
}

// A seed the relay declines because of the source itself has to stay
// distinguishable from a relay that is simply broken: only the first is fixed by
// offering a different source, and only the second is worth retrying as-is.
// These assertions used to ride on the URL seed transport, which no longer
// exists; the classification they defend belongs to the granted ingest that
// replaced it, so they now run against that.
func TestGrantedSourceRefusalStaysDistinctFromARelayFailure(t *testing.T) {
	for name, testCase := range map[string]struct {
		envelope string
		refused  bool
	}{
		"a source the relay will not accept": {
			envelope: `{"error":{"code":"SOURCE_HOST_NOT_PUBLIC","message":"source host 10.0.0.5 is not publicly routable","field":"url"}}`,
			refused:  true,
		},
		"a relay that cannot write its own storage": {
			envelope: `{"error":{"code":"UPLOAD_DIR_UNAVAILABLE","message":"the relay cannot write to its upload directory","field":null}}`,
			refused:  false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(CompanionSharedSecretEnv, strings.Repeat("4d", 32))
			sourcePath := filepath.Join(t.TempDir(), "source.mkv")
			if err := os.WriteFile(sourcePath, []byte("source-bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			companion := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(response, testCase.envelope)
			}))
			defer companion.Close()
			client, err := New(companion.URL)
			if err != nil {
				t.Fatal(err)
			}
			client.companionHTTP = companion.Client()
			registry := newTestSourceRegistry(t, time.Now, rand.Reader, 4, 4)
			_, err = client.ArchiveSource(context.Background(), ArchiveRequest{
				FilePath:       sourcePath,
				IdempotencyKey: "mediastorm-v1_" + strings.Repeat("34", 32),
				ArchiveCoordinates: ArchiveCoordinates{
					ContentKind: "movie",
					TMDBID:      "9522",
					TMDBTitle:   "Wedding Crashers",
				},
			}, registry)
			if err == nil {
				t.Fatal("a refused submission was reported as success")
			}
			if IsSourceRefused(err) != testCase.refused {
				t.Fatalf("IsSourceRefused = %v, want %v for %v", IsSourceRefused(err), testCase.refused, err)
			}
			if IsRelayNotOpen(err) {
				t.Fatalf("a seed refusal was mistaken for the open-access gate: %v", err)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %T (%v), want *APIError", err, err)
			}
			if apiErr.Status != http.StatusBadRequest {
				t.Fatalf("apiErr = %+v", apiErr)
			}
			// Whichever kind of refusal it was, the capability must not survive it.
			registry.mu.Lock()
			remaining := len(registry.grants)
			registry.mu.Unlock()
			if remaining != 0 {
				t.Fatal("a refused submission retained a live source grant")
			}
		})
	}
}

func TestRevokeAllInterruptsAlreadyAcquiredSourceReader(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	body := bytes.Repeat([]byte{7}, sourceServeChunkBytes*4)
	registry := newTestSourceRegistry(t, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 32)), 4, len(body))
	grant := issueTestSource(t, registry, body, "ing_slow_reader", now.Add(time.Minute))
	request := signedSourceRequest(
		t,
		http.MethodGet,
		grant.Capability,
		"ing_slow_reader",
		grant.ETag,
		"bytes=0-"+strconv.Itoa(len(body)-1),
		"peartube-companion",
		testSourceSecret(),
		now,
		"nonce-slow-reader",
	)
	writer := &blockingSourceWriter{
		header:  make(http.Header),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		registry.ServeHTTP(writer, request)
		close(done)
	}()

	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("source reader did not begin its first bounded write")
	}
	registry.RevokeAll()
	close(writer.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revoked source reader did not stop")
	}
	if writer.bytes != sourceServeChunkBytes {
		t.Fatalf("revoked reader wrote %d bytes, want only in-flight chunk %d", writer.bytes, sourceServeChunkBytes)
	}
}

func TestRevokeAllInvalidatesPreparedSourcePolicyEpoch(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestSourceRegistry(t, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{9}, 32)), 4, 64)
	path := filepath.Join(t.TempDir(), "prepared.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := registry.Prepare(path, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	epoch := registry.PolicyEpoch()
	registry.RevokeAll()
	if _, err := registry.Issue(prepared, SourceGrantScope{
		CompanionID: "peartube-companion",
		JobID:       "ing_stale_policy",
		PolicyEpoch: epoch,
	}); err == nil {
		t.Fatal("prepared source crossed a policy epoch")
	}
}
