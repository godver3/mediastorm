package handlers

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"novastream/models"
)

//go:embed web_templates/*.html
var webTemplates embed.FS

// WebAppHandler serves a static single-page web app from disk.
type WebAppHandler struct {
	root     string
	prefix   string
	Branding *SettingsHandler
	// IndexPathRewrite, when set, rewrites quoted absolute URLs starting with
	// From to To in index.html. Used to serve the Expo export (built for /watch)
	// at /watch-legacy.
	IndexPathRewrite *PathRewrite
}

// PathRewrite replaces one absolute URL prefix with another (e.g. "/watch/" -> "/watch-legacy/").
type PathRewrite struct {
	From string
	To   string
}

// NewWebAppHandler creates a handler for a static SPA hosted under prefix.
func NewWebAppHandler(root, prefix string) *WebAppHandler {
	cleanRoot := filepath.Clean(root)
	if absRoot, err := filepath.Abs(cleanRoot); err == nil {
		cleanRoot = absRoot
	}
	return &WebAppHandler{
		root:   cleanRoot,
		prefix: "/" + strings.Trim(prefix, "/"),
	}
}

// ResolveWebAppDir returns the directory of the web client built from web/
// (served at /watch). STRMR_WEB_APP_DIR wins, so packaged deployments can place
// the build anywhere; otherwise the first existing web/dist relative to the
// working directory (repo root or backend/).
func ResolveWebAppDir() string {
	if dir := strings.TrimSpace(os.Getenv("STRMR_WEB_APP_DIR")); dir != "" {
		return dir
	}
	if dir := firstExistingDir(filepath.Join("web", "dist"), filepath.Join("..", "web", "dist")); dir != "" {
		return dir
	}
	return filepath.Join("..", "web", "dist")
}

// ResolveLegacyWebAppDir returns the retired Expo web export, served at
// /watch-legacy for local comparison only: STRMR_WEB_LEGACY_DIR, else a local
// frontend/dist checkout. "" means none (the image doesn't ship it) and
// /watch-legacy answers 404.
func ResolveLegacyWebAppDir() string {
	if dir := strings.TrimSpace(os.Getenv("STRMR_WEB_LEGACY_DIR")); dir != "" {
		return dir
	}
	return firstExistingDir(filepath.Join("frontend", "dist"), filepath.Join("..", "frontend", "dist"))
}

func firstExistingDir(candidates ...string) string {
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// WebAppRedirectHandler permanently redirects an old app prefix (/watch-next)
// to the same path, query included, under a new one (/watch). The Location is
// relative so it stays correct whether or not the request came in under the
// server base path (utils.BasePathHandler strips it before routing).
type WebAppRedirectHandler struct {
	from string
	to   string
}

// NewWebAppRedirectHandler redirects from -> to (both app prefixes, e.g. "/watch-next", "/watch").
func NewWebAppRedirectHandler(from, to string) *WebAppRedirectHandler {
	return &WebAppRedirectHandler{from: "/" + strings.Trim(from, "/"), to: "/" + strings.Trim(to, "/")}
}

func (h *WebAppRedirectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	rest, ok := strings.CutPrefix(escaped, h.from)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		http.NotFound(w, r)
		return
	}
	// From the request's directory back up to the root: one "../" per directory
	// level below it ("/watch-next" has none, "/watch-next/a/b" has two).
	up := strings.Repeat("../", strings.Count(escaped, "/")-1)
	target := up + strings.TrimPrefix(h.to, "/") + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	if !strings.HasPrefix(target, ".") {
		target = "./" + target
	}
	w.Header().Set("Location", target)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusMovedPermanently)
}

func (h *WebAppHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	indexPath := filepath.Join(h.root, "index.html")
	if info, err := os.Stat(indexPath); err != nil || info.IsDir() {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "web app bundle not found at %s; run `npm run build` in web/ or set STRMR_WEB_APP_DIR", h.root)
		return
	}

	requestPath := strings.TrimPrefix(r.URL.Path, h.prefix)
	requestPath = strings.TrimPrefix(requestPath, "/")
	if requestPath == "" {
		h.serveIndex(w, r, indexPath)
		return
	}

	cleanPath := path.Clean("/" + requestPath)
	if cleanPath == "/" {
		h.serveIndex(w, r, indexPath)
		return
	}
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	filePath := filepath.Join(h.root, filepath.FromSlash(cleanPath))

	if !strings.HasPrefix(filePath, h.root+string(os.PathSeparator)) && filePath != h.root {
		http.NotFound(w, r)
		return
	}

	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		if cleanPath == "manifest.json" {
			h.serveManifest(w, r, filePath)
			return
		}
		serveWebAppAsset(w, r, filePath)
		return
	}

	if hasFileExtension(cleanPath) {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r, indexPath)
}

func (h *WebAppHandler) serveIndex(w http.ResponseWriter, r *http.Request, indexPath string) {
	if h.IndexPathRewrite == nil {
		serveWebAppIndex(w, r, indexPath)
		return
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	from, to := h.IndexPathRewrite.From, h.IndexPathRewrite.To
	html := strings.NewReplacer(`"`+from, `"`+to, `'`+from, `'`+to).Replace(string(data))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, strings.NewReader(html))
}

