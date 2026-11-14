package user_settings

import (
	"novastream/models"
	"testing"
)

func TestLocalPreferencePersistsAndResetsToInheritedDefault(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defaults := models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(true)}}
	if err := s.Update("search-profile", models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(false)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("local-profile", models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(true)}}); err != nil {
		t.Fatal(err)
	}
	s, err = NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"search-profile", false}, {"local-profile", true}, {"inherited-profile", true}} {
		got, err := s.GetWithDefaults(tc.id, defaults)
		if err != nil || models.BoolVal(got.Playback.PreferLocalFiles, !tc.want) != tc.want {
			t.Fatalf("profile %s did not retain its preference: %+v %v", tc.id, got.Playback, err)
		}
	}
	if err := s.Update("search-profile", models.UserSettings{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWithDefaults("search-profile", defaults)
	if err != nil || !models.BoolVal(got.Playback.PreferLocalFiles, false) {
		t.Fatal("reset did not restore server default")
	}
}
