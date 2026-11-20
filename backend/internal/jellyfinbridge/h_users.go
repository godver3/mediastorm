package jellyfinbridge

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// bridgeTokenTTL is the lifetime of a Jellyfin bridge token. Jellyfin clients
// expect long-lived tokens (they never refresh); revoke via mediastorm's
// session management or by changing the account password.
const bridgeTokenTTL = 365 * 24 * time.Hour

var errAmbiguousLogin = errors.New("ambiguous profile name")

// displayNames returns the Jellyfin user name for every profile:
// "account/profile", unique and typeable at the login prompt. A profile whose
// account is gone keeps its name, with a short tag if another profile has it.
func (s *Server) displayNames() map[string]string {
	profiles := s.deps.Users.ListAll()
	counts := map[string]int{}
	for _, p := range profiles {
		counts[strings.ToLower(strings.TrimSpace(p.Name))]++
	}
	out := make(map[string]string, len(profiles))
	for _, p := range profiles {
		name := strings.TrimSpace(p.Name)
		if acct, ok := s.deps.Accounts.Get(p.AccountID); ok && strings.TrimSpace(acct.Username) != "" {
			out[p.ID] = strings.TrimSpace(acct.Username) + "/" + name
			continue
		}
		if counts[strings.ToLower(name)] > 1 {
			name = name + " (" + deriveID(refUser, p.ID)[:4] + ")"
		}
		out[p.ID] = name
	}
	return out
}

// resolveLogin finds the single profile a Jellyfin login refers to and checks
// the password against the owning account (or the profile PIN, if allowed).
// Accepted user names: the profile display name ("Kids" or "Kids (alice)"),
// or "account/profile".
func (s *Server) resolveLogin(username, password string) (models.User, models.Account, error) {
	return s.verifyLogin(s.loginCandidates(username), password)
}

// loginCandidates returns the profiles a typed user name can refer to.
func (s *Server) loginCandidates(username string) []models.User {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil
	}
	names := s.displayNames()
	var candidates []models.User
	accountHint, profileHint, hasSlash := strings.Cut(username, "/")
	for _, p := range s.deps.Users.ListAll() {
		switch {
		case strings.EqualFold(names[p.ID], username):
			candidates = append(candidates, p)
		case strings.EqualFold(strings.TrimSpace(p.Name), username):
			candidates = append(candidates, p)
		case hasSlash && strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(profileHint)):
			if acct, ok := s.deps.Accounts.Get(p.AccountID); ok && strings.EqualFold(acct.Username, strings.TrimSpace(accountHint)) {
				candidates = append(candidates, p)
			}
		}
	}
	return candidates
}

// loginMaxCandidates bounds the password checks one login can cost: a bare
// profile name such as "Kids" can match a profile in every account, and each
// candidate costs up to five bcrypt comparisons. Beyond it the name must be
// typed as "account/profile".
const loginMaxCandidates = 3

// verifyLogin checks the password against each candidate profile.
func (s *Server) verifyLogin(candidates []models.User, password string) (models.User, models.Account, error) {
	if len(candidates) > loginMaxCandidates {
		return models.User{}, models.Account{}, errAmbiguousLogin
	}
	var matched []models.User
	var matchedAccount models.Account
	for _, p := range candidates {
		acct, ok := s.deps.Accounts.Get(p.AccountID)
		if !ok {
			continue
		}
		// VerifyPin succeeds for profiles without a PIN, so HasPin must be checked first.
		hasPin := s.deps.Users.HasPin(p.ID)
		if hasPin && s.profilePinsEnforced() {
			// Same rule as the main API: a PIN-locked profile needs its PIN on
			// top of the account password, sent as "<password><PIN>".
			if verified, ok := s.verifyPasswordWithPin(p, acct, password); ok {
				matched = append(matched, p)
				matchedAccount = verified
				continue
			}
		} else if verified, err := s.deps.Accounts.Authenticate(acct.Username, password); err == nil {
			matched = append(matched, p)
			matchedAccount = verified
			continue
		}
		if s.settings().AllowPinLogin && password != "" && hasPin && s.deps.Users.VerifyPin(p.ID, password) == nil && !acct.IsExpired() {
			matched = append(matched, p)
			matchedAccount = acct
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], matchedAccount, nil
	case 0:
		return models.User{}, models.Account{}, errors.New("invalid credentials")
	default:
		return models.User{}, models.Account{}, errAmbiguousLogin
	}
}

