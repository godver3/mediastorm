package jellyfinbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/requestsecurity"
	"novastream/models"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/sync/singleflight"
)

// handleImage serves /Items/{id}/Images/{type}[/{index}]. Jellyfin clients
// request images without credentials, and the artwork is public (TMDB/TVDB),
// so this endpoint is anonymous. Images go through mediastorm's image proxy
// for resizing and disk caching.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.ids.get(pathVar(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Image not found")
		return
	}
	source := imageURLFor(s.imageRef(ref), strings.ToLower(pathVar(r, "type")))
	if source == "" {
		writeError(w, http.StatusNotFound, "Image not found")
		return
	}
	if recordingID, ok := strings.CutPrefix(source, recordingFramePrefix); ok && ref.Kind == refRecording {
		s.serveRecordingFrame(w, r, recordingID)
		return
	}
	width := 0
	for _, key := range []string{"fillWidth", "maxWidth", "width"} {
		if n := parseIntDefault(queryValue(r, key), 0); n > 0 {
			width = n
			break
		}
	}
	if width == 0 {
		if h := parseIntDefault(firstNonEmpty(queryValue(r, "fillHeight"), queryValue(r, "maxHeight")), 0); h > 0 {
			// Posters are ~2:3; requesting by height maps to a proportional width.
			width = h * 2 / 3
			if strings.EqualFold(pathVar(r, "type"), "backdrop") || strings.EqualFold(pathVar(r, "type"), "thumb") {
				width = h * 16 / 9
			}
		}
	}
	// Local and synced libraries serve their own artwork. Their IDs are
	// random UUIDs (unguessable), so the artwork handler is called with
	// server authority instead of the (anonymous) caller's.
	if itemID, kind, ok := libraryArtworkPath(source); ok && (ref.Kind == refLocal || ref.Kind == refLocalSeries || ref.Kind == refLocalSeason) {
		s.serveLibraryArtwork(w, r, itemID, kind)
		return
	}
	if strings.EqualFold(pathVar(r, "type"), "logo") {
		// Logos are transparent PNGs; the resizing proxy turns them into
		// JPEGs with a black background.
		s.fetchExternalImage(w, r, logoSource(source))
		return
	}
	s.proxyImage(w, r, source, width, parseIntDefault(queryValue(r, "quality"), 0))
}

// logoSource asks TMDB for a TV-sized logo instead of the original file
// (often several megabytes).
func logoSource(source string) string {
	if strings.Contains(source, "image.tmdb.org/t/p/original/") {
		return strings.Replace(source, "/t/p/original/", "/t/p/w500/", 1)
	}
	return source
}

