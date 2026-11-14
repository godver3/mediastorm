package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"novastream/models"
	"strings"
	"time"
)

var errLiveProgrammeBlocked = errors.New("live programme is not permitted for this profile")

func isLiveKidsProfile(users UsersProvider, profileID string) bool {
	if users == nil {
		return false
	}
	profile, ok := users.Get(profileID)
	return ok && profile.IsKidsProfile
}

// AuthorizeLiveProgramme always consults the current guide and profile policy.
// It returns the current programme boundary so an active session can recheck it.
func (h *LiveHandler) AuthorizeLiveProgramme(r *http.Request, rawURL string) (time.Time, error) {
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if h.usersSvc == nil {
		return time.Time{}, nil
	}
	profile, ok := h.usersSvc.Get(profileID)
	if !ok {
		return time.Time{}, errLiveProgrammeBlocked
	}
	if !profile.IsKidsProfile {
		return time.Time{}, nil
	}
	if profile.KidsLiveMaxAge == nil || h.epgService == nil {
		return time.Time{}, errLiveProgrammeBlocked
	}
	settings, err := h.cfgManager.Load()
	if err != nil {
		return time.Time{}, errLiveProgrammeBlocked
	}
	channels, err := visibleLiveChannels(r, h, profileID)
	if err != nil {
		return time.Time{}, errLiveProgrammeBlocked
	}
	offset := time.Duration(h.resolveProfileLiveSource(r, settings).EPGTimeOffsetMinutes) * time.Minute
	now := time.Now()
	for _, channel := range channels {
		if channelID := r.URL.Query().Get("channelId"); channelID != "" && channelID != channel.ID && channelID != channel.PlaybackID {
			continue
		}
		if strings.TrimSpace(channel.URL) != strings.TrimSpace(rawURL) {
			continue
		}
		id := strings.TrimSpace(channel.TvgID)
		if id == "" {
			return time.Time{}, errLiveProgrammeBlocked
		}
		found := false
		var boundary time.Time
		for _, current := range h.epgService.GetNowPlaying([]string{id}, -offset) {
			p := current.Current
			if p == nil || p.Start.Add(offset).After(now) || !p.Stop.Add(offset).After(now) {
				continue
			}
			age, known := models.ProgrammeMinimumAge(*p)
			if !known || age > *profile.KidsLiveMaxAge {
				return time.Time{}, errLiveProgrammeBlocked
			}
			if boundary.IsZero() || p.Stop.Add(offset).Before(boundary) {
				boundary = p.Stop.Add(offset)
			}
			found = true
		}
		if found {
			return boundary, nil
		}
		return time.Time{}, errLiveProgrammeBlocked
	}
	return time.Time{}, errLiveProgrammeBlocked
}

type liveProgrammeAuthorizer interface {
	AuthorizeLiveProgramme(*http.Request, string) (time.Time, error)
}

func (h *VideoHandler) requireLiveProgramme(w http.ResponseWriter, r *http.Request, source string) bool {
	authorizer, ok := h.liveChannels.(liveProgrammeAuthorizer)
	if !ok {
		if !isLiveKidsProfile(h.usersSvc, r.URL.Query().Get("profileId")) {
			return true
		}
	} else if _, err := authorizer.AuthorizeLiveProgramme(r, source); err == nil {
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "live programme blocked by parental controls", http.StatusForbidden)
	return false
}

// Per-request checks protect playlists and segments. The monitor also ends
// upstream work while a child pauses or disconnects at a programme boundary.
func (h *VideoHandler) monitorLiveProgramme(r *http.Request, sessionID, source string) {
	if !isLiveKidsProfile(h.usersSvc, r.URL.Query().Get("profileId")) {
		return
	}
	authorizer, ok := h.liveChannels.(liveProgrammeAuthorizer)
	if !ok {
		return
	}
	request := r.Clone(context.WithoutCancel(r.Context()))
	go func() {
		for {
			h.hlsManager.mu.RLock()
			session, exists := h.hlsManager.sessions[sessionID]
			h.hlsManager.mu.RUnlock()
			if !exists {
				return
			}
			ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
			boundary, err := authorizer.AuthorizeLiveProgramme(request.WithContext(ctx), source)
			cancel()
			if err != nil {
				h.hlsManager.CleanupSession(sessionID)
				return
			}
			session.mu.RLock()
			stopped := session.Stopped
			session.mu.RUnlock()
			if stopped {
				return
			}
			delay := 5 * time.Second
			if until := time.Until(boundary); !boundary.IsZero() && until < delay {
				delay = until
			}
			if delay < 10*time.Millisecond {
				delay = 10 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			<-timer.C
		}
	}()
}

const kidsLiveReferencePrefix = "https://live.mediastorm.invalid/channel/"

func kidsLiveReference(id string) string {
	return kidsLiveReferencePrefix + base64.RawURLEncoding.EncodeToString([]byte(id))
}

// Only managed tune-in resolves these opaque references. The catalog never
// sends provider stream credentials to a child's player or fallback path.
func (h *VideoHandler) resolveKidsLiveReference(w http.ResponseWriter, r *http.Request, source string) (string, bool) {
	if !isLiveKidsProfile(h.usersSvc, r.URL.Query().Get("profileId")) {
		return source, true
	}
	if !strings.HasPrefix(source, kidsLiveReferencePrefix) {
		http.Error(w, "kids Live TV requires a catalog reference", http.StatusForbidden)
		return "", false
	}
	id, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(source, kidsLiveReferencePrefix))
	if err != nil {
		http.Error(w, "invalid channel reference", http.StatusBadRequest)
		return "", false
	}
	channels, err := visibleLiveChannels(r, h.liveChannels, r.URL.Query().Get("profileId"))
	if err == nil {
		for _, channel := range channels {
			if channel.ID != string(id) {
				continue
			}
			query := r.URL.Query()
			query.Set("sourceId", channel.SourceID)
			query.Set("channelId", channel.ID)
			if channel.PlaybackID != "" {
				query.Set("channelId", channel.PlaybackID)
			}
			query.Set("url", channel.URL)
			r.URL.RawQuery = query.Encode()
			return channel.URL, true
		}
	}
	http.Error(w, "channel not available", http.StatusNotFound)
	return "", false
}

func redactKidsLiveChannels(channels []LiveChannel) []LiveChannel {
	result := append([]LiveChannel(nil), channels...)
	for i := range result {
		result[i].URL = kidsLiveReference(result[i].ID)
		result[i].StreamURL = ""
		result[i].Catchup = nil
	}
	return result
}

// Keep generic video proxies from accepting these reserved catalog references.
func isKidsLiveReference(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && strings.EqualFold(parsed.Hostname(), "live.mediastorm.invalid")
}
