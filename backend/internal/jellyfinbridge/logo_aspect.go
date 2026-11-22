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
	// logoAspectQueue bounds logos waiting to be measured; a lineup larger
	// than this measures the rest on later listings.
	logoAspectQueue = 1024
	// logoSizesMax bounds remembered sizes (a few bytes each, no image data),
	// well above IPTV lineups, so sizes never compete with cached images.
	logoSizesMax = 20000
	// logoAspectMin and logoAspectMax bound what is reported, so a broken
	// image cannot make a card a sliver.
	logoAspectMin = 0.25
	logoAspectMax = 8.0
)

// logoSize is a measured logo: width and height, both 0 when the format has
// no known size (SVG) or, with failed, when the fetch failed at at.
type logoSize struct {
	width, height int
	failed        bool
	at            time.Time
}

var (
	logoSizes = struct {
		sync.Mutex
		m map[string]logoSize
	}{m: map[string]logoSize{}}
	logoAspectPending = struct {
		sync.Mutex
		m map[string]bool
	}{m: map[string]bool{}}
	logoAspectJobs  = make(chan string, logoAspectQueue)
	logoAspectStart sync.Once
	// fetchLogo measures a logo in the background (replaced in tests). It
	// keeps only the size: logos do not take image-cache slots from posters.
	fetchLogo = func(source string) {
		ctx, cancel := context.WithTimeout(context.Background(), logoAspectTimeout)
		defer cancel()
		img := fetchImage(ctx, source)
		rememberLogoSize(source, img.width, img.height, img.data == nil)
	}
)

// rememberLogoSize records a logo's size (fetchImage calls it for every image
// it measures, so logos served to clients are measured too).
func rememberLogoSize(source string, width, height int, failed bool) {
	logoSizes.Lock()
	defer logoSizes.Unlock()
	if _, ok := logoSizes.m[source]; !ok && len(logoSizes.m) >= logoSizesMax {
		// Full: drop failures first, else any one entry.
		for k, v := range logoSizes.m {
			if v.failed {
				delete(logoSizes.m, k)
				break
			}
		}
		if len(logoSizes.m) >= logoSizesMax {
			for k := range logoSizes.m {
				delete(logoSizes.m, k)
				break
			}
		}
	}
	logoSizes.m[source] = logoSize{width: width, height: height, failed: failed, at: time.Now()}
}

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
	logoSizes.Lock()
	size, ok := logoSizes.m[source]
	logoSizes.Unlock()
	if ok {
		if size.width > 0 && size.height > 0 {
			return min(max(float64(size.width)/float64(size.height), logoAspectMin), logoAspectMax)
		}
		if !size.failed || time.Since(size.at) < externalImageFailTTL {
			return 1 // no known size, or failed a moment ago
		}
	}
	queueLogoFetch(source)
	return 1
}

// queueLogoFetch measures a logo in the background: once at a time per logo,
// logoAspectFetches at once, at most logoAspectQueue waiting.
func queueLogoFetch(source string) {
	if u, err := url.Parse(source); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	logoAspectStart.Do(func() {
		for range logoAspectFetches {
			go func() {
				for src := range logoAspectJobs {
					fetchLogo(src)
					logoAspectPending.Lock()
					delete(logoAspectPending.m, src)
					logoAspectPending.Unlock()
				}
			}()
		}
	})
	logoAspectPending.Lock()
	defer logoAspectPending.Unlock()
	if logoAspectPending.m[source] {
		return
	}
	select {
	case logoAspectJobs <- source:
		logoAspectPending.m[source] = true
	default: // queue full: measured on a later listing
	}
}