func (s *Server) profilePinsEnforced() bool {
	return s.deps.ProfilePinsEnforced != nil && s.deps.ProfilePinsEnforced()
}

// verifyPasswordWithPin checks "<account password><profile PIN>". The PIN
// length is known once the PIN has been used; older profiles try 4-8 digits.
func (s *Server) verifyPasswordWithPin(p models.User, acct models.Account, password string) (models.Account, bool) {
	lengths := []int{p.PinLength}
	if p.PinLength <= 0 {
		lengths = []int{4, 5, 6, 7, 8}
	}
	for _, n := range lengths {
		if n <= 0 || len(password) <= n {
			continue
		}
		accountPassword, pin := password[:len(password)-n], password[len(password)-n:]
		if s.deps.Users.VerifyPin(p.ID, pin) != nil {
			continue
		}
		if verified, err := s.deps.Accounts.Authenticate(acct.Username, accountPassword); err == nil {
			return verified, true
		}
	}
	return models.Account{}, false
}

type authenticateByNameRequest struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
	Password string `json:"Password"`
}

func (s *Server) handleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	var req authenticateByNameRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	// Limit attempts per client network and per targeted profile. Keys come
	// from the resolved profiles, not the typed name, so spelling variants of
	// one profile ("alice/Kids", "Kids", "Kids (ab12)") share a budget. The
	// attempt is recorded before the password check so parallel requests
	// cannot all slip under the limit.
	candidates := s.loginCandidates(req.Username)
	if len(candidates) > loginMaxCandidates {
		// Refused before any password check, and without charging the
		// matched profiles' budgets.
		log.Printf("[jellyfin-bridge] login for %q matches %d profiles; use \"account/profile\"", req.Username, len(candidates))
		writeError(w, http.StatusUnauthorized, "Error processing request.")
		return
	}
	keys := []string{"ip:" + ipLimitKey(ip)}
	for _, p := range candidates {
		keys = append(keys, "profile:"+p.ID)
	}
	if !s.login.attempt(keys...) {
		writeError(w, http.StatusTooManyRequests, "Too many login attempts. Try again later.")
		return
	}
	password := req.Pw
	if password == "" {
		password = req.Password
	}
	profile, account, err := s.verifyLogin(candidates, password)
	if err != nil {
		if errors.Is(err, errAmbiguousLogin) {
			log.Printf("[jellyfin-bridge] login for %q matches several profiles; use \"account/profile\"", req.Username)
		}
		writeError(w, http.StatusUnauthorized, "Error processing request.")
		return
	}
	// Success clears the profile budgets and withdraws this attempt from the
	// network budget (which counts failures only, so a household behind one
	// proxy address is not locked out by its own logins).
	s.login.clear(keys[1:]...)
	s.login.undoLast(keys[0])

	info := requestClientInfo(r)
	userAgent := strings.TrimSpace(strings.Join([]string{info.Client, info.Version, info.Device}, " "))
	if userAgent == "" {
		userAgent = r.UserAgent()
	}
	session, err := s.deps.Sessions.CreateScopedWithResource(account.ID, account.IsMaster, "Jellyfin: "+userAgent, ip, bridgeTokenTTL, models.SessionScopeJellyfin, profile.ID)
	if err != nil {
		log.Printf("[jellyfin-bridge] create session: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not create session")
		return
	}
	log.Printf("[jellyfin-bridge] %q signed in as profile %q (client %s %s)", account.Username, profile.Name, info.Client, info.Device)
	writeJSON(w, http.StatusOK, dto.AuthenticationResult{
		User:        s.userDto(profile),
		AccessToken: session.Token,
		ServerID:    s.serverID,
	})
}

