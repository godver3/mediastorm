package handlers

import "testing"

func TestFindSubtitleTrackByPreferenceSkipsUnlabeledTracks(t *testing.T) {
	streams := []SubtitleStreamInfo{
		{Index: 0, Language: "", Title: "", IsForced: false},
		{Index: 1, Language: "eng", Title: "English", IsForced: false},
	}

	got := FindSubtitleTrackByPreference(streams, "eng", "on", "eng")
	if got != 1 {
		t.Fatalf("expected English subtitle track 1, got %d", got)
	}
}

func TestFindSubtitleTrackByPreferenceForcedOnlySkipsUnlabeledTracks(t *testing.T) {
	streams := []SubtitleStreamInfo{
		{Index: 0, Language: "", Title: "Forced", IsForced: true},
		{Index: 1, Language: "eng", Title: "English Forced", IsForced: true},
	}

	got := FindSubtitleTrackByPreference(streams, "eng", "forced-only", "eng")
	if got != 1 {
		t.Fatalf("expected English forced subtitle track 1, got %d", got)
	}
}
