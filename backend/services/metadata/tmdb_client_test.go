package metadata

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

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

func TestIsImageDark(t *testing.T) {
	// Helper to create a PNG with a given color and optional transparent pixels
	makePNG := func(c color.Color, transparent bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
			for y := 0; y < 10; y++ {
				for x := 0; x < 10; x++ {
					if transparent && x < 5 {
						img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0})
					} else {
						r, g, b, a := c.RGBA()
						img.SetNRGBA(x, y, color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)})
					}
				}
			}
			w.Header().Set("Content-Type", "image/png")
			png.Encode(w, img)
		}
	}

	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantDark bool
	}{
		{
			name:     "black opaque logo is dark",
			handler:  makePNG(color.Black, false),
			wantDark: true,
		},
		{
			name:     "white opaque logo is not dark",
			handler:  makePNG(color.White, false),
			wantDark: false,
		},
		{
			name:     "black with transparency still dark",
			handler:  makePNG(color.Black, true),
			wantDark: true,
		},
		{
			name:     "dark gray (luminance ~30) is dark",
			handler:  makePNG(color.NRGBA{30, 30, 30, 255}, false),
			wantDark: true,
		},
		{
			name:     "mid gray (luminance ~128) is not dark",
			handler:  makePNG(color.NRGBA{128, 128, 128, 255}, false),
			wantDark: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			client := &tmdbClient{
				httpc: srv.Client(),
			}
			// isImageDark replaces /w500/ with /w92/, so include that in the URL
			testURL := srv.URL + "/w500/test.png"
			got := client.isImageDark(context.Background(), testURL)
			if got != tc.wantDark {
				t.Errorf("isImageDark() = %v, want %v", got, tc.wantDark)
			}
		})
	}
}

func TestIsImageDark_FetchError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	client := &tmdbClient{
		httpc: srv.Client(),
	}
	// Should return false on error (safe default)
	got := client.isImageDark(context.Background(), srv.URL+"/w500/missing.png")
	if got {
		t.Error("expected false on fetch error")
	}
}