func (s *Server) userDto(profile models.User) dto.UserDto {
	name := s.displayNames()[profile.ID]
	if name == "" {
		name = profile.Name
	}
	now := time.Now().UTC()
	return dto.UserDto{
		Name:                      name,
		ServerID:                  s.serverID,
		ServerName:                s.settings().ServerName,
		ID:                        s.userID(profile),
		HasPassword:               true,
		HasConfiguredPassword:     true,
		HasConfiguredEasyPassword: false,
		EnableAutoLogin:           false,
		LastLoginDate:             &now,
		LastActivityDate:          &now,
		Configuration:             defaultUserConfiguration(),
		Policy:                    s.userPolicy(profile),
	}
}

func defaultUserConfiguration() *dto.UserConfiguration {
	return &dto.UserConfiguration{
		PlayDefaultAudioTrack:      true,
		DisplayMissingEpisodes:     false,
		GroupedFolders:             []string{},
		SubtitleMode:               "Default",
		DisplayCollectionsView:     false,
		EnableLocalPassword:        false,
		OrderedViews:               []string{},
		LatestItemsExcludes:        []string{},
		MyMediaExcludes:            []string{},
		HidePlayedInLatest:         true,
		RememberAudioSelections:    true,
		RememberSubtitleSelections: true,
		EnableNextEpisodeAutoPlay:  true,
	}
}

// userPolicy never grants admin: the bridge implements none of Jellyfin's
// admin API, and an admin flag makes jellyfin-web open its dashboard.
func (s *Server) userPolicy(profile models.User) *dto.UserPolicy {
	return &dto.UserPolicy{
		IsAdministrator:                  false,
		IsHidden:                         false,
		IsDisabled:                       false,
		BlockedTags:                      []string{},
		AllowedTags:                      []string{},
		EnableUserPreferenceAccess:       true,
		AccessSchedules:                  []any{},
		BlockUnratedItems:                []string{},
		EnableRemoteAccess:               true,
		EnableLiveTvAccess:               !profile.IsKidsProfile,
		EnableCollectionManagement:       s.listsEditable(), // collections are the profile's custom lists
		EnableMediaPlayback:              true,
		EnableAudioPlaybackTranscoding:   true,
		EnableVideoPlaybackTranscoding:   true,
		EnablePlaybackRemuxing:           true,
		EnableContentDeletionFromFolders: []string{},
		EnabledDevices:                   []string{},
		EnableAllDevices:                 true,
		EnabledChannels:                  []string{},
		EnableAllChannels:                true,
		EnabledFolders:                   []string{},
		EnableAllFolders:                 true,
		LoginAttemptsBeforeLockout:       -1,
		MaxActiveSessions:                0,
		BlockedMediaFolders:              []string{},
		BlockedChannels:                  []string{},
		AuthenticationProviderID:         "Jellyfin.Server.Implementations.Users.DefaultAuthenticationProvider",
		PasswordResetProviderID:          "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
		SyncPlayAccess:                   "None",
	}
}

// handlePublicUsers lists profiles so clients can show a user picker.
func (s *Server) handlePublicUsers(w http.ResponseWriter, _ *http.Request) {
	if !s.settings().ShowUserList {
		writeJSON(w, http.StatusOK, []dto.UserDto{})
		return
	}
	profiles := s.deps.Users.ListAll()
	sort.Slice(profiles, func(i, j int) bool { return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name) })
	out := make([]dto.UserDto, 0, len(profiles))
	for _, p := range profiles {
		if acct, ok := s.deps.Accounts.Get(p.AccountID); !ok || acct.IsExpired() {
			continue
		}
		u := s.userDto(p)
		u.Policy = nil
		u.Configuration = nil
		u.LastLoginDate = nil
		u.LastActivityDate = nil
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.userDto(principalFrom(r).Profile))
}

