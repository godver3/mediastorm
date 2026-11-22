package jellyfinbridge

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"  // channel logos come as GIF too
	_ "image/jpeg" // and as JPEG
	_ "image/png"
	"net/url"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

// Channel logos come in every shape, from square marks to wordmarks seven
// times as wide as they are high. Clients size channel cards by
// PrimaryImageAspectRatio, which a Jellyfin server reports from the image
// file; a fixed 1 put wide logos tiny into square cards, or had them cropped.
// The bridge measures each logo when it fetches it (fetchImage) and fetches
// the logos it has not seen yet in the background, so a channel reports its
// logo's real ratio once known, and 1 until then.

const (
	logoAspectFetches = 4 // background logo fetches at a time
	logoAspectTimeout = 15 * time.Second
	// logoAspectMin and logoAspectMax bound what is reported, so a broken
	// image cannot make a card a sliver.
	logoAspectMin = 0.25
	logoAspectMax = 8.0
)

var (
	logoAspectSlots   = make(chan struct{}, logoAspectFetches)
	logoAspectPending = struct {
		sync.Mutex
		m map[string]bool
	}{m: map[string]bool{}}
	// fetchLogo fetches a logo in the background (replaced in tests).
	fetchLogo = func(source string) {
		ctx, cancel := context.WithTimeout(context.Background(), logoAspectTimeout)
		defer cancel()
		cacheExternalImage(source, fetchImage(ctx, source))
	}
)

// imageSize returns an image's width and height, 0 and 0 when its format is
// not one Go decodes (SVG).
func imageSize(data []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// logoAspectRatio is the width-to-height ratio of a channel logo: measured
// when the logo has been fetched, else 1 while it is fetched in the
// background.
func logoAspectRatio(source string) float64 {
	externalImages.Lock()
	cached, ok := externalImages.entries[source]
	externalImages.Unlock()
	if ok {
		if cached.width > 0 && cached.height > 0 {
			return min(max(float64(cached.width)/float64(cached.height), logoAspectMin), logoAspectMax)
		}
		if cached.data == nil && time.Since(cached.at) < externalImageFailTTL {
			return 1 // failed a moment ago: not fetched again yet
		}
		if cached.data != nil {
			return 1 // a format without a known size
		}
	}
	queueLogoFetch(source)
	return 1
}

// queueLogoFetch fetches a logo in the background, once at a time per logo
// and logoAspectFetches logos at once.
func queueLogoFetch(source string) {
	if u, err := url.Parse(source); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	logoAspectPending.Lock()
	if logoAspectPending.m[source] {
		logoAspectPending.Unlock()
		return
	}
	logoAspectPending.m[source] = true
	logoAspectPending.Unlock()
	go func() {
		defer func() {
			logoAspectPending.Lock()
			delete(logoAspectPending.m, source)
			logoAspectPending.Unlock()
		}()
		logoAspectSlots <- struct{}{}
		defer func() { <-logoAspectSlots }()
		fetchLogo(source)
	}()
}
