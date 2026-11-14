package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"novastream/internal/auth"
	"novastream/internal/requestsecurity"
	"novastream/models"
	metadatapkg "novastream/services/metadata"
)

var stremioDirectoryManifestPattern = regexp.MustCompile(`"manifestUrl"\s*:\s*"([^"]+)"`)

const (
	stremioShelfCatalogTTL      = 10 * time.Minute
	stremioShelfMaxCatalogItems = 500
	stremioShelfMaxResponseSize = 4 << 20
)

type stremioManifestCatalogResponse struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	PageSize int    `json:"pageSize,omitempty"`
}

type stremioManifestIngestionResponse struct {
	ID          string                           `json:"id"`
	Name        string                           `json:"name"`
	Version     string                           `json:"version,omitempty"`
	ManifestURL string                           `json:"manifestUrl"`
	Catalogs    []stremioManifestCatalogResponse `json:"catalogs"`
}

func newStremioShelfHTTPClient(policyProvider func() requestsecurity.RestrictedHostPolicy) *http.Client {
	return requestsecurity.NewSafeHTTPClientWithPolicyProvider(15*time.Second, 5, policyProvider)
}

func (h *MetadataHandler) stremioShelfClient() *http.Client {
	if h.stremioHTTPClient != nil {
		return h.stremioHTTPClient
	}
	h.stremioHTTPClient = newStremioShelfHTTPClient(h.stremioShelfHostPolicy)
	return h.stremioHTTPClient
}

// stremioShelfHostPolicy permits private-network add-ons (for example a
// Docker-hosted AIOMetadata) only when the master admin configured them: the
// origin of a global Stremio home shelf, or any configured provider/private
// media origin. Catalog requests carry a client-supplied manifestUrl, so
// profile-level shelves are deliberately not trusted here.
func (h *MetadataHandler) stremioShelfHostPolicy() requestsecurity.RestrictedHostPolicy {
	if h.CfgManager == nil {
		return nil
	}
	base := configuredProviderHostPolicy(h.CfgManager)
	allowed := make(map[string]struct{})
	if settings, err := h.CfgManager.Load(); err == nil {
		for _, shelf := range settings.HomeShelves.Shelves {
			if !strings.EqualFold(strings.TrimSpace(shelf.Type), "stremio") {
				continue
			}
			manifestURL, _, err := normalizeStremioManifestInput(shelf.AddonManifestURL)
			if err != nil {
				continue
			}
			if key, ok := providerOriginKey(manifestURL); ok {
				allowed[key] = struct{}{}
			}
		}
	}
	return func(hostname, port string) bool {
		if _, ok := allowed[privateMediaEndpointKey(hostname, port)]; ok {
			return true
		}
		return base(hostname, port)
	}
}

// stremioManifestIngestionClient returns the client used while an admin adds a
// new Stremio shelf. The shelf is not saved yet, so a master admin's typed
// manifest origin is trusted for this request the same way a typed indexer or
// scraper URL is. Origins discovered via add-on directory pages are not.
func (h *MetadataHandler) stremioManifestIngestionClient(r *http.Request, manifestURL string) *http.Client {
	key, ok := providerOriginKey(manifestURL)
	if !ok || !auth.IsMaster(r) {
		return h.stremioShelfClient()
	}
	return newStremioShelfHTTPClient(func() requestsecurity.RestrictedHostPolicy {
		base := h.stremioShelfHostPolicy()
		return func(hostname, port string) bool {
			if privateMediaEndpointKey(hostname, port) == key {
				return true
			}
			return base != nil && base(hostname, port)
		}
	})
}

// normalizeStremioManifestInput accepts a manifest URL, an addon base URL, a
// stremio:// install URL, or Stremio Web's addon URL and returns the canonical
// HTTP(S) manifest URL plus its resource base URL.
func normalizeStremioManifestInput(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", fmt.Errorf("manifest URL is required")
	}
	if strings.HasPrefix(strings.ToLower(value), "stremio://") {
		value = "https://" + value[len("stremio://"):]
	}
	if parsedWeb, err := url.Parse(value); err == nil && strings.EqualFold(parsedWeb.Hostname(), "web.stremio.com") {
		fragment := strings.TrimPrefix(parsedWeb.Fragment, "/")
		if idx := strings.Index(fragment, "?"); idx >= 0 {
			if addonURL := strings.TrimSpace(parseQueryValue(fragment[idx+1:], "addon")); addonURL != "" {
				value = addonURL
			}
		}
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", "", fmt.Errorf("invalid Stremio manifest URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("Stremio manifest URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", "", fmt.Errorf("Stremio manifest URL must not contain embedded credentials")
	}
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/manifest.json") {
		path += "/manifest.json"
	}
	parsed.Path = path
	manifestURL := parsed.String()

	base := *parsed
	base.Path = strings.TrimSuffix(parsed.Path, "/manifest.json")
	base.RawQuery = ""
	base.ForceQuery = false
	return manifestURL, strings.TrimRight(base.String(), "/"), nil
}

