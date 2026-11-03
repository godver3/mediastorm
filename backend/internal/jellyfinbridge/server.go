// Package jellyfinbridge makes mediastorm look like a Jellyfin server, so stock
// Jellyfin clients (jellyfin-web, Android TV, Swiftfin, Findroid, ...) can sign
// in with a mediastorm profile, browse, and play.
//
// It runs as a separate HTTP listener (default :8096). Every mediastorm profile
// is a Jellyfin user; libraries are virtual views over the profile's watchlist,
// progress, custom lists, trending titles and live TV. Playback reuses
// mediastorm's own pipeline (prequeue resolution → HLS sessions) by calling the
// existing HTTP handlers in-process.
package jellyfinbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
)

// ServerVersion is the Jellyfin version the bridge reports. Android TV refuses
// servers older than 10.11.0, jellyfin-web older than 10.10.0.
const ServerVersion = "10.11.11"

const productName = "Jellyfin Server"

// Server is the Jellyfin-compatible HTTP listener.
type Server struct {
	deps     Deps
	cfg      config.JellyfinBridgeSettings
	serverID string
	ids      *idStore
	prefs    *prefsStore
	// tracks remembers the audio and subtitle picked per profile and series
	// (audioChoice, subtitleChoice), for the next episode's defaults.
	tracks    *prefsStore
	plays     *playSessions
	releases  *releaseCache
	autoPicks *autoPicks
	views     *viewCache
	login     *loginLimiter
	qc        *quickConnect
	tiles     *tileCache
	live      liveSettings
	sockets   *socketHub
	msKey     []byte // media source ID key (see mediaSourceID)
	// copyFailures remembers, per profile, device and item, a stream copy
	// that failed to play, so the next attempt transcodes.
	copyFailures sync.Map
	// chapters holds the chapters of the stream last probed per profile
	// and item (chapterSet).
	chapters sync.Map
	// durations holds the length of the stream last played per profile and
	// item (knownDuration), for progress reports without a live session.
	durations sync.Map
	// stopped holds items stopped from Active Streams (profile|item -> time).
	stopped      sync.Map
	seriesCounts sync.Map // profile|series GUID -> seriesCount
	states       userStateCache
	proxies      []*net.IPNet

	httpSrv *http.Server
	udp     *discoveryResponder

	mu      sync.Mutex
	started bool
}

// New builds a bridge server. cfg.ServerID must already be set (see EnsureServerID).
func New(deps Deps, cfg config.JellyfinBridgeSettings) *Server {
	if cfg.Port <= 0 {
		cfg.Port = 8096
	}
	if strings.TrimSpace(cfg.ServerName) == "" {
		cfg.ServerName = "mediastorm"
	}
	if cfg.PlaybackInfoWaitSeconds <= 0 {
		cfg.PlaybackInfoWaitSeconds = 20
	}
	serverID := normalizeID(cfg.ServerID)
	if serverID == "" {
		serverID = deriveID("server", cfg.ServerName)
	}
	idPath := ""
	prefsPath := ""
	tracksPath := ""
	if deps.CacheDir != "" {
		idPath = filepath.Join(deps.CacheDir, "jellyfin_bridge", "item_ids.json")
		prefsPath = filepath.Join(deps.CacheDir, "jellyfin_bridge", "display_prefs.json")
		tracksPath = filepath.Join(deps.CacheDir, "jellyfin_bridge", "track_choices.json")
	}
	s := &Server{
		deps:      deps,
		cfg:       cfg,
		serverID:  serverID,
		ids:       newIDStore(idPath),
		prefs:     newPrefsStore(prefsPath),
		tracks:    newNamedPrefsStore(tracksPath, "track choices", maxTrackChoicesPerProfile),
		plays:     newPlaySessions(),
		releases:  newReleaseCache(),
		autoPicks: newAutoPicks(),
		views:     newViewCache(),
		login:     newLoginLimiter(),
		qc:        newQuickConnect(),
		tiles:     newTileCache(),
		sockets:   newSocketHub(),
		msKey:     randomBytes(32),
		states:    userStateCache{entries: map[string]userStateEntry{}},
	}
	for _, cidr := range cfg.TrustedProxies {
		cidr = strings.TrimSpace(cidr)
		if !strings.Contains(cidr, "/") {
			if ip := net.ParseIP(cidr); ip != nil && ip.To4() != nil {
				cidr += "/32"
			} else {
				cidr += "/128"
			}
		}
		if _, network, err := net.ParseCIDR(cidr); err == nil {
			s.proxies = append(s.proxies, network)
		} else {
			log.Printf("[jellyfin-bridge] ignoring invalid trusted proxy %q", cidr)
		}
	}
	return s
}

