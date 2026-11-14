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
	for _, patch := range []SkipPreferences{{Intro: models.BoolPtr(true)}, {Recap: models.BoolPtr(false)}, {Credits: models.BoolPtr(true)}} {
		wg.Add(1)
		go func(p SkipPreferences) {
			defer wg.Done()
			if err := s.UpdateSkipping("adult", p); err != nil {
				t.Error(err)
			}
		}(patch)
	}
	wg.Wait()
	if err := s.UpdateSkipping("child", SkipPreferences{Intro: models.BoolPtr(false)}); err != nil {
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
