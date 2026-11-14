package user_settings

import (
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestOlderSettingsUpdatesPreserveRecommendationChoices(t *testing.T) {
	s, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	if err := s.Update("stefan", models.UserSettings{Recommendations: &config.RecommendationSettings{UseSearchInterests: &yes, UseWatchlist: &no}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("stefan", models.UserSettings{Playback: models.PlaybackSettings{PreferredAudioLanguage: "deu"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("stefan")
	if got.Recommendations == nil || !got.Recommendations.SearchInterestsEnabled() || got.Recommendations.WatchlistEnabled() {
		t.Fatal("older client reset choices")
	}
	child, _ := s.GetWithDefaults("ella", models.DefaultUserSettings())
	if child.Recommendations != nil && child.Recommendations.SearchInterestsEnabled() {
		t.Fatal("child inherited adult search opt-in")
	}
}
