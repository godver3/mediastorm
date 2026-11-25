package jellyfinbridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestExternalImageShrinksLargePictures: a guide image of several MB (tvspielfilm serves
// 4242x2828 camera originals) used to be refused, so recordings with it had no picture; it is
// scaled down to fit instead. Small images pass unchanged.
func TestExternalImageShrinksLargePictures(t *testing.T) {
	noisy := func(w, h int) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		rng := rand.New(rand.NewSource(1))
		for i := range img.Pix {
			img.Pix[i] = uint8(rng.Intn(256))
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	big := noisy(3000, 2000)
	if len(big) <= externalImageMaxBytes {
		t.Fatalf("test image is %d bytes, want more than %d", len(big), externalImageMaxBytes)
	}
	small := noisy(320, 180)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		if r.URL.Path == "/big.jpg" {
			_, _ = w.Write(big)
			return
		}
		_, _ = w.Write(small)
	}))
	defer ts.Close()
	saved := externalImageClient
	externalImageClient = ts.Client() // the safe client refuses loopback addresses
	defer func() { externalImageClient = saved }()

	got := fetchImage(context.Background(), ts.URL+"/big.jpg")
	if got.data == nil || len(got.data) > externalImageMaxBytes || got.contentType != "image/jpeg" ||
		got.width != externalImageShrinkSide || got.height != 1280 {
		t.Fatalf("large image: %d bytes, %s, %dx%d", len(got.data), got.contentType, got.width, got.height)
	}
	if got := fetchImage(context.Background(), ts.URL+"/small.jpg"); !bytes.Equal(got.data, small) {
		t.Fatal("a small image was changed")
	}

	// Not a picture, or one too large to decode: refused.
	if data, _ := shrinkExternalImage([]byte("not an image")); data != nil {
		t.Fatal("garbage was shrunk")
	}
	huge := image.NewGray(image.Rect(0, 0, 9000, 8000))
	huge.Set(0, 0, color.Gray{Y: 1})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, huge, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := shrinkExternalImage(buf.Bytes()); data != nil {
		t.Fatal("a 72 megapixel picture was decoded")
	}
}

// TestShrinkKeepsTransparencyOrientationAndHeight: a transparent picture stays
// transparent (PNG, not black JPEG), a camera original is turned as its EXIF
// orientation says, and a tall one is bounded by its height.
func TestShrinkKeepsTransparencyOrientationAndHeight(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	noise := func(img *image.RGBA, alpha bool) {
		for i := range img.Pix {
			img.Pix[i] = uint8(rng.Intn(256))
			if !alpha && i%4 == 3 {
				img.Pix[i] = 255
			}
		}
	}

	// A large logo: transparent background, a detailed (noisy) part that
	// makes the file larger than the cache takes.
	logo := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	detail := image.NewRGBA(image.Rect(0, 0, 1000, 800))
	noise(detail, true)
	for y := 0; y < 800; y++ {
		copy(logo.Pix[logo.PixOffset(1000, 1000+y):logo.PixOffset(2000, 1000+y)], detail.Pix[detail.PixOffset(0, y):detail.PixOffset(1000, y)])
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, logo); err != nil {
		t.Fatal(err)
	}
	out, contentType := shrinkExternalImage(buf.Bytes())
	if out == nil || contentType != "image/png" {
		t.Fatalf("transparent picture: %d bytes, %q", len(out), contentType)
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := decoded.At(0, 0).RGBA(); a != 0 {
		t.Errorf("transparent corner has alpha %d", a)
	}

	// A 3000x2000 JPEG tagged "rotate 90° clockwise" (orientation 6).
	photo := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	noise(photo, false)
	buf.Reset()
	if err := jpeg.Encode(&buf, photo, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	exif := []byte{0xFF, 0xE1, 0, 0}
	exif = append(exif, "Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00"...)
	exif[2], exif[3] = byte((len(exif)-2)>>8), byte(len(exif)-2)
	tagged := append(append([]byte{0xFF, 0xD8}, exif...), buf.Bytes()[2:]...)
	if got := jpegOrientation(tagged); got != 6 {
		t.Fatalf("orientation %d, want 6", got)
	}
	out, contentType = shrinkExternalImage(tagged)
	if w, h := imageSize(out); contentType != "image/jpeg" || w != 1280 || h != 1920 {
		t.Fatalf("turned photo: %q %dx%d, want 1280x1920", contentType, w, h)
	}

	// A tall picture is bounded by its height.
	tall := image.NewRGBA(image.Rect(0, 0, 1200, 4000))
	noise(tall, false)
	buf.Reset()
	if err := jpeg.Encode(&buf, tall, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	out, _ = shrinkExternalImage(buf.Bytes())
	if w, h := imageSize(out); h != externalImageShrinkSide || w != 576 {
		t.Fatalf("tall picture: %dx%d", w, h)
	}
}

// TestExternalImageFetchesAreJoined: concurrent requests for one image fetch
// (and decode) it once.
func TestExternalImageFetchesAreJoined(t *testing.T) {
	var hits atomic.Int64
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	}))
	defer ts.Close()
	saved := externalImageClient
	externalImageClient = ts.Client()
	defer func() { externalImageClient = saved }()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); fetchImageShared(context.Background(), ts.URL+"/joined.png") }()
	}
	// Hold the fetch until it has started and every goroutine had time to
	// join it.
	for deadline := time.Now().Add(5 * time.Second); hits.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d fetches, want 1", n)
	}
}

// TestShrinkRefusesWebP: a WebP's header can name another size than the frame
// it holds, so WebPs are not decoded (a large one stays refused, as before).
func TestShrinkRefusesWebP(t *testing.T) {
	webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(webp)); err != nil || format != "webp" {
		t.Fatalf("test picture: %q, %v", format, err)
	}
	if out, _ := shrinkExternalImage(webp); out != nil {
		t.Fatal("a WebP was decoded")
	}
}