func parseQueryValue(rawQuery, name string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	return values.Get(name)
}

func stremioAddonDirectoryPageURL(raw string) (string, bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", false, nil
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "stremio-addons.net" && host != "www.stremio-addons.net" {
		return "", false, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false, fmt.Errorf("Stremio add-on page URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", false, fmt.Errorf("Stremio add-on page URL must not contain embedded credentials")
	}
	if !strings.HasPrefix(strings.TrimRight(parsed.Path, "/"), "/addons/") {
		return "", false, nil
	}
	parsed.Fragment = ""
	return parsed.String(), true, nil
}

func extractStremioDirectoryManifestURL(body []byte) (string, error) {
	// Next.js serializes the page data inside script tags, where JSON quotes can
	// be escaped. Normalize those quotes before locating the manifestUrl field.
	normalized := strings.ReplaceAll(string(body), `\"`, `"`)
	match := stremioDirectoryManifestPattern.FindStringSubmatch(normalized)
	if len(match) != 2 {
		return "", fmt.Errorf("add-on page does not expose a manifest URL")
	}
	value := strings.TrimSpace(match[1])
	value = strings.ReplaceAll(value, `\u0026`, "&")
	if value == "" {
		return "", fmt.Errorf("add-on page exposes an empty manifest URL")
	}
	return value, nil
}

func resolveStremioDirectoryManifestURL(ctx context.Context, client *http.Client, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", liveStreamUserAgent)
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("directory page returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, stremioShelfMaxResponseSize+1))
	if err != nil {
		return "", err
	}
	if len(body) > stremioShelfMaxResponseSize {
		return "", fmt.Errorf("directory page exceeds %d bytes", stremioShelfMaxResponseSize)
	}
	return extractStremioDirectoryManifestURL(body)
}

func (h *MetadataHandler) loadStremioManifest(ctx context.Context, client *http.Client, rawURL string) (*stremioManifest, string, string, error) {
	if pageURL, isDirectoryPage, err := stremioAddonDirectoryPageURL(rawURL); err != nil {
		return nil, "", "", err
	} else if isDirectoryPage {
		// A directory page names a third-party manifest, so only the shared
		// admin-configured policy applies to it.
		client = h.stremioShelfClient()
		rawURL, err = resolveStremioDirectoryManifestURL(ctx, client, pageURL)
		if err != nil {
			return nil, "", "", fmt.Errorf("resolve Stremio add-on page: %w", err)
		}
	}
	manifestURL, baseURL, err := normalizeStremioManifestInput(rawURL)
	if err != nil {
		return nil, "", "", err
	}
	var manifest stremioManifest
	if err := getStremioShelfJSON(ctx, client, manifestURL, &manifest); err != nil {
		if errors.Is(err, requestsecurity.ErrRestrictedOutboundAddress) {
			return nil, "", "", fmt.Errorf("fetch Stremio manifest: %w (private add-ons must be added by the server admin, or listed in Allowed Private Media Origins)", err)
		}
		return nil, "", "", fmt.Errorf("fetch Stremio manifest: %w", err)
	}
	if len(manifest.Catalogs) == 0 {
		return nil, "", "", fmt.Errorf("Stremio manifest has no catalogs")
	}
	return &manifest, manifestURL, baseURL, nil
}

// StremioManifest discovers the movie and series catalogs that can be ingested
// as independent home shelves.
func (h *MetadataHandler) StremioManifest(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	client := h.stremioShelfClient()
	if _, isDirectoryPage, directoryErr := stremioAddonDirectoryPageURL(rawURL); directoryErr != nil {
		writeJSONError(w, directoryErr.Error(), http.StatusBadRequest)
		return
	} else if !isDirectoryPage {
		typedManifestURL, _, err := normalizeStremioManifestInput(rawURL)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		client = h.stremioManifestIngestionClient(r, typedManifestURL)
	}
	manifest, manifestURL, _, err := h.loadStremioManifest(r.Context(), client, rawURL)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	catalogs := make([]stremioManifestCatalogResponse, 0, len(manifest.Catalogs))
	for _, catalog := range manifest.Catalogs {
		mediaType := normalizeStremioCatalogType(catalog.Type)
		if mediaType == "" || strings.TrimSpace(catalog.ID) == "" {
			continue
		}
		name := strings.TrimSpace(catalog.Name)
		if name == "" {
			name = strings.TrimSpace(catalog.ID)
		}
		catalogs = append(catalogs, stremioManifestCatalogResponse{
			Type:     mediaType,
			ID:       strings.TrimSpace(catalog.ID),
			Name:     name,
			PageSize: catalog.PageSize,
		})
	}
	if len(catalogs) == 0 {
		writeJSONError(w, "Stremio manifest has no movie or series catalogs", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = strings.TrimSpace(manifest.ID)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = h.encodeMetadata(w, r, stremioManifestIngestionResponse{
		ID:          strings.TrimSpace(manifest.ID),
		Name:        name,
		Version:     strings.TrimSpace(manifest.Version),
		ManifestURL: manifestURL,
		Catalogs:    catalogs,
	})
}

// StremioList loads one advertised add-on catalog and feeds its identities into
// the shared curated-list enrichment/filtering pipeline.
func (h *MetadataHandler) StremioList(w http.ResponseWriter, r *http.Request) {
	rawManifestURL := strings.TrimSpace(r.URL.Query().Get("manifestUrl"))
	catalogType := normalizeStremioCatalogType(r.URL.Query().Get("catalogType"))
	catalogID := strings.TrimSpace(r.URL.Query().Get("catalogId"))
	if rawManifestURL == "" || catalogType == "" || catalogID == "" {
		writeJSONError(w, "manifestUrl, catalogType, and catalogId are required", http.StatusBadRequest)
		return
	}
	manifestURL, _, err := normalizeStremioManifestInput(rawManifestURL)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Progressive home requests read only enough catalog pages to fill the row.
	// Full discovery and background completion retain the complete catalog path.
	if _, ok := h.serviceForUser(strings.TrimSpace(r.URL.Query().Get("userId"))).(shelfCardsService); ok &&
		r.URL.Query().Get("shelfPhase") == "cards" && progressiveShelfRequest(r) {
		response, err := h.progressiveStremioShelf(r, manifestURL, catalogType, catalogID)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = h.encodeMetadata(w, r, response)
		return
	}

	metas, err := h.loadStremioShelfCatalog(r.Context(), manifestURL, catalogType, catalogID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	curated := make([]metadatapkg.CuratedItem, 0, len(metas))
	for _, meta := range metas {
		item := curatedItemFromStremioMeta(meta, catalogType)
		if item.Title == "" && item.IMDBID == "" && item.TMDBID == 0 {
			continue
		}
		curated = append(curated, item)
	}
	if len(curated) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = h.encodeMetadata(w, r, CustomListResponse{Items: []models.TrendingItem{}, Total: 0})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideUnreleased")), "true")
	hideWatched := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideWatched")), "true")
	limit, offset := parseLimitOffset(r)
	label := strings.TrimSpace(r.URL.Query().Get("name"))
	if label == "" {
		label = catalogID
	}
	response := h.buildShelfFromCurated(w, r, curated, label, userID, hideUnreleased, hideWatched, limit, offset)
	if response == nil {
		return
	}
	_ = h.encodeMetadata(w, r, response)
}

func getStremioShelfJSON(ctx context.Context, client *http.Client, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", liveStreamUserAgent)
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, stremioShelfMaxResponseSize+1))
	if err != nil {
		return err
	}
	if len(body) > stremioShelfMaxResponseSize {
		return fmt.Errorf("response exceeds %d bytes", stremioShelfMaxResponseSize)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	return nil
}

func normalizeStremioCatalogType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie", "movies":
		return "movie"
	case "series", "show", "shows", "tv":
		return "series"
	default:
		return ""
	}
}

func curatedItemFromStremioMeta(meta stremioMeta, fallbackType string) metadatapkg.CuratedItem {
	mediaType := normalizeStremioCatalogType(meta.Type)
	if mediaType == "" {
		mediaType = fallbackType
	}
	item := metadatapkg.CuratedItem{
		Title:     strings.TrimSpace(meta.Name),
		PosterURL: meta.Poster, BackdropURL: meta.Background, Overview: meta.Description, Genres: meta.Genres,
		Year:      stremioReleaseYear(meta.ReleaseInfo),
		MediaType: mediaType,
		// Add-on catalogs often ship curated artwork (e.g. rating posters);
		// enrichment fills only what the catalog leaves out.
		CatalogMetadata: true,
		IMDBRating:      stremioIMDBRating(meta.IMDBRating),
	}
	id := strings.TrimSpace(meta.ID)
	if strings.HasPrefix(strings.ToLower(id), "tt") {
		item.IMDBID = id
	} else if strings.HasPrefix(strings.ToLower(id), "tmdb:") {
		parts := strings.Split(id, ":")
		if parsed, err := strconv.ParseInt(parts[len(parts)-1], 10, 64); err == nil && parsed > 0 {
			item.TMDBID = parsed
		}
	}
	return item
}

func stremioReleaseYear(value string) int {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r < '0' || r > '9' }) {
		if len(field) != 4 {
			continue
		}
		year, err := strconv.Atoi(field)
		if err == nil && year >= 1800 && year <= 2200 {
			return year
		}
	}
	return 0
}

// stremioIMDBRating reads a catalog imdbRating, which add-ons send as a
// string ("7.4") or a number. Values outside 0-10 are ignored.
func stremioIMDBRating(value interface{}) float64 {
	var rating float64
	switch v := value.(type) {
	case float64:
		rating = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		rating = parsed
	default:
		return 0
	}
	if rating <= 0 || rating > 10 {
		return 0
	}
	return rating
}