// EnsureServerID makes sure the bridge has a persisted server GUID, generating
// and saving one on first start so clients keep recognising the server.
//
// The ID lives in <cacheDir>/jellyfin_bridge/server_id, which nothing else
// writes; settings.json only mirrors it for display. (A settings page saved
// from a state loaded before the ID existed would otherwise erase it, and
// clients identify servers by ID: a new one means re-adding the server.)
func EnsureServerID(cfgManager *config.Manager, cacheDir string) (config.JellyfinBridgeSettings, error) {
	settings, err := cfgManager.Load()
	if err != nil {
		return config.JellyfinBridgeSettings{}, err
	}
	idPath := filepath.Join(cacheDir, "jellyfin_bridge", "server_id")
	id := ""
	if raw, err := os.ReadFile(idPath); err == nil {
		id = normalizeID(string(raw))
	}
	if id == "" {
		id = normalizeID(settings.JellyfinBridge.ServerID)
	}
	if id == "" {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return config.JellyfinBridgeSettings{}, err
		}
		id = hex.EncodeToString(buf)
	}
	if err := os.MkdirAll(filepath.Dir(idPath), 0o755); err == nil {
		if current, _ := os.ReadFile(idPath); normalizeID(string(current)) != id {
			if err := os.WriteFile(idPath, []byte(id+"\n"), 0o600); err != nil {
				log.Printf("[jellyfin-bridge] could not store server id: %v", err)
			}
		}
	}
	if settings.JellyfinBridge.ServerID != id {
		settings.JellyfinBridge.ServerID = id
		if err := cfgManager.Save(settings); err != nil {
			log.Printf("[jellyfin-bridge] could not mirror server id into settings: %v", err)
		}
	}
	return settings.JellyfinBridge, nil
}

// Port returns the configured listen port (after defaults).
func (s *Server) Port() int { return s.cfg.Port }

// Handler returns the bridge's root HTTP handler (exposed for tests).
func (s *Server) Handler() http.Handler {
	r := mux.NewRouter()
	r.StrictSlash(false)
	s.routes(r)
	r.NotFoundHandler = http.HandlerFunc(s.notFound)
	r.MethodNotAllowedHandler = http.HandlerFunc(s.notFound)
	return s.cors(s.accessLog(s.webClient(lowercasePath(r))))
}

// Start begins listening. It returns once the listener is bound; serving
// continues in the background. A bind failure is returned, not fatal.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// New started the stores' flush loops; a server that never ran is
		// discarded, so stop them here.
		s.ids.close()
		s.prefs.close()
		s.tracks.close()
		return fmt.Errorf("jellyfin bridge listen on %s: %w", addr, err)
	}
	s.httpSrv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // streams
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[jellyfin-bridge] server error: %v", err)
		}
	}()
	// A loopback-only listener sits behind a proxy; advertising it on the
	// LAN would point clients at an unreachable address.
	if s.cfg.UDPDiscovery && !isLoopbackHost(s.cfg.Host) {
		udp, err := startDiscovery(s)
		if err != nil {
			log.Printf("[jellyfin-bridge] UDP discovery disabled: %v", err)
		} else {
			s.udp = udp
		}
	}
	s.started = true
	log.Printf("[jellyfin-bridge] listening on %s (server id %s)", addr, s.serverID)
	return nil
}

