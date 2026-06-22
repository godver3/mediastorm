package metadata

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	xdraw "golang.org/x/image/draw"
)

// countingRoundTripper returns a canned response for every request and counts calls.
type countingRoundTripper struct {
	mu     sync.Mutex
	calls  int
	body   string
	status int
}

func (rt *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.calls++
	rt.mu.Unlock()
	status := rt.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     make(http.Header),
	}, nil
}

func (rt *countingRoundTripper) callCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.calls
}

// TestMovieDetails_FileCacheAndSingleflightCleanup verifies that movieDetails
// persists results to the file cache (so repeat calls don't re-hit TMDB) and
// that the in-flight singleflight map does not retain completed entries.
func TestMovieDetails_FileCacheAndSingleflightCleanup(t *testing.T) {
	rt := &countingRoundTripper{body: `{"id":603,"title":"The Matrix","release_date":"1999-03-30","imdb_id":"tt0133093","runtime":136}`}
	cache := newFileCache(t.TempDir(), 24)
	c := newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache)

	ctx := context.Background()

	got, err := c.movieDetails(ctx, 603)
	if err != nil {
		t.Fatalf("first movieDetails: unexpected error: %v", err)
	}
	if got == nil || got.Name != "The Matrix" {
		t.Fatalf("first movieDetails: got %+v, want name=The Matrix", got)
	}
	if rt.callCount() != 1 {
		t.Fatalf("expected 1 network call after first fetch, got %d", rt.callCount())
	}

	// The singleflight map must not retain entries once a fetch completes.
	leaked := 0
	c.movieCache.Range(func(_, _ any) bool { leaked++; return true })
	if leaked != 0 {
		t.Fatalf("movieCache leaked %d entries after fetch, want 0", leaked)
	}

	// Second call must be served from the file cache — no additional network call.
	got2, err := c.movieDetails(ctx, 603)
	if err != nil {
		t.Fatalf("second movieDetails: unexpected error: %v", err)
	}
	if got2 == nil || got2.Name != "The Matrix" {
		t.Fatalf("second movieDetails: got %+v, want name=The Matrix", got2)
	}
	if rt.callCount() != 1 {
		t.Fatalf("expected file-cache hit (still 1 call), got %d", rt.callCount())
	}
}

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

