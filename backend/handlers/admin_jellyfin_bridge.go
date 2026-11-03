package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"novastream/config"
)

// JellyfinBridgeStatus is the bridge listener's state.
type JellyfinBridgeStatus struct {
	Running  bool   `json:"running"`
	Port     int    `json:"port,omitempty"`
	ServerID string `json:"serverId,omitempty"`
	Error    string `json:"error,omitempty"`
}

// JellyfinBridgeController starts and stops the bridge.
type JellyfinBridgeController interface {
	Apply(config.JellyfinBridgeSettings) error
	Status() JellyfinBridgeStatus
}

// JellyfinBridgeAdmin serves the Jellyfin Bridge section of the Integrations
// page: its settings and whether it runs. Saving applies at once.
type JellyfinBridgeAdmin struct {
	cfg  *config.Manager
	ctrl JellyfinBridgeController
	// testBaseURL overrides the address the self-test dials (tests only).
	testBaseURL string
}

func NewJellyfinBridgeAdmin(cfg *config.Manager, ctrl JellyfinBridgeController) *JellyfinBridgeAdmin {
	return &JellyfinBridgeAdmin{cfg: cfg, ctrl: ctrl}
}

// maxJellyfinReleaseVersions caps the version list (mediastorm's search
// returns at most 50 results).
const maxJellyfinReleaseVersions = 50

// jellyfinBridgeFields describes the settings form, in display order.
var jellyfinBridgeFields = []map[string]any{
	{"key": "port", "type": "number", "group": "Connection", "label": "Port", "description": "Listener port (default: 8096, the standard Jellyfin port)."},
	{"key": "host", "type": "text", "group": "Connection", "label": "Host", "description": "Bind address (leave empty to bind all interfaces)."},
	{"key": "serverName", "type": "text", "group": "Connection", "label": "Server Name", "description": "Name shown in Jellyfin apps."},
	{"key": "udpDiscovery", "type": "boolean", "group": "Connection", "label": "LAN Discovery", "description": "Answer Jellyfin server discovery broadcasts on UDP port 7359."},
	{"key": "trustedProxies", "type": "tags", "group": "Connection", "label": "Trusted Proxies", "description": "Reverse proxy addresses or CIDRs (e.g. 172.18.0.0/16) whose X-Forwarded-For header is trusted for per-client login limits, comma-separated. Loopback is always trusted."},
	{"key": "showUserList", "type": "boolean", "group": "Sign-in", "warning": true, "label": "Show Profile List", "description": "Security warning: think this through before turning it on. Lists every profile as account/profile (e.g. \"household/Anna\") on the Jellyfin sign-in screen to anyone who can reach the bridge port, so the account login names are public and only the password is left to guess. Use it only on a LAN or behind a VPN with people you trust, never when the port is reachable from the internet. Off, users type account/profile (or the profile name) or use Quick Connect."},
	{"key": "allowPinLogin", "type": "boolean", "group": "Sign-in", "label": "Allow PIN Login", "description": "Accept a profile's PIN alone as the Jellyfin password. Weaker than the account password: a 4-digit PIN is easy to guess. With Enforce Profile PINs on and this off, PIN-locked profiles sign in with the account password followed by the PIN."},
	{"key": "quickConnect", "type": "boolean", "group": "Sign-in", "label": "Quick Connect", "description": "Let a signed-in user approve another device (e.g. a TV) by entering the code it shows, instead of typing the password on the TV."},
	{"key": "webClientDir", "type": "text", "group": "Web Client", "label": "Web Client Directory", "placeholder": defaultJellyfinWebDir, "description": "Folder containing a jellyfin-web build (with index.html), as seen from inside the mediastorm container. Leave empty for " + defaultJellyfinWebDir + "."},
	{"key": "playbackInfoWaitSeconds", "type": "number", "group": "Playback", "label": "Resolve Wait (seconds)", "description": "How long to wait for stream resolution before answering the client (default: 20)."},
	{"key": "releaseVersions", "type": "number", "group": "Playback", "label": "Release Versions", "min": 0, "max": maxJellyfinReleaseVersions, "description": "How many of mediastorm's top-ranked releases movies and episodes offer as versions to pick from, in ranking order (default: 15). 0 removes the version choice; the Auto version is played as before."},
	{"key": "autoAvoidVideoTranscoding", "type": "boolean", "group": "Playback", "label": "Auto: Avoid Video Transcoding", "description": "When mediastorm's first choice would need its video transcoded for the app (codec, HDR or Dolby Vision it does not declare), the Auto version plays the best-ranked release the app can take as it is. Up to 3 more releases are tried; versions picked by hand are played as they are."},
	{"key": "copyHevc", "type": "boolean", "group": "Playback", "label": "Pass Through HEVC/AV1", "description": "Copy HEVC and AV1 video to Jellyfin apps and browsers that decode it, instead of converting it to H.264."},
	{"key": "copyHdr", "type": "boolean", "group": "Playback", "label": "Pass Through HDR", "description": "Keep HDR10/HDR10+/HLG for clients that declare HDR support; others get a tone-mapped SDR transcode."},
	{"key": "copyDolbyVision", "type": "boolean", "group": "Playback", "label": "Pass Through Dolby Vision", "description": "Keep Dolby Vision for clients that declare it (Safari, some TVs), or play profile 8 as its HDR10/HLG base layer."},
	{"key": "disableVideoTranscoding", "type": "boolean", "group": "Playback", "label": "Disable Video Transcoding", "description": "Never re-encode video for Jellyfin apps. Video is always copied, even when the app does not report support for the codec or HDR format (it may then play without picture or with wrong colours), and a stream that cannot be copied does not play. Live TV is then passed through as received, audio included."},
	{"key": "disableAudioTranscoding", "type": "boolean", "group": "Playback", "label": "Disable Audio Transcoding", "description": "Never convert audio for Jellyfin apps. Tracks the app cannot decode (e.g. TrueHD, DTS or E-AC-3 in Chrome) then play silently."},
}

