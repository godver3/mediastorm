package nzbfetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/internal/httpheaders"
	"novastream/internal/providerbreaker"
	"novastream/models"
)

type budgetFunc func(context.Context, string, string, time.Time) (time.Time, error)

func (f budgetFunc) ReserveNZB(ctx context.Context, title, provider string, now time.Time) (time.Time, error) {
	return f(ctx, title, provider, now)
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func candidate(id string) models.NZBResult {
	return models.NZBResult{Title: "Arbitrary.Release.Name", Indexer: "Fixture"}.WithNZBIdentity(models.NZBIdentity{CatalogID: id, MediaType: "movie"})
}

func TestSharedAcquisitionCacheAndIndependentCancellation(t *testing.T) {
	var reservations, downloads atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	f := New(budgetFunc(func(context.Context, string, string, time.Time) (time.Time, error) {
		reservations.Add(1)
		return time.Time{}, nil
	}))
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		downloads.Add(1)
		if r.UserAgent() != "Indexer override" || r.Header.Get("Accept") == "" {
			t.Error("download headers lost")
		}
		close(started)
		<-release
		return response(200, "<nzb/>"), nil
	})}
	c := candidate("tmdb:movie:78")
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, _, err := f.Fetch(ctx, client, nil, "https://fixture/nzb", c, "Indexer override")
		first <- err
	}()
	<-started
	second := make(chan error, 1)
	go func() {
		_, _, err := f.Fetch(context.Background(), client, nil, "https://fixture/nzb", c, "Indexer override")
		second <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	data, _, err := f.Fetch(context.Background(), client, nil, "https://fixture/nzb", c, "Indexer override")
	if err != nil || string(data) != "<nzb/>" || downloads.Load() != 1 || reservations.Load() != 1 {
		t.Fatalf("cache/dedup: %q %v, downloads=%d budgets=%d", data, err, downloads.Load(), reservations.Load())
	}
}

func TestSlowTitleDoesNotBlockAnotherTitle(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	f := New(budgetFunc(func(context.Context, string, string, time.Time) (time.Time, error) { return time.Time{}, nil }))
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/slow" {
			close(started)
			<-release
		}
		return response(200, "<nzb/>"), nil
	})}
	done := make(chan error, 1)
	go func() {
		_, _, err := f.Fetch(context.Background(), client, nil, "https://fixture/slow", candidate("tmdb:movie:78"), "")
		done <- err
	}()
	<-started
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := f.Fetch(ctx, client, nil, "https://fixture/fast", candidate("tmdb:movie:79"), ""); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetRejectsMissingIdentityAndStorageFailureBeforeHTTP(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected HTTP"); return nil, nil })}
	f := New(budgetFunc(func(context.Context, string, string, time.Time) (time.Time, error) {
		return time.Time{}, errors.New("storage unavailable")
	}))
	for _, c := range []models.NZBResult{{Indexer: "Fixture"}, candidate("tmdb:movie:78")} {
		if _, _, err := f.Fetch(context.Background(), client, nil, "https://fixture/nzb", c, ""); err == nil {
			t.Fatal("expected refusal")
		}
	}
	f = New(budgetFunc(func(context.Context, string, string, time.Time) (time.Time, error) {
		return time.Now().Add(time.Minute), nil
	}))
	_, _, err := f.Fetch(context.Background(), client, nil, "https://fixture/nzb", candidate("tmdb:movie:78"), "")
	var denied *BudgetError
	if !errors.As(err, &denied) {
		t.Fatalf("expected budget error: %v", err)
	}
}

func TestHTTPFailuresAndRetryAfterUseProviderBreaker(t *testing.T) {
	for _, code := range []int{401, 404, 429, 500, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			b := providerbreaker.New()
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.UserAgent() != httpheaders.NZBDownloadUserAgent {
					t.Error("default User-Agent lost")
				}
				resp := response(code, "error")
				resp.Header.Set("Retry-After", "172800")
				return resp, nil
			})}
			_, _, err := New(nil).Fetch(context.Background(), client, b, "https://fixture/nzb", candidate("tmdb:movie:78"), "")
			if err == nil {
				t.Fatal("expected HTTP error")
			}
			allowed, until, _ := b.Allow("fixture")
			limited := code == 429 || code == 503
			// A 48 h Retry-After is honoured up to the breaker's 24 h cap.
			if allowed == limited || (limited && (time.Until(until) < 23*time.Hour || time.Until(until) > 25*time.Hour)) {
				t.Fatalf("breaker state: allowed=%v until=%v", allowed, until)
			}
		})
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }

func TestIncompleteBodyDoesNotPauseIndexer(t *testing.T) {
	b := providerbreaker.New()
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		r := response(200, "")
		r.Body = brokenBody{}
		return r, nil
	})}
	_, _, err := New(nil).Fetch(context.Background(), client, b, "https://fixture/nzb", candidate("tmdb:movie:78"), "")
	if err == nil {
		t.Fatal("expected body read failure")
	}
	if allowed, _, _ := b.Allow("fixture"); !allowed {
		t.Fatal("body failure paused indexer")
	}
}

func TestGzipResponsesReturnBodyReadErrors(t *testing.T) {
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write([]byte("<nzb/>")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "truncated"}[truncated], func(t *testing.T) {
			body := compressed.Bytes()
			if truncated {
				body = body[:len(body)-8]
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = w.Write(body)
			}))
			defer server.Close()
			data, _, err := New(nil).Fetch(t.Context(), server.Client(), providerbreaker.New(), server.URL, candidate("tmdb:movie:78"), "")
			if (err != nil) != truncated || (!truncated && string(data) != "<nzb/>") {
				t.Fatalf("data=%q err=%v", data, err)
			}
		})
	}
}

// Without a catalog identity (a query without SxxEyy, an IMDb-only movie) the
// grab is budgeted per release instead of refused.
func TestMissingIdentityBudgetsTheRelease(t *testing.T) {
	var titles []string
	f := New(budgetFunc(func(_ context.Context, title, _ string, _ time.Time) (time.Time, error) {
		titles = append(titles, title)
		return time.Time{}, nil
	}))
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(200, "<nzb/>"), nil })}
	if _, _, err := f.Fetch(context.Background(), client, nil, "https://fixture/nzb", models.NZBResult{Indexer: "Fixture"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(titles) != 1 || !strings.HasPrefix(titles[0], "release:fixture|") {
		t.Fatalf("title keys = %v", titles)
	}
}

// The release limit is per download URL: another release from the same
// indexer reserves under its own key, and a season pack fetched again for a
// different episode is served from the cache.
func TestBudgetIsPerReleaseAndCacheSpansEpisodes(t *testing.T) {
	var releases []string
	var downloads atomic.Int32
	f := New(budgetFunc(func(_ context.Context, _, release string, _ time.Time) (time.Time, error) {
		releases = append(releases, release)
		return time.Time{}, nil
	}))
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		downloads.Add(1)
		return response(200, "<nzb/>"), nil
	})}
	ep := func(n int) models.NZBResult {
		return models.NZBResult{Indexer: "Fixture"}.WithNZBIdentity(models.NZBIdentity{CatalogID: "tmdb:tv:1", MediaType: "series", Season: 2, Episode: n})
	}
	for _, step := range []struct {
		url string
		ep  int
	}{{"https://fixture/a", 5}, {"https://fixture/b", 5}, {"https://fixture/a", 6}} {
		if _, _, err := f.Fetch(context.Background(), client, nil, step.url, ep(step.ep), ""); err != nil {
			t.Fatal(err)
		}
	}
	if downloads.Load() != 2 || len(releases) != 2 || releases[0] == releases[1] {
		t.Fatalf("downloads=%d releases=%v", downloads.Load(), releases)
	}
}

func TestDownloadErrorsKeepDetailWithoutSecrets(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return response(403, "API key expired"), nil
	})}
	_, _, err := New(nil).Fetch(context.Background(), client, providerbreaker.New(), "https://user:pw@fixture/api?t=get&apikey=SECRET", candidate("tmdb:movie:78"), "")
	if err == nil || !strings.Contains(err.Error(), "API key expired") || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error lost detail: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("error leaks credentials: %v", err)
	}
	failing := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })}
	_, _, err = New(nil).Fetch(context.Background(), failing, providerbreaker.New(), "https://fixture/api?apikey=SECRET", candidate("tmdb:movie:78"), "")
	if err == nil || !strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("transport error: %v", err)
	}
}

func TestAny2xxIsSuccess(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(203, "<nzb/>"), nil })}
	if _, _, err := New(nil).Fetch(context.Background(), client, providerbreaker.New(), "https://fixture/nzb", candidate("tmdb:movie:78"), ""); err != nil {
		t.Fatal(err)
	}
}