func TestBackdropVisualDiversityScore(t *testing.T) {
	makeImage := func(left, right color.Color) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, 160, 90))
		for y := 0; y < 90; y++ {
			for x := 0; x < 160; x++ {
				if x < 80 {
					img.Set(x, y, left)
				} else {
					img.Set(x, y, right)
				}
			}
		}
		return img
	}

	primary := computeBackdropVisualSignature(makeImage(color.NRGBA{R: 220, G: 40, B: 40, A: 255}, color.NRGBA{R: 30, G: 40, B: 220, A: 255}))
	similar := computeBackdropVisualSignature(makeImage(color.NRGBA{R: 210, G: 45, B: 45, A: 255}, color.NRGBA{R: 35, G: 45, B: 210, A: 255}))
	different := computeBackdropVisualSignature(makeImage(color.NRGBA{R: 20, G: 180, B: 80, A: 255}, color.NRGBA{R: 230, G: 220, B: 40, A: 255}))

	similarScore := backdropVisualDiversityScore(primary, similar)
	differentScore := backdropVisualDiversityScore(primary, different)
	if differentScore <= similarScore {
		t.Fatalf("different score = %f, similar score = %f; want different higher", differentScore, similarScore)
	}
	if similarScore >= 0 {
		t.Fatalf("similar score = %f, want duplicate-like candidate penalized below zero", similarScore)
	}

	keyArt := image.NewRGBA(image.Rect(0, 0, 220, 124))
	for y := 0; y < 124; y++ {
		for x := 0; x < 220; x++ {
			switch {
			case x < 80:
				keyArt.Set(x, y, color.NRGBA{R: 190, G: 20, B: 30, A: 255})
			case y < 52:
				keyArt.Set(x, y, color.NRGBA{R: 20, G: 20, B: 30, A: 255})
			case x > 150:
				keyArt.Set(x, y, color.NRGBA{R: 20, G: 70, B: 190, A: 255})
			default:
				keyArt.Set(x, y, color.NRGBA{R: 230, G: 180, B: 60, A: 255})
			}
		}
	}
	croppedKeyArt := image.NewRGBA(image.Rect(0, 0, 220, 124))
	xdraw.ApproxBiLinear.Scale(croppedKeyArt, croppedKeyArt.Bounds(), keyArt, image.Rect(0, 12, 185, 124), xdraw.Over, nil)

	primaryKeyArt := computeBackdropVisualSignature(keyArt)
	croppedKeyArtSig := computeBackdropVisualSignature(croppedKeyArt)
	if cropAlignedDuplicateScore(primaryKeyArt, croppedKeyArtSig) < 28 {
		t.Fatalf("crop-aligned duplicate score = %f, want duplicate crop above threshold", cropAlignedDuplicateScore(primaryKeyArt, croppedKeyArtSig))
	}
	if score := backdropVisualDiversityScore(primaryKeyArt, croppedKeyArtSig); score >= 0 {
		t.Fatalf("cropped key-art score = %f, want duplicate-like candidate penalized below zero", score)
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
			name:     "solid black image not marked dark (solid background)",
			handler:  makePNG(color.Black, false),
			wantDark: false, // >70% opaque = solid background, skip dark flag
		},
		{
			name:     "white opaque logo is not dark",
			handler:  makePNG(color.White, false),
			wantDark: false,
		},
		{
			name:     "black on transparent background is dark",
			handler:  makePNG(color.Black, true),
			wantDark: true, // 50% opaque = cutout logo, dark flag applies
		},
		{
			name:     "solid dark gray not marked dark (solid background)",
			handler:  makePNG(color.NRGBA{30, 30, 30, 255}, false),
			wantDark: false, // >70% opaque = solid background
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
	selectLogo := func(logos []tmdbImageItem, preferredLang string) string {
		selected, ok := selectLogoCandidate(logos, preferredLang, func(tmdbImageItem) bool { return false })
		if !ok {
			return ""
		}
		return selected.FilePath
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

func TestSelectLogoCandidate_SkipsWhiteOnlySVGWithinSameLanguageTier(t *testing.T) {
	logos := []tmdbImageItem{
		{FilePath: "/white.svg", ISO6391: "en", VoteAverage: 9.0},
		{FilePath: "/colored.png", ISO6391: "en", VoteAverage: 5.0},
		{FilePath: "/fallback.png", ISO6391: "", VoteAverage: 10.0},
	}

	selected, ok := selectLogoCandidate(logos, "en", func(item tmdbImageItem) bool {
		return item.FilePath == "/white.svg"
	})
	if !ok {
		t.Fatal("expected a selected logo")
	}
	if selected.FilePath != "/colored.png" {
		t.Fatalf("selected %q, want colored same-language logo", selected.FilePath)
	}
}

func TestSelectLogoCandidate_KeepsWhiteOnlySVGWhenOnlyLowerLanguageTierAlternativesExist(t *testing.T) {
	logos := []tmdbImageItem{
		{FilePath: "/white.svg", ISO6391: "en", VoteAverage: 9.0},
		{FilePath: "/fallback.png", ISO6391: "", VoteAverage: 10.0},
	}

	selected, ok := selectLogoCandidate(logos, "en", func(item tmdbImageItem) bool {
		return item.FilePath == "/white.svg"
	})
	if !ok {
		t.Fatal("expected a selected logo")
	}
	if selected.FilePath != "/white.svg" {
		t.Fatalf("selected %q, want preferred-language logo", selected.FilePath)
	}
}

func TestIsWhiteOnlySVGXML(t *testing.T) {
	tests := []struct {
		name string
		svg  string
		want bool
	}{
		{
			name: "white class fill",
			svg:  `<svg><style>.st0{fill:#FFFFFF;}</style><path class="st0"/></svg>`,
			want: true,
		},
		{
			name: "colored class fill",
			svg:  `<svg><style>.st0{fill:#FEE303;}.st1{fill:#FFFFFF;}</style><path class="st0"/><path class="st1"/></svg>`,
			want: false,
		},
		{
			name: "rgb white fill",
			svg:  `<svg><path fill="rgb(255,255,255)"/></svg>`,
			want: true,
		},
		{
			name: "no fill",
			svg:  `<svg><path d="M0 0"/></svg>`,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWhiteOnlySVGXML(tc.svg); got != tc.want {
				t.Fatalf("isWhiteOnlySVGXML() = %v, want %v", got, tc.want)
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