func (s *Server) handleUserByID(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if normalizeID(pathVar(r, "uid")) != s.userID(p.Profile) {
		writeError(w, http.StatusNotFound, "User not found")
		return
	}
	writeJSON(w, http.StatusOK, s.userDto(p.Profile))
}

// handleUsers is the admin listing; non-admins only ever see themselves.
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []dto.UserDto{s.userDto(principalFrom(r).Profile)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if p := principalFrom(r); p != nil {
		_ = s.deps.Sessions.Revoke(p.Session.Token)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessions returns the caller's own session. jellyfin-web reads it to
// populate the remote-control menu and its playback info overlay.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	info := dto.SessionInfoDto{
		ID:                       deriveID("session", p.Session.Token),
		UserID:                   s.userID(p.Profile),
		UserName:                 p.Profile.Name,
		Client:                   p.Client.Client,
		DeviceID:                 p.Client.DeviceID,
		DeviceName:               p.Client.Device,
		ApplicationVersion:       p.Client.Version,
		ServerID:                 s.serverID,
		LastActivityDate:         time.Now().UTC(),
		IsActive:                 true,
		PlayableMediaTypes:       []string{"Video"},
		SupportedCommands:        []string{},
		AdditionalUsers:          []any{},
		NowPlayingQueue:          []any{},
		NowPlayingQueueFullItems: []any{},
		PlayState:                dto.PlayerStateInfo{RepeatMode: "RepeatNone", PlaybackOrder: "Default"},
	}
	s.fillNowPlaying(r.Context(), p, &info)
	writeJSON(w, http.StatusOK, []dto.SessionInfoDto{info})
}

// loginLimiter throttles login attempts per key (client network and each
// targeted profile), so neither many addresses against one profile nor one
// address against many profiles can guess freely.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

const (
	loginWindow      = 10 * time.Minute
	loginMaxFailures = 10
	loginMaxKeys     = 50000
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string][]time.Time{}}
}

// attempt records one attempt against every key if all are under the limit,
// and reports whether the attempt may proceed. Successful logins clear().
func (l *loginLimiter) attempt(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-loginWindow)
	for _, key := range keys {
		recent := l.attempts[key][:0]
		for _, t := range l.attempts[key] {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		if len(recent) == 0 {
			delete(l.attempts, key)
		} else {
			l.attempts[key] = recent
		}
		if len(recent) >= loginMaxFailures {
			return false
		}
	}
	if len(l.attempts) >= loginMaxKeys {
		l.evictLocked(cutoff)
	}
	for _, key := range keys {
		l.attempts[key] = append(l.attempts[key], now)
	}
	return true
}

// evictLocked bounds the map without refusing logins: expired keys go
// first, then the keys with the oldest latest attempt.
func (l *loginLimiter) evictLocked(cutoff time.Time) {
	for key, times := range l.attempts {
		if len(times) == 0 || times[len(times)-1].Before(cutoff) {
			delete(l.attempts, key)
		}
	}
	if len(l.attempts) < loginMaxKeys {
		return
	}
	type entry struct {
		key  string
		last time.Time
	}
	entries := make([]entry, 0, len(l.attempts))
	for key, times := range l.attempts {
		entries = append(entries, entry{key, times[len(times)-1]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].last.Before(entries[j].last) })
	for _, e := range entries[:len(entries)/2] {
		delete(l.attempts, e.key)
	}
}

// undoLast removes the most recent attempt recorded for key.
func (l *loginLimiter) undoLast(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if times := l.attempts[key]; len(times) > 1 {
		l.attempts[key] = times[:len(times)-1]
	} else {
		delete(l.attempts, key)
	}
}

func (l *loginLimiter) clear(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.attempts, key)
	}
}

// ipLimitKey groups IPv6 clients by /64 (one subscriber's allocation).
func ipLimitKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
