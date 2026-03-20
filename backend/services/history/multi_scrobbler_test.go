package history

import (
	"errors"
	"novastream/models"
	"testing"
	"time"
)

// mockScrobbler is a test double for TraktScrobbler.
type mockScrobbler struct {
	enabled        bool
	enabledForUser map[string]bool
	movieCalls     int
	episodeCalls   int
	returnErr      error
}

func (m *mockScrobbler) ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	m.movieCalls++
	return m.returnErr
}

func (m *mockScrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time) error {
	m.episodeCalls++
	return m.returnErr
}

func (m *mockScrobbler) IsEnabled() bool {
	return m.enabled
}

func (m *mockScrobbler) IsEnabledForUser(userID string) bool {
	if m.enabledForUser != nil {
		return m.enabledForUser[userID]
	}
	return m.enabled
}

// mockRTScrobbler is a test double for TraktRealTimeScrobbler.
type mockRTScrobbler struct {
	progressCalls int
	stopCalls     int
}

func (m *mockRTScrobbler) HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	m.progressCalls++
}

func (m *mockRTScrobbler) StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	m.stopCalls++
}

func TestMultiScrobbler_FansOutMovie(t *testing.T) {
	s1 := &mockScrobbler{}
	s2 := &mockScrobbler{}

	multi := NewMultiScrobbler(s1, s2)
	err := multi.ScrobbleMovie("user1", 105, 0, "tt0088763", time.Now())

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if s1.movieCalls != 1 {
		t.Errorf("expected 1 call to s1, got %d", s1.movieCalls)
	}
	if s2.movieCalls != 1 {
		t.Errorf("expected 1 call to s2, got %d", s2.movieCalls)
	}
}

func TestMultiScrobbler_FansOutEpisode(t *testing.T) {
	s1 := &mockScrobbler{}
	s2 := &mockScrobbler{}

	multi := NewMultiScrobbler(s1, s2)
	err := multi.ScrobbleEpisode("user1", 75897, 2, 5, time.Now())

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if s1.episodeCalls != 1 {
		t.Errorf("expected 1 call to s1, got %d", s1.episodeCalls)
	}
	if s2.episodeCalls != 1 {
		t.Errorf("expected 1 call to s2, got %d", s2.episodeCalls)
	}
}

func TestMultiScrobbler_ContinuesOnError(t *testing.T) {
	s1 := &mockScrobbler{returnErr: errors.New("s1 failed")}
	s2 := &mockScrobbler{}

	multi := NewMultiScrobbler(s1, s2)
	err := multi.ScrobbleMovie("user1", 105, 0, "tt0088763", time.Now())

	// Should return first error but still call s2
	if err == nil {
		t.Error("expected error from s1")
	}
	if s1.movieCalls != 1 {
		t.Errorf("expected 1 call to s1, got %d", s1.movieCalls)
	}
	if s2.movieCalls != 1 {
		t.Errorf("expected 1 call to s2 despite s1 error, got %d", s2.movieCalls)
	}
}

func TestMultiScrobbler_IsEnabled(t *testing.T) {
	s1 := &mockScrobbler{enabled: false}
	s2 := &mockScrobbler{enabled: true}

	multi := NewMultiScrobbler(s1, s2)
	if !multi.IsEnabled() {
		t.Error("expected IsEnabled=true when any scrobbler is enabled")
	}

	s2.enabled = false
	if multi.IsEnabled() {
		t.Error("expected IsEnabled=false when no scrobbler is enabled")
	}
}

func TestMultiScrobbler_IsEnabledForUser(t *testing.T) {
	s1 := &mockScrobbler{enabledForUser: map[string]bool{"user1": true}}
	s2 := &mockScrobbler{enabledForUser: map[string]bool{"user2": true}}

	multi := NewMultiScrobbler(s1, s2)

	if !multi.IsEnabledForUser("user1") {
		t.Error("expected user1 enabled via s1")
	}
	if !multi.IsEnabledForUser("user2") {
		t.Error("expected user2 enabled via s2")
	}
	if multi.IsEnabledForUser("user3") {
		t.Error("expected user3 not enabled")
	}
}

func TestMultiRealTimeScrobbler_FansOut(t *testing.T) {
	rt1 := &mockRTScrobbler{}
	rt2 := &mockRTScrobbler{}

	multi := NewMultiRealTimeScrobbler(rt1, rt2)
	update := models.PlaybackProgressUpdate{MediaType: "movie", ItemID: "123"}

	multi.HandleProgressUpdate("user1", update, 50.0)
	if rt1.progressCalls != 1 || rt2.progressCalls != 1 {
		t.Errorf("expected 1 progress call each, got %d and %d", rt1.progressCalls, rt2.progressCalls)
	}

	multi.StopSession("user1", update, 90.0)
	if rt1.stopCalls != 1 || rt2.stopCalls != 1 {
		t.Errorf("expected 1 stop call each, got %d and %d", rt1.stopCalls, rt2.stopCalls)
	}
}
