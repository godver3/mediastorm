package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"novastream/internal/auth"
	"novastream/models"
)

type fakeShareSessions struct {
	lastScope    string
	lastDuration time.Duration
	lastAccount  string
}

func (f *fakeShareSessions) CreateScoped(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration, scope string) (models.Session, error) {
	f.lastScope = scope
	f.lastDuration = duration
	f.lastAccount = accountID
	return models.Session{
		Token:     "minted-token",
		AccountID: accountID,
		IsMaster:  isMaster,
		Scope:     scope,
		ExpiresAt: time.Now().Add(duration),
	}, nil
}

func newTestShareHandler() (*ShareHandler, *fakeShareSessions) {
	sessions := &fakeShareSessions{}
	return NewShareHandler(NewShareStore(), sessions, ""), sessions
}

func TestShareCreateStoresWhitelistedParams(t *testing.T) {
	h, _ := newTestShareHandler()

	body, _ := json.Marshal(map[string]string{
		"sourcePath":            "/movies/a.mkv",
		"preselectedAudioTrack": "2",
		"title":                 "A Movie",
		"notAllowedKey":         "should-be-dropped",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp shareCreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" || !strings.HasSuffix(resp.URL, "/share/"+resp.Token) {
		t.Fatalf("unexpected response: %+v", resp)
	}

	stored, ok := h.store.Consume(resp.Token)
	if !ok {
		t.Fatal("share should have been stored")
	}
	if stored.Params["sourcePath"] != "/movies/a.mkv" || stored.Params["preselectedAudioTrack"] != "2" {
		t.Fatalf("whitelisted params not stored: %v", stored.Params)
	}
	if _, exists := stored.Params["notAllowedKey"]; exists {
		t.Fatal("non-whitelisted param should have been dropped")
	}
}

func TestShareCreateRejectsNoSource(t *testing.T) {
	h, _ := newTestShareHandler()
	body, _ := json.Marshal(map[string]string{"title": "No Source"})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestShareCreateRequiresAuth(t *testing.T) {
	h, _ := newTestShareHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(`{"sourcePath":"/x"}`))
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestShareOpenMintsScopedSessionAndIsSingleUse(t *testing.T) {
	h, sessions := newTestShareHandler()
	rec, _ := h.store.Create("acct9", false, map[string]string{
		"sourcePath":            "/movies/a.mkv",
		"preselectedAudioTrack": "1",
	})

	req := httptest.NewRequest(http.MethodGet, "/share/"+rec.Token, nil)
	w := httptest.NewRecorder()
	h.Open(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body: %s)", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if !strings.HasSuffix(parsed.Path, "/watch/playback.html") {
		t.Fatalf("Location path = %q, want /watch/playback.html", parsed.Path)
	}
	q := parsed.Query()
	if q.Get("token") != "minted-token" {
		t.Fatalf("token = %q, want minted-token", q.Get("token"))
	}
	if q.Get("shareMode") != "1" {
		t.Fatalf("shareMode = %q, want 1", q.Get("shareMode"))
	}
	if q.Get("sourcePath") != "/movies/a.mkv" || q.Get("preselectedAudioTrack") != "1" {
		t.Fatalf("captured params missing from redirect: %v", q)
	}
	if sessions.lastScope != models.SessionScopeStream {
		t.Fatalf("minted scope = %q, want %q", sessions.lastScope, models.SessionScopeStream)
	}
	if sessions.lastDuration != SharePlaybackSessionTTL {
		t.Fatalf("minted duration = %v, want %v", sessions.lastDuration, SharePlaybackSessionTTL)
	}
	if sessions.lastAccount != "acct9" {
		t.Fatalf("minted account = %q, want acct9", sessions.lastAccount)
	}

	// Second open is rejected (single use).
	w2 := httptest.NewRecorder()
	h.Open(w2, httptest.NewRequest(http.MethodGet, "/share/"+rec.Token, nil))
	if w2.Code != http.StatusGone {
		t.Fatalf("second open status = %d, want 410", w2.Code)
	}
}

func TestShareOpenUnknownTokenIsGone(t *testing.T) {
	h, _ := newTestShareHandler()
	req := httptest.NewRequest(http.MethodGet, "/share/nope", nil)
	w := httptest.NewRecorder()
	h.Open(w, req)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", w.Code)
	}
}