// defaultJellyfinWebDir is where the bridge looks for jellyfin-web when no
// directory is set (mount a build there in Docker).
const defaultJellyfinWebDir = "/app/jellyfin-web"

// jellyfinWebClient tells the page whether a jellyfin-web build is in place.
type jellyfinWebClient struct {
	Dir   string `json:"dir"`
	Found bool   `json:"found"`
}

type jellyfinBridgeResponse struct {
	Settings  config.JellyfinBridgeSettings `json:"settings"`
	Status    JellyfinBridgeStatus          `json:"status"`
	WebClient jellyfinWebClient             `json:"webClient"`
	Fields    []map[string]any              `json:"fields"`
}

func webClientState(s config.JellyfinBridgeSettings) jellyfinWebClient {
	dir := strings.TrimSpace(s.WebClientDir)
	if dir == "" {
		dir = defaultJellyfinWebDir
	}
	st, err := os.Stat(filepath.Join(dir, "index.html"))
	return jellyfinWebClient{Dir: dir, Found: err == nil && !st.IsDir()}
}

func (a *JellyfinBridgeAdmin) write(w http.ResponseWriter, status int, s config.JellyfinBridgeSettings) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jellyfinBridgeResponse{Settings: s, Status: a.ctrl.Status(), WebClient: webClientState(s), Fields: jellyfinBridgeFields})
}

func writeBridgeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Get returns the bridge settings, its status and the form fields.
func (a *JellyfinBridgeAdmin) Get(w http.ResponseWriter, r *http.Request) {
	s, err := a.cfg.Load()
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.write(w, http.StatusOK, s.JellyfinBridge)
}

// Put saves the bridge settings and starts, stops or restarts the bridge.
func (a *JellyfinBridgeAdmin) Put(w http.ResponseWriter, r *http.Request) {
	var in config.JellyfinBridgeSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
		return
	}
	if in.Port < 1 || in.Port > 65535 {
		writeBridgeError(w, http.StatusBadRequest, "port must be between 1 and 65535")
		return
	}
	if in.ReleaseVersions < 0 || in.ReleaseVersions > maxJellyfinReleaseVersions {
		writeBridgeError(w, http.StatusBadRequest, "release versions must be between 0 and 50")
		return
	}
	if in.PlaybackInfoWaitSeconds < 0 || in.PlaybackInfoWaitSeconds > 300 {
		writeBridgeError(w, http.StatusBadRequest, "resolve wait must be between 0 and 300 seconds")
		return
	}
	in.Host = strings.TrimSpace(in.Host)
	in.ServerName = strings.TrimSpace(in.ServerName)
	in.WebClientDir = strings.TrimSpace(in.WebClientDir)
	proxies := in.TrustedProxies[:0:0]
	for _, p := range in.TrustedProxies {
		if p = strings.TrimSpace(p); p != "" {
			proxies = append(proxies, p)
		}
	}
	in.TrustedProxies = proxies

	s, err := a.cfg.Load()
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	in.ServerID = s.JellyfinBridge.ServerID // the clients' identity of this server
	s.JellyfinBridge = in
	if err := a.cfg.Save(s); err != nil {
		writeBridgeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A listener that cannot start (port in use) is reported in the status;
	// the settings stay saved.
	_ = a.ctrl.Apply(in)
	a.write(w, http.StatusOK, in)
}
