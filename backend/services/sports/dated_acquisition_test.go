package sports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDatedAcquisitionCoalescesAndDoesNotBlockOtherKeys(t *testing.T) {
	s := NewService(t.TempDir())
	s.SetEnabledLeagueIDs([]string{"mlb", "nfl"})
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/mlb/") {
			if calls.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"events":[]}`))}, nil
	})}
	day := time.Now().UTC().Format("2006-01-02")
	first := make(chan error, 1)
	go func() { _, err := s.GetDatedScoreboard(context.Background(), day, "mlb"); first <- err }()
	<-started
	// An unrelated key finishes while MLB is held in the transport.
	otherCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.GetDatedScoreboard(otherCtx, day, "nfl"); err != nil {
		t.Fatal("other league blocked by cache lock", err)
	}
	waiterCtx, stop := context.WithCancel(context.Background())
	stop()
	if _, err := s.GetDatedScoreboard(waiterCtx, day, "mlb"); err != context.Canceled {
		t.Fatalf("waiter cancellation lost: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("same-key waiter started another acquisition")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDatedScoreboard(context.Background(), day, "mlb"); err != nil || calls.Load() != 1 {
		t.Fatal("published result not cached", err, calls.Load())
	}
}

func TestCricketDatedAcquisitionRetainsOverlappingTestOnly(t *testing.T) {
	s := NewService(t.TempDir())
	league := League{ID: "cricket-8048", Sport: "cricket", Slug: "8048", EventKind: "matchup"}
	var calls atomic.Int32
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `{"events":[]}`
		if r.URL.Query().Get("dates") == "" {
			body = `{"events":[{"id":"ongoing","date":"2026-09-21T10:00Z","endDate":"2026-09-25T18:00Z","competitions":[{"id":"ongoing","status":{"type":{"state":"in"}},"competitors":[{"team":{"id":"a","displayName":"A"},"homeAway":"home"},{"team":{"id":"b","displayName":"B"},"homeAway":"away"}]}]},{"id":"old","date":"2026-05-01T10:00Z","endDate":"2026-05-05T18:00Z","competitions":[{"id":"old"}]}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	games, err := s.fetchDatedLeagueWithOverlap(context.Background(), league, "2026-09-24")
	if err != nil || len(games) != 1 || !strings.Contains(games[0].ID, "ongoing") || calls.Load() != 2 {
		t.Fatal(games, err, calls.Load())
	}
}

func TestCricketOptionalOverviewFailureKeepsDatedScores(t *testing.T) {
	s := NewService(t.TempDir())
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("dates") == "" {
			return nil, fmt.Errorf("overview unavailable")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"events":[]}`))}, nil
	})}
	if _, err := s.fetchDatedLeagueWithOverlap(context.Background(), League{ID: "cricket-8048", Sport: "cricket", Slug: "8048"}, "2026-09-24"); err != nil {
		t.Fatal("optional overview blanked dated response", err)
	}
}

func TestDatedCacheRetainsExpandedLeagueFallbacks(t *testing.T) {
	s := NewService(t.TempDir())
	s.SetEnabledLeagueIDs([]string{"mlb"})
	s.dated = map[string]datedEntry{}
	for i := 0; i < 340*5; i++ {
		s.dated[fmt.Sprintf("league:sample:%d", i)] = datedEntry{expires: time.Now().Add(-time.Minute)}
	}
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"events":[]}`))}, nil
	})}
	if _, err := s.GetDatedScoreboard(context.Background(), time.Now().UTC().Format("2006-01-02"), "mlb"); err != nil {
		t.Fatal(err)
	}
	if len(s.dated) < 1700 || len(s.dated) > datedCacheLimit {
		t.Fatalf("expanded fallback entries evicted: %d", len(s.dated))
	}
}

func TestDatedInitiatorCancellationDoesNotCancelSharedAcquisition(t *testing.T) {
	s := NewService(t.TempDir())
	s.SetEnabledLeagueIDs([]string{"mlb"})
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-release:
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"events":[]}`))}, nil
	})}
	day := time.Now().UTC().Format("2006-01-02")
	creatorCtx, cancel := context.WithCancel(context.Background())
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := s.GetDatedScoreboard(creatorCtx, day, "mlb"); first <- err }()
	<-started
	waiterCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	go func() { _, err := s.GetDatedScoreboard(waiterCtx, day, "mlb"); second <- err }()
	cancel()
	select {
	case err := <-first:
		if err != context.Canceled {
			t.Errorf("creator did not cancel independently: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("creator cancellation blocked")
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal("creator canceled shared work", err)
	}
	if calls.Load() != 1 {
		t.Fatal("waiter required a replacement request", calls.Load())
	}
}

func TestCricketPartialStatusSurvivesOverlapMerge(t *testing.T) {
	for _, partialOverview := range []bool{false, true} {
		s := NewService(t.TempDir())
		s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
			overview := r.URL.Query().Get("dates") == ""
			body := `{"events":[]}`
			if overview {
				body = `{"events":[{"id":"ongoing","date":"2026-09-21T10:00Z","endDate":"2026-09-25T18:00Z","competitions":[{"id":"ongoing","competitors":[{"team":{"id":"a","displayName":"A"}},{"team":{"id":"b","displayName":"B"}}]}]}]}`
			}
			if overview == partialOverview {
				body = strings.Replace(body, `{"events":`, `{"count":500,"events":`, 1)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		games, err := s.fetchDatedLeagueWithOverlap(context.Background(), League{ID: "cricket-8048", Sport: "cricket", Slug: "8048", EventKind: "matchup"}, "2026-09-24")
		if len(games) != 1 || !errors.Is(err, errPartialScoreboard) {
			t.Fatalf("overview=%v games=%d err=%v", partialOverview, len(games), err)
		}
	}
}
