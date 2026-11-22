package jellyfinbridge

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageSize(t *testing.T) {
	if w, h := imageSize(testPNG(t, 512, 72)); w != 512 || h != 72 {
		t.Fatalf("size = %dx%d", w, h)
	}
	if w, h := imageSize([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>")); w != 0 || h != 0 {
		t.Fatalf("svg size = %dx%d", w, h)
	}
}

// A channel reports its logo's real ratio once the logo has been fetched;
// until then it reports 1 and the logo is fetched in the background, once.
func TestLogoAspectRatio(t *testing.T) {
	source := "https://logos.example/rtl-up-de.png"
	fetched := make(chan string, 4)
	old := fetchLogo
	fetchLogo = func(s string) {
		fetched <- s
		rememberLogoSize(s, 512, 72, false)
	}
	t.Cleanup(func() {
		fetchLogo = old
		logoSizes.Lock()
		delete(logoSizes.m, source)
		delete(logoSizes.m, "https://logos.example/line.png")
		logoSizes.Unlock()
	})

	if got := logoAspectRatio(source); got != 1 {
		t.Fatalf("before the fetch = %v, want 1", got)
	}
	select {
	case s := <-fetched:
		if s != source {
			t.Fatalf("fetched %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("logo not fetched")
	}
	deadline := time.Now().Add(5 * time.Second)
	for logoAspectRatio(source) == 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := logoAspectRatio(source); got < 7.1 || got > 7.12 {
		t.Fatalf("ratio = %v, want 512/72", got)
	}
	select {
	case s := <-fetched:
		t.Fatalf("fetched again: %q", s)
	default:
	}

	// Extremes are bounded; logos that are not URLs are not fetched.
	rememberLogoSize("https://logos.example/line.png", 1000, 10, false)
	if got := logoAspectRatio("https://logos.example/line.png"); got != logoAspectMax {
		t.Fatalf("bounded ratio = %v", got)
	}
	if got := logoAspectRatio("file:///etc/passwd"); got != 1 {
		t.Fatalf("non-URL logo = %v", got)
	}
	select {
	case s := <-fetched:
		t.Fatalf("non-URL logo fetched: %q", s)
	case <-time.After(100 * time.Millisecond):
	}
}

// Measuring logos never touches the image cache, so a lineup larger than it
// can't evict posters; sizes stay bounded.
func TestLogoSizesStayOutOfImageCache(t *testing.T) {
	externalImages.Lock()
	before := len(externalImages.entries)
	externalImages.Unlock()
	for i := range logoSizesMax + 50 {
		rememberLogoSize(fmt.Sprintf("https://logos.example/%d.png", i), 100, 50, false)
	}
	logoSizes.Lock()
	n := len(logoSizes.m)
	for k := range logoSizes.m {
		if strings.HasPrefix(k, "https://logos.example/") {
			delete(logoSizes.m, k)
		}
	}
	logoSizes.Unlock()
	externalImages.Lock()
	after := len(externalImages.entries)
	externalImages.Unlock()
	if n > logoSizesMax || after != before {
		t.Fatalf("sizes=%d (max %d), image cache %d -> %d", n, logoSizesMax, before, after)
	}
}
