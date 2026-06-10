package handlers

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"novastream/models"
)

//go:embed web_templates/*.html
var webTemplates embed.FS

// WebAppHandler serves a static single-page web app from disk.
type WebAppHandler struct {
	root   string
	prefix string
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

// ResolveWebAppDir returns the first usable web app directory. The environment
// override is intentionally explicit so packaged deployments can place the
// exported bundle anywhere.
func ResolveWebAppDir() string {
	if dir := strings.TrimSpace(os.Getenv("STRMR_WEB_APP_DIR")); dir != "" {
		return dir
	}

	candidates := []string{
		filepath.Join("frontend", "dist"),
		filepath.Join("..", "frontend", "dist"),
		filepath.Join("frontend", "web-build"),
		filepath.Join("..", "frontend", "web-build"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join("..", "frontend", "dist")
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
		fmt.Fprintf(w, "web app bundle not found at %s; run `npm run web:export` in frontend or set STRMR_WEB_APP_DIR", h.root)
		return
	}

	requestPath := strings.TrimPrefix(r.URL.Path, h.prefix)
	requestPath = strings.TrimPrefix(requestPath, "/")
	if requestPath == "" {
		serveWebAppIndex(w, r, indexPath)
		return
	}

	cleanPath := path.Clean("/" + requestPath)
	if cleanPath == "/" {
		serveWebAppIndex(w, r, indexPath)
		return
	}
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	filePath := filepath.Join(h.root, filepath.FromSlash(cleanPath))

	if !strings.HasPrefix(filePath, h.root+string(os.PathSeparator)) && filePath != h.root {
		http.NotFound(w, r)
		return
	}

	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		serveWebAppAsset(w, r, filePath)
		return
	}

	if hasFileExtension(cleanPath) {
		http.NotFound(w, r)
		return
	}
	serveWebAppIndex(w, r, indexPath)
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
		// Not authenticated — bounce to the web app so the user can log in.
		http.Redirect(w, r, h.serverBasePath+"/watch", http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data := WebPlaybackPageData{
		Users:          h.scopedUsers(session),
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

func (h *WebPlaybackHandler) scopedUsers(session *models.Session) []models.User {
	if h.users == nil || session == nil {
		return []models.User{}
	}

	all := h.users.List()
	if session.IsMaster {
		return all
	}

	scoped := make([]models.User, 0, len(all))
	for _, user := range all {
		if user.AccountID == session.AccountID {
			scoped = append(scoped, user)
		}
	}
	return scoped
}