func TestFetchImages_LogoLanguagePreference(t *testing.T) {
	// selectLogo mirrors the logo selection logic from fetchImages:
	// filter to preferred lang + English + no-language, then sort by preference tier.
	selectLogo := func(logos []tmdbImageItem, preferredLang string) string {
		var usable []tmdbImageItem
		for _, l := range logos {
			if l.ISO6391 == preferredLang || l.ISO6391 == "en" || l.ISO6391 == "" {
				usable = append(usable, l)
			}
		}
		if len(usable) == 0 {
			return ""
		}
		sort.Slice(usable, func(i, j int) bool {
			li, lj := usable[i], usable[j]
			if preferredLang != "" && preferredLang != "en" {
				iPref := li.ISO6391 == preferredLang
				jPref := lj.ISO6391 == preferredLang
				if iPref != jPref {
					return iPref
				}
			}
			iEng := li.ISO6391 == "en"
			jEng := lj.ISO6391 == "en"
			if iEng != jEng {
				return iEng
			}
			iNull := li.ISO6391 == ""
			jNull := lj.ISO6391 == ""
			if iNull != jNull {
				return iNull
			}
			return li.VoteAverage > lj.VoteAverage
		})
		return usable[0].FilePath
	}

	tests := []struct {
		name          string
		logos         []tmdbImageItem
		preferredLang string
		wantPath      string
		description   string
	}{
		{
			name: "english user: english preferred over portuguese",
			logos: []tmdbImageItem{
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 9.0},
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 5.0},
			},
			preferredLang: "en",
			wantPath:      "/en_logo.png",
			description:   "English logo should be selected even with lower vote average",
		},
		{
			name: "english user: english preferred over no-language",
			logos: []tmdbImageItem{
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 9.0},
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 5.0},
			},
			preferredLang: "en",
			wantPath:      "/en_logo.png",
			description:   "English logo should beat no-language logo",
		},
		{
			name: "english user: no-language selected when mixed with foreign",
			logos: []tmdbImageItem{
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 9.0},
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 3.0},
			},
			preferredLang: "en",
			wantPath:      "/null_logo.png",
			description:   "No-language logo should be selected; foreign logos filtered out",
		},
		{
			name: "english user: only foreign logos returns nil (Lucas the Spider case)",
			logos: []tmdbImageItem{
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 5.0},
				{FilePath: "/es_logo.png", ISO6391: "es", VoteAverage: 3.0},
			},
			preferredLang: "en",
			wantPath:      "",
			description:   "Foreign-only logos should be skipped for English users",
		},
		{
			name: "portuguese user: portuguese preferred over english",
			logos: []tmdbImageItem{
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 9.0},
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 5.0},
			},
			preferredLang: "pt",
			wantPath:      "/pt_logo.png",
			description:   "User's preferred language should win over English",
		},
		{
			name: "portuguese user: falls back to english when no pt logo",
			logos: []tmdbImageItem{
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 5.0},
				{FilePath: "/fr_logo.png", ISO6391: "fr", VoteAverage: 9.0},
			},
			preferredLang: "pt",
			wantPath:      "/en_logo.png",
			description:   "Should fall back to English when preferred language unavailable",
		},
		{
			name: "portuguese user: preferred lang over no-language",
			logos: []tmdbImageItem{
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 9.0},
				{FilePath: "/pt_logo.png", ISO6391: "pt", VoteAverage: 3.0},
			},
			preferredLang: "pt",
			wantPath:      "/pt_logo.png",
			description:   "User's language should win over no-language",
		},
		{
			name: "highest voted english logo wins among english",
			logos: []tmdbImageItem{
				{FilePath: "/en_low.png", ISO6391: "en", VoteAverage: 2.0},
				{FilePath: "/en_high.png", ISO6391: "en", VoteAverage: 8.0},
			},
			preferredLang: "en",
			wantPath:      "/en_high.png",
			description:   "Among english logos, highest vote average should win",
		},
		{
			name:          "no logos returns nil",
			logos:         []tmdbImageItem{},
			preferredLang: "en",
			wantPath:      "",
			description:   "Empty logo list should return nil logo",
		},
		{
			name: "all tiers present picks user's language",
			logos: []tmdbImageItem{
				{FilePath: "/en_logo.png", ISO6391: "en", VoteAverage: 10.0},
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 8.0},
				{FilePath: "/fr_logo.png", ISO6391: "fr", VoteAverage: 3.0},
			},
			preferredLang: "fr",
			wantPath:      "/fr_logo.png",
			description:   "User's language should win even with lowest vote average",
		},
		{
			name: "single foreign logo returns nil for english user",
			logos: []tmdbImageItem{
				{FilePath: "/ja_logo.png", ISO6391: "ja", VoteAverage: 8.0},
			},
			preferredLang: "en",
			wantPath:      "",
			description:   "A non-English/non-preferred logo should be skipped",
		},
		{
			name: "no-language fallback when no preferred or english",
			logos: []tmdbImageItem{
				{FilePath: "/null_logo.png", ISO6391: "", VoteAverage: 5.0},
				{FilePath: "/ja_logo.png", ISO6391: "ja", VoteAverage: 9.0},
			},
			preferredLang: "fr",
			wantPath:      "/null_logo.png",
			description:   "No-language logo used as last resort",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectLogo(tc.logos, tc.preferredLang)
			if got != tc.wantPath {
				t.Errorf("selected %q, want %q\n  %s", got, tc.wantPath, tc.description)
			}
		})
	}
}

func TestLogoLanguage(t *testing.T) {
	tests := []struct {
		language string
		want     string
	}{
		{"", "en"},
		{"en", "en"},
		{"eng", "en"},
		{"en-US", "en"},
		{"pt-BR", "pt"},
		{"pt_BR", "pt"},
		{"por", "pt"},
		{"fr", "fr"},
		{"fra", "fr"},
		{"ja", "ja"},
		{"jpn", "ja"},
	}
	for _, tc := range tests {
		t.Run(tc.language, func(t *testing.T) {
			client := &tmdbClient{language: tc.language}
			got := client.logoLanguage()
			if got != tc.want {
				t.Errorf("logoLanguage(%q) = %q, want %q", tc.language, got, tc.want)
			}
		})
	}
}
