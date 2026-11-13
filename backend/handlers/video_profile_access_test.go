package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
)

func TestHLSRequestsCheckStoredProfile(t *testing.T) {
	lastAccess := time.Now().Add(-time.Hour)
	session := &HLSSession{ID: "foreign-session", ProfileID: "foreign-profile", OriginalPath: "/webdav/foreign.mkv", LastAccess: lastAccess}
	manager := &HLSManager{sessions: map[string]*HLSSession{session.ID: session}}
	h := &VideoHandler{hlsManager: manager}
	for _, handler := range []http.HandlerFunc{h.ServeHLSPlaylist, h.ServeHLSMasterPlaylist, h.ServeHLSSegment, h.ServeHLSSubtitles, h.ServeHLSSubtitlePlaylist, h.ServeHLSSubtitleTrack, h.ServeHLSLiveCaptions, h.GetHLSLiveCCStatus, h.KeepAliveHLSSession, h.StopHLSSession, h.GetHLSSessionStatus, h.SeekHLSSession} {
		r := httptest.NewRequest("POST", "/api/video/hls/foreign-session?profileId=own-profile", nil)
		r = mux.SetURLVars(r, map[string]string{"sessionID": session.ID, "segment": "segment0.ts", "track": "0"})
		r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account-a"))
		w := httptest.NewRecorder()
		profileaccess.Middleware(func(profile, account string) bool { return profile == "own-profile" && account == "account-a" })(handler).ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("HLS handler returned HTTP %d", w.Code)
		}
		if manager.sessions[session.ID] != session || !session.LastAccess.Equal(lastAccess) {
			t.Fatal("denied request modified the foreign session")
		}
	}
}

func TestHLSStatusAllowsOwnerAndBoundShareOnly(t *testing.T) {
	for _, tc := range []struct {
		name, profile, scope, resource string
		want                           int
	}{
		{"owner", "own-profile", "", "", 200},
		{"foreign", "foreign-profile", "", "", 404},
		{"unattributed", "", "", "", 404},
		{"bound share", "foreign-profile", models.SessionScopeStream, "/fixture.mkv", 200},
		{"other shared source", "foreign-profile", models.SessionScopeStream, "/different.mkv", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &HLSSession{ID: "session", ProfileID: tc.profile, OriginalPath: "/webdav/fixture.mkv"}
			h := &VideoHandler{hlsManager: &HLSManager{sessions: map[string]*HLSSession{"session": session}}}
			r := httptest.NewRequest("GET", "/api/video/hls/session/status", nil)
			r = mux.SetURLVars(r, map[string]string{"sessionID": "session"})
			ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "account-a")
			ctx = context.WithValue(ctx, auth.ContextKeySession, models.Session{Scope: tc.scope, ScopeResource: tc.resource})
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(profile, account string) bool { return profile == "own-profile" && account == "account-a" })(http.HandlerFunc(h.GetHLSSessionStatus)).ServeHTTP(w, r.WithContext(ctx))
			if w.Code != tc.want {
				t.Fatalf("HTTP %d, want %d", w.Code, tc.want)
			}
		})
	}
}
