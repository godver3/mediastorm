package metadata

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
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