// Shutdown stops the listener and flushes persisted state.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.udp != nil {
		s.udp.close()
		s.udp = nil
	}
	if s.httpSrv != nil {
		// Long-lived streams would otherwise hold the shared shutdown budget;
		// give in-flight requests a few seconds, then close hard.
		bridgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = s.httpSrv.Shutdown(bridgeCtx)
		cancel()
		if err != nil {
			_ = s.httpSrv.Close()
		}
	}
	// Hijacked WebSockets are invisible to http.Server.Shutdown.
	s.sockets.closeAll()
	s.plays.closeAll(s)
	s.ids.close()
	s.prefs.close()
	s.tracks.close()
	s.started = false
	return err
}

// lowercasePath makes routing case-insensitive: Jellyfin's ASP.NET routes are,
// and clients mix casings (/Items vs /items). Routes are registered lowercase.
func lowercasePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lower := strings.ToLower(r.URL.Path)
		if lower != r.URL.Path {
			r2 := r.Clone(r.Context())
			r2.URL.Path = lower
			r2.URL.RawPath = ""
			r = r2
		}
		// Collapse duplicate slashes some clients produce when joining base URLs.
		for strings.Contains(r.URL.Path, "//") {
			r.URL.Path = strings.ReplaceAll(r.URL.Path, "//", "/")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
		h.Set("Access-Control-Allow-Headers", "Accept, Accept-Language, Authorization, Cache-Control, Content-Type, Content-Length, Range, X-Emby-Authorization, X-Emby-Token, X-MediaBrowser-Token, X-Emby-Client, X-Emby-Device-Id, X-Emby-Device-Name, X-Emby-Client-Version, X-Requested-With")
		h.Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges, Content-Disposition")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth wraps handlers that need an authenticated principal.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `MediaBrowser realm="mediastorm"`)
			writeError(w, http.StatusUnauthorized, "Access token is invalid or expired.")
			return
		}
		next(w, withPrincipal(r, p))
	}
}

// optionalAuth attaches a principal when the request carries a valid token,
// but does not require one (images, anonymous-tolerant endpoints).
func (s *Server) optionalAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := s.authenticate(r); ok {
			r = withPrincipal(r, p)
		}
		next(w, r)
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	log.Printf("[jellyfin-bridge] unhandled %s %q", r.Method, r.URL.Path)
	writeError(w, http.StatusNotFound, "Not found")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[jellyfin-bridge] encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

func noContent(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// clientIP returns the caller address. X-Forwarded-For is honoured only when
// the connection comes from loopback or a configured trusted proxy (e.g. the
// Docker network a reverse proxy sits on); otherwise any client could pick
// its own address and dodge rate limits.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || s.trustedProxy(ip)) {
		// The rightmost hop is the one the local proxy appended; anything to
		// its left was supplied by the client.
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if fwd := strings.TrimSpace(hops[len(hops)-1]); net.ParseIP(fwd) != nil {
			return fwd
		}
	}
	return host
}

// userID returns the Jellyfin user GUID for a profile.
func (s *Server) userID(profile models.User) string {
	return s.ids.put(refUser, profile.ID, refContext{Name: profile.Name})
}

// profileByUserID maps a Jellyfin user GUID back to a profile.
func (s *Server) profileByUserID(id string) (models.User, bool) {
	id = normalizeID(id)
	if id == "" {
		return models.User{}, false
	}
	for _, profile := range s.deps.Users.ListAll() {
		if deriveID(refUser, profile.ID) == id {
			return profile, true
		}
	}
	return models.User{}, false
}

func randomHex(n int) string {
	return hex.EncodeToString(randomBytes(n))
}

func randomBytes(n int) []byte {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return buf
}

func (s *Server) trustedProxy(ip net.IP) bool {
	for _, network := range s.proxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveWaitFor caps how long a request blocks on stream resolution for
// clients with short HTTP timeouts. Android TV's SDK client gives up after
// about 10s; browsers, Swiftfin and desktop players wait much longer.
// The client is identified by the caller that started playback: media
// requests (hls.js, ExoPlayer) carry no client header.
func resolveWaitFor(p *principal, wait time.Duration) time.Duration {
	const shortTimeoutMaxWait = 8 * time.Second
	if p != nil && strings.Contains(strings.ToLower(p.Client.Client), "android tv") && wait > shortTimeoutMaxWait {
		return shortTimeoutMaxWait
	}
	return wait
}
