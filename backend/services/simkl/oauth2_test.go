package simkl

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"novastream/config"
)

type oauthStub struct {
	mu       sync.Mutex
	device   func(w http.ResponseWriter, form url.Values)
	token    func(w http.ResponseWriter, form url.Values)
	pinCalls int
	forms    []url.Values
}

func installOAuthStub(t *testing.T, stub *oauthStub) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		stub.mu.Lock()
		stub.forms = append(stub.forms, r.PostForm)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/oauth2/device":
			stub.device(w, r.PostForm)
		case r.URL.Path == "/oauth2/token":
			stub.token(w, r.PostForm)
		case r.URL.Path == "/oauth/pin":
			stub.mu.Lock()
			stub.pinCalls++
			stub.mu.Unlock()
			_, _ = w.Write([]byte(`{"user_code":"12345","verification_url":"https://simkl.com/pin","expires_in":900,"interval":5}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	orig := apiBaseURL
	SetBaseURLForTest(server.URL)
	t.Cleanup(func() { SetBaseURLForTest(orig) })
	return NewClient()
}

func writeDevice(w http.ResponseWriter, _ url.Values) {
	_, _ = w.Write([]byte(`{"device_code":"dev-secret","user_code":"BDWP-HQPK","verification_uri":"https://simkl.com/pin","verification_uri_complete":"https://simkl.com/pin?user_code=BDWP-HQPK","expires_in":900,"interval":5}`))
}

func TestStartAccountAuthFallsBackToPINForV1Apps(t *testing.T) {
	stub := &oauthStub{device: func(w http.ResponseWriter, _ url.Values) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","message":"This client_id is not enabled for OAuth 2.0"}`))
	}}
	c := installOAuthStub(t, stub)

	start, err := c.StartAccountAuth("acc-v1", "v1-client")
	if err != nil {
		t.Fatalf("StartAccountAuth: %v", err)
	}
	if start.Version != AuthVersionV1 || start.UserCode != "12345" || stub.pinCalls != 1 {
		t.Fatalf("start = %+v pinCalls=%d, want V1 PIN fallback", start, stub.pinCalls)
	}
}

func TestDeviceAuthPollsUntilApprovedAndHonoursSlowDown(t *testing.T) {
	polls := 0
	stub := &oauthStub{device: writeDevice, token: func(w http.ResponseWriter, form url.Values) {
		polls++
		if form.Get("device_code") != "dev-secret" || form.Get("client_id") != "v2-client" {
			t.Errorf("token form = %v", form)
		}
		switch polls {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"simkl_at_x","refresh_token":"simkl_rt_y","expires_in":604800,"scope":"media:read media:write","token_type":"Bearer"}`))
		}
	}}
	c := installOAuthStub(t, stub)

	start, err := c.StartAccountAuth("acc-v2", "v2-client")
	if err != nil {
		t.Fatalf("StartAccountAuth: %v", err)
	}
	if start.Version != AuthVersionV2 || start.VerificationURL != "https://simkl.com/pin?user_code=BDWP-HQPK" {
		t.Fatalf("start = %+v", start)
	}
	if scope := stub.forms[0].Get("scope"); scope != "media:read media:write" {
		t.Fatalf("device scope = %q", scope)
	}

	key := pendingAuthKey("acc-v2", start.UserCode)
	poll := func() *AuthResult {
		t.Helper()
		// Pretend the interval has passed so the poll reaches Simkl.
		pendingAuthMu.Lock()
		pendingAuths[key].lastPoll = time.Now().Add(-time.Hour)
		pendingAuthMu.Unlock()
		res, err := c.CheckAccountAuth("acc-v2", "v2-client", start.UserCode)
		if err != nil {
			t.Fatalf("CheckAccountAuth: %v", err)
		}
		return res
	}

	if res := poll(); res.Done {
		t.Fatal("authorization_pending reported as done")
	}
	if res := poll(); res.Done {
		t.Fatal("slow_down reported as done")
	}
	pendingAuthMu.Lock()
	interval := pendingAuths[key].interval
	pendingAuthMu.Unlock()
	if interval != 10*time.Second {
		t.Fatalf("interval after slow_down = %v, want 10s", interval)
	}
	// A poll inside the interval must not reach Simkl.
	if res, err := c.CheckAccountAuth("acc-v2", "v2-client", start.UserCode); err != nil || res.Done || polls != 2 {
		t.Fatalf("early poll: res=%+v err=%v polls=%d", res, err, polls)
	}
	res := poll()
	if !res.Done || res.Version != AuthVersionV2 || res.AccessToken != "simkl_at_x" || res.RefreshToken != "simkl_rt_y" || res.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("result = %+v", res)
	}
}

func TestEnsureValidTokenRefreshesNearExpiryV2Token(t *testing.T) {
	stub := &oauthStub{token: func(w http.ResponseWriter, form url.Values) {
		if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "simkl_rt_y" || form.Get("client_secret") != "" {
			t.Errorf("refresh form = %v", form)
		}
		_, _ = w.Write([]byte(`{"access_token":"simkl_at_new","refresh_token":"simkl_rt_y","expires_in":604800}`))
	}}
	c := installOAuthStub(t, stub)

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings, _ := mgr.Load()
	account := config.SimklAccount{ID: "acc", ClientID: "v2-client", AccessToken: "simkl_at_old", RefreshToken: "simkl_rt_y", AuthVersion: AuthVersionV2, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	settings.Simkl.Accounts = []config.SimklAccount{account}
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}

	token, err := c.EnsureValidToken(&account, mgr)
	if err != nil || token != "simkl_at_new" || account.AccessToken != "simkl_at_new" {
		t.Fatalf("token=%q account=%+v err=%v", token, account, err)
	}
	saved, _ := mgr.Load()
	if got := saved.Simkl.GetAccountByID("acc"); got.AccessToken != "simkl_at_new" || got.ExpiresAt < time.Now().Add(6*24*time.Hour).Unix() {
		t.Fatalf("saved account = %+v", got)
	}

	// A fresh token is not refreshed again.
	if _, err := c.EnsureValidToken(&account, mgr); err != nil || len(stub.forms) != 1 {
		t.Fatalf("second call refreshed again: forms=%d err=%v", len(stub.forms), err)
	}
}

func TestEnsureValidTokenLeavesV1TokensAlone(t *testing.T) {
	c := NewClient()
	account := config.SimklAccount{ID: "acc", ClientID: "v1", AccessToken: "v1-token"}
	token, err := c.EnsureValidToken(&account, nil)
	if err != nil || token != "v1-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
}
