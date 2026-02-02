package metadata

import "testing"

func TestNormalizeLanguage(t *testing.T) {
	tests := map[string]string{
		"":      "en-US",
		"en":    "en-US",
		"en_US": "en-US",
		"pt-br": "pt-BR",
		"fr-FR": "fr-FR",
		"es":    "es-US",
	}
	for input, expect := range tests {
		if got := normalizeLanguage(input); got != expect {
			t.Fatalf("normalizeLanguage(%q) = %q, want %q", input, got, expect)
		}
	}
}

func TestBuildTMDBImage(t *testing.T) {
	if img := buildTMDBImage("", tmdbPosterSize, "poster"); img != nil {
		t.Fatal("expected nil image when path empty")
	}
	img := buildTMDBImage("/poster.png", tmdbPosterSize, "poster")
	if img == nil {
		t.Fatal("expected image for valid path")
	}
	if img.URL != "https://image.tmdb.org/t/p/w780/poster.png" {
		t.Fatalf("unexpected image url: %s", img.URL)
	}
	if img.Type != "poster" {
		t.Fatalf("unexpected image type: %s", img.Type)
	}
}

func TestParseTMDBYear(t *testing.T) {
	if year := parseTMDBYear("2024-05-01", ""); year != 2024 {
		t.Fatalf("expected 2024, got %d", year)
	}
	if year := parseTMDBYear("", "2019-01-01"); year != 2019 {
		t.Fatalf("expected 2019, got %d", year)
	}
	if year := parseTMDBYear("199", ""); year != 0 {
		t.Fatalf("expected 0 for invalid date, got %d", year)
	}
}
