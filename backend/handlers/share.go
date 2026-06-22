package handlers

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"novastream/internal/auth"
	"novastream/models"
)

// SharePlaybackSessionTTL is how long the minted stream-scoped session stays valid
// after a share link is opened — long enough to watch a movie with pauses.
const SharePlaybackSessionTTL = 6 * time.Hour

// shareAllowedParams is the whitelist of playback parameters a share link may carry
// through to the web player. Anything not listed is dropped when creating a share.
var shareAllowedParams = map[string]bool{
	"sourcePath": true, "movie": true,
	"profileId": true, "profileName": true, "userId": true,
	"mediaType": true, "title": true, "seriesTitle": true, "displayName": true,
	"year": true, "seasonNumber": true, "episodeNumber": true, "episodeName": true,
	"imdbId": true, "tvdbId": true, "tmdbId": true, "titleId": true,
	"headerImage": true, "posterUrl": true,
	"dv": true, "hdr10": true, "dvProfile": true, "forceAAC": true,
	"preselectedAudioTrack": true, "preselectedSubtitleTrack": true,
	"startOffset": true, "startPercent": true, "actualStartOffset": true, "durationHint": true,
	"tvgId": true, "liveSourceUrl": true,
}

// ShareSessionService mints the short-lived stream-scoped session granted to a
// share-link recipient. Satisfied by *sessions.Service.
type ShareSessionService interface {
	CreateScoped(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration, scope string) (models.Session, error)
}

// ShareHandler creates and consumes one-time shareable playback links.
type ShareHandler struct {
	store          *ShareStore
	sessions       ShareSessionService
	serverBasePath string
}

// NewShareHandler creates a ShareHandler.
func NewShareHandler(store *ShareStore, sessions ShareSessionService, serverBasePath string) *ShareHandler {
	serverBasePath = "/" + strings.Trim(serverBasePath, "/")
	if serverBasePath == "/" {
		serverBasePath = ""
	}
	return &ShareHandler{store: store, sessions: sessions, serverBasePath: serverBasePath}
}

type shareCreateResponse struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// Create generates a one-time share link from captured playback parameters. The
// authenticated account is read from the request context, so this handler works
// behind both the session-token middleware (web player) and the admin/account
// cookie auth.
func (h *ShareHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	accountID := auth.GetAccountID(r)
	if accountID == "" {
		writeShareJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeShareJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := make(map[string]string, len(body))
	for key, value := range body {
		value = strings.TrimSpace(value)
		if value == "" || !shareAllowedParams[key] {
			continue
		}
		params[key] = value
	}

	if params["sourcePath"] == "" && params["movie"] == "" {
		writeShareJSONError(w, http.StatusBadRequest, "no playback source to share")
		return
	}

	rec, err := h.store.Create(accountID, auth.IsMaster(r), params)
	if err != nil {
		writeShareJSONError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(shareCreateResponse{
		URL:       h.serverBasePath + "/share/" + rec.Token,
		Token:     rec.Token,
		ExpiresAt: rec.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// Open consumes a one-time share link (GET /share/{token}). It mints a short-lived
// stream-scoped session and redirects into the player-only web view. Already-used,
// expired, or unknown links render a friendly 410 page.
func (h *ShareHandler) Open(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(shareTokenFromPath(r.URL.Path))

	rec, ok := h.store.Consume(token)
	if !ok {
		renderShareUnavailable(w, h.serverBasePath)
		return
	}

	session, err := h.sessions.CreateScoped(
		rec.AccountID, rec.IsMaster,
		r.UserAgent(), clientIPForShare(r),
		SharePlaybackSessionTTL, models.SessionScopeStream,
	)
	if err != nil {
		http.Error(w, "failed to open shared playback", http.StatusInternalServerError)
		return
	}

	values := url.Values{}
	for key, value := range rec.Params {
		values.Set(key, value)
	}
	values.Set("token", session.Token)
	values.Set("shareMode", "1")

	target := h.serverBasePath + "/watch/playback.html?" + values.Encode()
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// shareTokenFromPath extracts the token from a /share/{token} path without
// depending on the mux vars (so it also works in lightweight tests).
func shareTokenFromPath(path string) string {
	idx := strings.LastIndex(path, "/share/")
	if idx < 0 {
		return ""
	}
	return strings.Trim(path[idx+len("/share/"):], "/")
}

func clientIPForShare(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if comma := strings.IndexByte(fwd, ','); comma >= 0 {
			return strings.TrimSpace(fwd[:comma])
		}
		return fwd
	}
	if host, _, found := strings.Cut(r.RemoteAddr, ":"); found {
		return host
	}
	return r.RemoteAddr
}

func writeShareJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func renderShareUnavailable(w http.ResponseWriter, basePath string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	watchURL := html.EscapeString(basePath + "/watch")
	w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>Link unavailable</title><style>` +
		`html,body{height:100%;margin:0}body{display:flex;align-items:center;justify-content:center;` +
		`background:#0b0d12;color:#e7eaf0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}` +
		`.card{max-width:420px;padding:32px;text-align:center}h1{font-size:20px;margin:0 0 12px}` +
		`p{opacity:.7;line-height:1.5;margin:0 0 20px}a{color:#7aa2ff;text-decoration:none}` +
		`</style></head><body><div class="card"><h1>This share link is no longer available</h1>` +
		`<p>One-time playback links can only be opened once, and expire after 24 hours. ` +
		`Ask whoever shared it to send a new link.</p>` +
		`<a href="` + watchURL + `">Go to the app</a></div></body></html>`))
}
