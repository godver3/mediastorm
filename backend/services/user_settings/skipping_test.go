package user_settings

import (
	"sync"
	"testing"

	"novastream/models"
)

func TestSkipPreferencesPersistIndependentlyAcrossProfiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update("adult", models.UserSettings{Playback: models.PlaybackSettings{PreferredAudioLanguage: "ger"}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, patch := range []SkipPreferences{{Intro: models.BoolPtr(true)}, {Recap: models.BoolPtr(false)}, {Credits: models.BoolPtr(true)}, {NextEpisode: models.BoolPtr(false)}} {
		wg.Add(1)
		go func(p SkipPreferences) {
			defer wg.Done()
			if err := s.UpdateSkipping("adult", p); err != nil {
				t.Error(err)
			}
		}(patch)
	}
	wg.Wait()
	if err := s.UpdateSkipping("child", SkipPreferences{Intro: models.BoolPtr(false), NextEpisode: models.BoolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	// An older client's settings screen must not erase new fields it cannot decode.
	if err := s.Update("adult", models.UserSettings{Playback: models.PlaybackSettings{PreferredAudioLanguage: "eng", CreditsAutoSkip: models.BoolPtr(true)}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	adult, _ := reopened.Get("adult")
	child, _ := reopened.Get("child")
	if adult.Playback.IntroAutoSkip == nil || !*adult.Playback.IntroAutoSkip || adult.Playback.RecapAutoSkip == nil || *adult.Playback.RecapAutoSkip || !*adult.Playback.CreditsAutoSkip {
		t.Fatalf("lost skip choices: %+v", adult.Playback)
	}
	if adult.Playback.NextEpisodeAutoPlay == nil || *adult.Playback.NextEpisodeAutoPlay || child.Playback.NextEpisodeAutoPlay == nil || !*child.Playback.NextEpisodeAutoPlay {
		t.Fatal("next-episode choice was lost or leaked between profiles")
	}
	if adult.Playback.PreferredAudioLanguage != "eng" {
		t.Fatal("unrelated setting lost")
	}
	if child.Playback.IntroAutoSkip == nil || *child.Playback.IntroAutoSkip {
		t.Fatal("child inherited adult choice")
	}
	if child.Playback.CreditsAutoSkip != nil {
		t.Fatal("unrelated child preference changed")
	}
}

func TestSkipPreferencesRollbackOnStorageFailure(t *testing.T) {
	s, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSkipping("profile", SkipPreferences{Intro: models.BoolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	s.path = t.TempDir() + "/missing/settings.json"
	if err := s.UpdateSkipping("profile", SkipPreferences{Intro: models.BoolPtr(false)}); err == nil {
		t.Fatal("expected storage failure")
	}
	got, _ := s.Get("profile")
	if !*got.Playback.IntroAutoSkip {
		t.Fatal("failed save changed in-memory preference")
	}
}

// A client that saves the full settings document it read earlier (for
// example the Expo app's cached settings) must not switch intro or recap
// auto-skip back to the value it last saw.
func TestStaleFullSettingsSaveKeepsSkipChoices(t *testing.T) {
	s, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSkipping("adult", SkipPreferences{Intro: models.BoolPtr(true), Recap: models.BoolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	stale := models.UserSettings{Playback: models.PlaybackSettings{
		PreferredAudioLanguage: "eng",
		IntroAutoSkip:          models.BoolPtr(false),
		RecapAutoSkip:          models.BoolPtr(false),
	}}
	if err := s.Update("adult", stale); err != nil {
		t.Fatal(err)
	}
	adult, err := s.Get("adult")
	if err != nil || adult == nil {
		t.Fatalf("Get: %v", err)
	}
	if !*adult.Playback.IntroAutoSkip || !*adult.Playback.RecapAutoSkip {
		t.Fatal("full settings save overwrote intro/recap auto-skip")
	}
	if adult.Playback.PreferredAudioLanguage != "eng" {
		t.Fatal("full settings save did not apply its other fields")
	}
}

func TestNextEpisodeAutoplayFalseSurvivesReopenDefaultsAndStaleFullSave(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defaults := models.DefaultUserSettings()
	if defaults.Playback.NextEpisodeAutoPlay == nil || !*defaults.Playback.NextEpisodeAutoPlay {
		t.Fatal("legacy autoplay default must be on")
	}
	if err := s.UpdateSkipping("profile", SkipPreferences{NextEpisode: models.BoolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []*bool{nil, models.BoolPtr(true)} {
		if err := s.Update("profile", models.UserSettings{Playback: models.PlaybackSettings{NextEpisodeAutoPlay: value}}); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetWithDefaults("profile", defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got.Playback.NextEpisodeAutoPlay == nil || *got.Playback.NextEpisodeAutoPlay {
		t.Fatal("explicit false was treated as an empty override")
	}
	if got.Playback.CreditsAutoSkip != nil && *got.Playback.CreditsAutoSkip {
		t.Fatal("autoplay changed credits skipping")
	}
}

func TestNextEpisodeAutoplayStorageFailureRollsBack(t *testing.T) {
	s, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSkipping("profile", SkipPreferences{NextEpisode: models.BoolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	s.path = t.TempDir() + "/missing/settings.json"
	if err := s.UpdateSkipping("profile", SkipPreferences{NextEpisode: models.BoolPtr(true)}); err == nil {
		t.Fatal("expected save failure")
	}
	got, _ := s.Get("profile")
	if got.Playback.NextEpisodeAutoPlay == nil || *got.Playback.NextEpisodeAutoPlay {
		t.Fatal("failed save changed live choice")
	}
}
