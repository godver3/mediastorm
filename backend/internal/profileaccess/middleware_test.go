package profileaccess

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"novastream/internal/auth"
	"novastream/models"
)

func TestProfileOwnershipSelectors(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, body, contentType string
		vars                                    map[string]string
		want                                    int
	}{
		{name: "own query", target: "/?profileId=own", want: 200},
		{name: "foreign query", target: "/?profileId=other", want: 404},
		{name: "foreign alias", target: "/?userId=other", want: 404},
		{name: "repeated query", target: "/?profileId=own&profileId=other", want: 404},
		{name: "conflicting aliases", target: "/?profileId=own&userId=other", want: 404},
		{name: "foreign path", target: "/", vars: map[string]string{"userID": "other"}, want: 404},
		{name: "missing profile", target: "/?profileId=missing", want: 404},
		{name: "own JSON", method: "POST", body: `{"profileId":"own","limit":1}`, contentType: "application/json", want: 200},
		{name: "foreign JSON", method: "POST", body: `{"profileId":"other"}`, contentType: "application/json", want: 404},
		{name: "no content type", method: "POST", body: `{"profileId":"other"}`, want: 404},
		{name: "wrong content type", method: "POST", body: `{"profileId":"other"}`, contentType: "text/plain", want: 404},
		{name: "case insensitive JSON", method: "POST", body: `{"PROFILEID":"other"}`, want: 404},
		{name: "mixed case duplicates", method: "POST", body: `{"profileId":"own","PROFILEID":"other"}`, want: 404},
		{name: "escaped key", method: "POST", body: `{"pro\u0066ileId":"other"}`, want: 404},
		{name: "query versus body", method: "POST", target: "/?profileId=own", body: `{"userId":"other"}`, want: 404},
		{name: "malformed JSON", method: "POST", body: `{"profileId":"other"`, want: 400},
		{name: "trailing JSON", method: "POST", body: `{} {"profileId":"other"}`, want: 400},
		{name: "invalid selector type", method: "POST", body: `{"profileId":123}`, want: 400},
		{name: "large object", method: "POST", body: `{"padding":"` + strings.Repeat("x", maxProfileBodyBytes) + `","profileId":"other"}`, want: 413},
		{name: "whitespace padding", method: "POST", body: strings.Repeat(" ", maxProfileBodyBytes+1) + `{"profileId":"other"}`, want: 413},
		{name: "owned bulk import", method: "POST", vars: map[string]string{"userID": "own"}, body: `{"items":["` + strings.Repeat("x", maxProfileBodyBytes) + `"]}`, want: 200},
		{name: "large diagnostic upload", method: "POST", target: "/api/debug/log", body: `{"entries":["` + strings.Repeat("x", maxProfileBodyBytes) + `"]}`, want: 200},
		{name: "binary upload", method: "POST", body: "binary\x00content", contentType: "application/octet-stream", want: 200},
		{name: "preflight", method: "OPTIONS", target: "/?profileId=other", want: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = "GET"
			}
			target := tc.target
			if target == "" {
				target = "/"
			}
			r := httptest.NewRequest(method, target, strings.NewReader(tc.body))
			r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account-a"))
			r = mux.SetURLVars(r, tc.vars)
			r.Header.Set("Content-Type", tc.contentType)
			called := false
			handler := Middleware(func(profile, account string) bool { return profile == "own" && account == "account-a" })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.body {
					t.Fatal("request body changed")
				}
				w.WriteHeader(200)
			}))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == 200) {
				t.Fatalf("HTTP %d, handler called %v; want %d", w.Code, called, tc.want)
			}
			if tc.want == 404 && strings.Contains(w.Body.String(), "other") {
				t.Fatal("foreign profile details exposed")
			}
		})
	}
}

func TestProfileOwnershipMasterAndScopedStream(t *testing.T) {
	for _, mode := range []string{"master", "scoped stream"} {
		t.Run(mode, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/?profileId=other", nil)
			ctx := r.Context()
			if mode == "master" {
				ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
			} else {
				ctx = context.WithValue(ctx, auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream})
			}
			w := httptest.NewRecorder()
			Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r.WithContext(ctx))
			if w.Code != 204 {
				t.Fatalf("HTTP %d", w.Code)
			}
		})
	}
}