func serveWebAppIndex(w http.ResponseWriter, r *http.Request, indexPath string) {
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, indexPath)
}

func serveWebAppAsset(w http.ResponseWriter, r *http.Request, filePath string) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, filePath)
}

func hasFileExtension(p string) bool {
	base := path.Base(p)
	return strings.Contains(base, ".")
}

// WebPlaybackUserService exposes the profile methods needed by the standalone
// web player.
type WebPlaybackUserService interface {
	List() []models.User
}

// WebPlaybackSessionValidator validates a session token. It is satisfied by
// *sessions.Service and lets the handler gate the playback page behind auth.
type WebPlaybackSessionValidator interface {
	Validate(token string) (models.Session, error)
}

// WebPlaybackHandler renders the dedicated browser player page.
type WebPlaybackHandler struct {
	users          WebPlaybackUserService
	sessions       WebPlaybackSessionValidator
	serverBasePath string
	template       *template.Template
}

type WebPlaybackPageData struct {
	Users          []models.User
	ServerBasePath string
}

// NewWebPlaybackHandler creates the standalone web playback handler. The
// playback page is not public: requests must carry a valid session token
// (?token=, Authorization: Bearer, X-PIN, or the admin cookie) or they are
// redirected to the web app login.
func NewWebPlaybackHandler(users WebPlaybackUserService, sessions WebPlaybackSessionValidator, serverBasePath string) *WebPlaybackHandler {
	serverBasePath = "/" + strings.Trim(serverBasePath, "/")
	if serverBasePath == "/" {
		serverBasePath = ""
	}

	funcMap := template.FuncMap{
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}

	tmpl := template.Must(template.New("web-playback").Funcs(funcMap).ParseFS(webTemplates, "web_templates/playback.html"))
	return &WebPlaybackHandler{
		users:          users,
		sessions:       sessions,
		serverBasePath: serverBasePath,
		template:       tmpl,
	}
}

func (h *WebPlaybackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session := h.resolveSession(r)
	if session == nil {
		// Not authenticated — bounce to the web app so the user can log in. A
		// returnTo inside one of the web apps lands there instead (the app's own
		// guard sends the user through sign-in and back to that page).
		target := h.serverBasePath + "/watch"
		if ret := h.signedOutReturnPath(r.URL.Query().Get("returnTo")); ret != "" {
			target = ret
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data := WebPlaybackPageData{
		Users:          h.scopedUsers(session, r.URL.Query().Get("profileId")),
		ServerBasePath: h.serverBasePath,
	}
	if err := h.template.ExecuteTemplate(w, "playback", data); err != nil {
		http.Error(w, "web playback template error", http.StatusInternalServerError)
	}
}

// resolveSession validates the request's session token. Browser navigations to
// the handoff page carry the token as ?token=; API-style callers may use the
// Authorization/X-PIN headers; admins reach it via the admin cookie.
func (h *WebPlaybackHandler) resolveSession(r *http.Request) *models.Session {
	if h.sessions == nil {
		return nil
	}

	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		}
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-PIN"))
	}
	if token == "" {
		if cookie, err := r.Cookie(adminSessionCookieName); err == nil {
			token = cookie.Value
		}
	}
	if token == "" {
		return nil
	}

	session, err := h.sessions.Validate(token)
	if err != nil {
		return nil
	}
	return &session
}

func (h *WebPlaybackHandler) scopedUsers(session *models.Session, requestedProfileID string) []models.User {
	if h.users == nil || session == nil {
		return []models.User{}
	}

	all := h.users.List()
	if session.IsMaster {
		return all
	}

	scoped := make([]models.User, 0, len(all))
	for _, user := range all {
		if user.AccountID == session.AccountID &&
			(session.Scope != models.SessionScopeStream || user.ID == strings.TrimSpace(requestedProfileID)) {
			scoped = append(scoped, user)
		}
	}
	return scoped
}

// webAppReturnRoots are the app roots, under the server base path, a signed-out
// playback request may be sent back to: the web client (/watch) and the retired
// Expo export when a local copy is served (/watch-legacy).
var webAppReturnRoots = []string{"/watch", "/watch-legacy"}

// signedOutReturnPath returns returnTo when it is a same-origin path inside one
// of the web apps (never the player itself), else "". Absolute and
// scheme-relative URLs are rejected so the redirect can't leave the server.
func (h *WebPlaybackHandler) signedOutReturnPath(returnTo string) string {
	raw := strings.TrimSpace(returnTo)
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	cleaned := path.Clean(u.Path)
	rest, ok := strings.CutPrefix(cleaned, h.serverBasePath)
	if !ok || strings.HasSuffix(rest, "/playback.html") {
		return ""
	}
	for _, root := range webAppReturnRoots {
		if rest == root || strings.HasPrefix(rest, root+"/") {
			if u.RawQuery != "" {
				return cleaned + "?" + u.RawQuery
			}
			return cleaned
		}
	}
	return ""
}
