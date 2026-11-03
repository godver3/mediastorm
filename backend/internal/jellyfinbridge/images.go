package jellyfinbridge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/requestsecurity"
	"novastream/models"
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
}

const (
	externalImageMaxBytes    = 2 << 20
	externalImageCacheTTL    = 6 * time.Hour
	externalImageFailTTL     = 10 * time.Minute // dead logo hosts are common in IPTV lists
	externalImageCacheMax    = 512
	externalImageCacheBudget = 64 << 20
)

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
		cached = fetchImage(r, source)
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
func fetchImage(r *http.Request, source string) externalImage {
	failed := externalImage{at: time.Now()}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, source, nil)
	if err != nil {
		return failed
	}
	resp, err := externalImageClient.Do(req)
	if err != nil {
		return failed
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, externalImageMaxBytes+1))
	contentType := resp.Header.Get("Content-Type")
	if err != nil || resp.StatusCode != http.StatusOK || len(data) > externalImageMaxBytes || !strings.HasPrefix(contentType, "image/") {
		return failed
	}
	return externalImage{at: time.Now(), contentType: contentType, data: data}
}