// libraryArtworkPath parses mediastorm's /api/library/items/{id}/artwork/{kind}.
func libraryArtworkPath(source string) (itemID, kind string, ok bool) {
	rest, found := strings.CutPrefix(source, "/api/library/items/")
	if !found {
		return "", "", false
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "artwork" || parts[0] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

func (s *Server) serveLibraryArtwork(w http.ResponseWriter, r *http.Request, itemID, kind string) {
	if s.deps.Handlers.LibraryArtwork == nil {
		writeError(w, http.StatusNotFound, "Image not available")
		return
	}
	system := &principal{Session: models.Session{AccountID: models.MasterAccountID, IsMaster: true}}
	target := "/api/library/items/" + url.PathEscape(itemID) + "/artwork/" + url.PathEscape(kind)
	forward(w, r, system, s.deps.Handlers.LibraryArtwork, target, map[string]string{"itemID": itemID, "kind": kind})
}

// imageRef restores parent artwork for persisted refs predating SeriesPoster.
func (s *Server) imageRef(ref itemRef) itemRef {
	if ref.Kind == refSeason && ref.Ctx.SeriesPoster == "" {
		series := seriesInfoFromSeasonRef(ref)
		if parent, ok := s.ids.get(deriveID(refSeries, series.source())); ok {
			ref.Ctx.SeriesPoster = parent.Ctx.Poster
		}
	}
	return ref
}

// imageURLFor picks the source URL for a Jellyfin image type.
func imageURLFor(ref itemRef, imageType string) string {
	c := ref.Ctx
	switch imageType {
	case "primary":
		if ref.Kind == refSeason {
			return firstNonEmpty(c.Poster, c.SeriesPoster, c.Thumb)
		}
		if ref.Kind == refEpisode || (ref.Kind == refLocal && c.ViewKey != "") {
			// Without a still the series backdrop keeps the card landscape.
			return firstNonEmpty(c.Thumb, c.Backdrop, c.Poster)
		}
		return firstNonEmpty(c.Poster, c.Thumb)
	case "backdrop", "art", "banner":
		return firstNonEmpty(c.Backdrop, c.Thumb)
	case "thumb", "screenshot":
		return firstNonEmpty(c.Thumb, c.Backdrop)
	case "logo":
		return c.Logo
	}
	return ""
}

func (s *Server) proxyImage(w http.ResponseWriter, r *http.Request, source string, width, quality int) {
	if s.deps.Handlers.ImageProxy == nil {
		writeError(w, http.StatusNotFound, "Image not available")
		return
	}
	q := url.Values{"url": {source}}
	if width > 0 {
		q.Set("w", strconv.Itoa(width))
	}
	if quality > 0 && quality <= 100 {
		q.Set("q", strconv.Itoa(quality))
	}
	req := httptest.NewRequest(http.MethodGet, "/api/images/proxy?"+q.Encode(), nil).WithContext(r.Context())
	rec := newPassthroughRecorder(w)
	s.deps.Handlers.ImageProxy(rec, req)
	if rec.status >= 400 {
		// The resizing proxy only serves TMDB/TVDB art. Channel logos and
		// guide icons live anywhere, so fetch those through the SSRF-guarded
		// client (public addresses only). Never redirect: image URLs can come
		// from user records.
		s.fetchExternalImage(w, r, source)
	}
}

// externalImages caches small fetched images (channel logos repeat on every
// guide page).
var externalImages = struct {
	sync.Mutex
	entries map[string]externalImage
	bytes   int
}{entries: map[string]externalImage{}}

type externalImage struct {
	at          time.Time
	contentType string
	data        []byte // nil: the fetch failed (negative entry)
	// width and height are the image's size, 0 when its format is not
	// known (SVG, say).
	width, height int
}

const (
	externalImageMaxBytes    = 2 << 20 // larger images are scaled down (shrinkExternalImage)
	externalImageCacheTTL    = 6 * time.Hour
	externalImageFailTTL     = 10 * time.Minute // dead logo hosts are common in IPTV lists
	externalImageCacheMax    = 512
	externalImageCacheBudget = 64 << 20
)

const (
	// externalImageFetchMaxBytes bounds the download: guide images can be
	// camera originals (tvspielfilm serves 4242x2828 JPEGs of several MB).
	externalImageFetchMaxBytes = 16 << 20
	externalImageShrinkSide    = 1920     // the longer side of a scaled-down picture
	externalImageMaxPixels     = 24 << 20 // decoding a bigger picture is refused
)

var (
	// externalImageFetches joins concurrent fetches of one image: a page of
	// recordings of one show asks for its (large) guide image many times.
	externalImageFetches singleflight.Group
	// externalImageShrinks lets one picture be decoded at a time: a
	// progressive JPEG keeps its coefficients too, up to about 0.4 GB.
	externalImageShrinks = make(chan struct{}, 1)
)

// fetchImageShared is fetchImage with concurrent fetches of one source joined.
func fetchImageShared(ctx context.Context, source string) externalImage {
	v, _, _ := externalImageFetches.Do(source, func() (any, error) {
		// A client that goes away does not fail the others waiting.
		return fetchImage(context.WithoutCancel(ctx), source), nil
	})
	return v.(externalImage)
}

func cacheExternalImage(source string, img externalImage) {
	externalImages.Lock()
	defer externalImages.Unlock()
	if old, ok := externalImages.entries[source]; ok {
		externalImages.bytes -= len(old.data)
		delete(externalImages.entries, source)
	}
	for k, e := range externalImages.entries {
		if len(externalImages.entries) < externalImageCacheMax && externalImages.bytes+len(img.data) <= externalImageCacheBudget {
			break
		}
		externalImages.bytes -= len(e.data)
		delete(externalImages.entries, k)
	}
	externalImages.entries[source] = img
	externalImages.bytes += len(img.data)
}

var externalImageClient = requestsecurity.NewSafeHTTPClient(10*time.Second, 3, nil)

func (s *Server) fetchExternalImage(w http.ResponseWriter, r *http.Request, source string) {
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeError(w, http.StatusNotFound, "Image not available")
		return
	}
	externalImages.Lock()
	cached, ok := externalImages.entries[source]
	externalImages.Unlock()
	fresh := ok && ((cached.data != nil && time.Since(cached.at) < externalImageCacheTTL) ||
		(cached.data == nil && time.Since(cached.at) < externalImageFailTTL))
	if !fresh {
		cached = fetchImageShared(r.Context(), source)
		cacheExternalImage(source, cached)
	}
	if cached.data == nil {
		writeError(w, http.StatusNotFound, "Image not available")
		return
	}
	w.Header().Set("Content-Type", cached.contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(cached.data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(cached.data)
}

// passthroughRecorder forwards a successful response straight to the client
// but swallows error responses so the caller can fall back.
type passthroughRecorder struct {
	w       http.ResponseWriter
	status  int
	written bool
	header  http.Header
}

func newPassthroughRecorder(w http.ResponseWriter) *passthroughRecorder {
	return &passthroughRecorder{w: w, header: http.Header{}}
}

func (p *passthroughRecorder) Header() http.Header { return p.header }

func (p *passthroughRecorder) WriteHeader(status int) {
	if p.written {
		return
	}
	p.written = true
	p.status = status
	if status >= 400 {
		return
	}
	for k, v := range p.header {
		p.w.Header()[k] = v
	}
	p.w.WriteHeader(status)
}

func (p *passthroughRecorder) Write(b []byte) (int, error) {
	if !p.written {
		p.WriteHeader(http.StatusOK)
	}
	if p.status >= 400 {
		return len(b), nil
	}
	return p.w.Write(b)
}

// fetchImage downloads an external image; failures yield a negative entry.
func fetchImage(ctx context.Context, source string) externalImage {
	failed := externalImage{at: time.Now()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return failed
	}
	// Some logo hosts refuse Go's default User-Agent (Wikimedia, where many
	// IPTV playlists take their channel logos, answers 403).
	req.Header.Set("User-Agent", "mediastorm/1.0")
	resp, err := externalImageClient.Do(req)
	if err != nil {
		return failed
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, externalImageFetchMaxBytes+1))
	contentType := resp.Header.Get("Content-Type")
	if err != nil || resp.StatusCode != http.StatusOK || len(data) > externalImageFetchMaxBytes || !strings.HasPrefix(contentType, "image/") {
		return failed
	}
	if len(data) > externalImageMaxBytes {
		if data, contentType = shrinkExternalImage(data); data == nil {
			return failed
		}
	}
	img := externalImage{at: time.Now(), contentType: contentType, data: data}
	img.width, img.height = imageSize(data)
	rememberLogoSize(source, img.width, img.height, false)
	return img
}

// shrinkExternalImage scales a picture too large to cache and send as it is
// down to externalImageShrinkSide (its EXIF orientation applied) and encodes
// it as JPEG, or as PNG when it has transparency; nil when it cannot (unknown
// format, too many pixels, still too large). Only JPEG, PNG and GIF are
// decoded: a WebP's header may name another size than the frame it holds.
func shrinkExternalImage(data []byte) (out []byte, contentType string) {
	defer func() {
		if recover() != nil { // a malformed picture: refused, the server goes on
			out, contentType = nil, ""
		}
	}()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > externalImageMaxPixels {
		return nil, ""
	}
	if format != "jpeg" && format != "png" && format != "gif" {
		return nil, ""
	}
	externalImageShrinks <- struct{}{}
	defer func() { <-externalImageShrinks }()
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || src.Bounds().Dx() != cfg.Width || src.Bounds().Dy() != cfg.Height {
		return nil, ""
	}
	bounds := src.Bounds()
	scale := min(1, float64(externalImageShrinkSide)/float64(bounds.Dx()), float64(externalImageShrinkSide)/float64(bounds.Dy()))
	width, height := max(1, int(math.Round(float64(bounds.Dx())*scale))), max(1, int(math.Round(float64(bounds.Dy())*scale)))
	scaled := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), src, bounds, xdraw.Src, nil)
	dst := orientImage(scaled, jpegOrientation(data))
	var buf bytes.Buffer
	contentType = "image/jpeg"
	if o, ok := src.(interface{ Opaque() bool }); ok && !o.Opaque() {
		// JPEG has no alpha: transparent parts (a logo's background) would
		// turn black.
		contentType = "image/png"
		err = png.Encode(&buf, dst)
	} else {
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85})
	}
	if err != nil || buf.Len() > externalImageMaxBytes {
		return nil, ""
	}
	return buf.Bytes(), contentType
}

// jpegOrientation is a JPEG's EXIF orientation (1-8; 1 when it has none):
// browsers and image loaders honour it, Go's decoder does not.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // image data: no EXIF before it
			return 1
		}
		size := int(data[i+2])<<8 | int(data[i+3])
		if size < 2 || i+2+size > len(data) {
			return 1
		}
		if seg := data[i+4 : i+2+size]; marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

// exifOrientation reads the orientation tag (0x0112) of IFD0 in an EXIF
// TIFF block.
func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(tiff[4:8]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 1
	}
	for k, n := 0, int(order.Uint16(tiff[ifd:])); k < n; k++ {
		entry := ifd + 2 + 12*k
		if entry+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[entry:]) == 0x0112 {
			if v := int(order.Uint16(tiff[entry+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orientImage turns a picture as its EXIF orientation says (2-8 mirror
// and/or rotate it).
func orientImage(src *image.RGBA, orientation int) *image.RGBA {
	if orientation <= 1 || orientation > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			copy(dst.Pix[dst.PixOffset(dx, dy):dst.PixOffset(dx, dy)+4], src.Pix[src.PixOffset(x, y):src.PixOffset(x, y)+4])
		}
	}
	return dst
}
