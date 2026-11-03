package simkl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"novastream/config"
)

// Simkl AUTH V2 (OAuth 2.0 device flow). New Simkl apps are V2-only: their
// client IDs are rejected by the V1 PIN endpoints, and V1 client IDs are
// rejected here with 401 invalid_client. Accounts try V2 first and fall back to
// the V1 PIN flow, so both kinds of app keep working.

const (
	authVersionV2 = "v2"
	// AuthVersionV1 marks a V1 (PIN) sign-in in AuthStart/AuthResult.
	AuthVersionV1 = "v1"
	// AuthVersionV2 marks an AUTH V2 grant (refreshable 7-day tokens).
	AuthVersionV2 = authVersionV2

	oauth2Scope = "media:read media:write"
	// refreshLeeway refreshes a V2 access token this long before it expires.
	refreshLeeway = 24 * time.Hour
	// slowDownBackoff is what Simkl asks a device to add after slow_down.
	slowDownBackoff = 5 * time.Second
)

// ErrNotV2Client means the client ID belongs to an AUTH V1 app.
var ErrNotV2Client = errors.New("simkl client is not an AUTH V2 app")

// OAuth2Token is a token-endpoint response.
type OAuth2Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

type deviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type oauth2Error struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Message          string `json:"message"`
}

// AuthStart is what the UI shows to connect an account. The V2 device code
// stays on the server (it is the polling credential and must not be shown).
type AuthStart struct {
	Version         string
	UserCode        string
	VerificationURL string
	ExpiresIn       int
	Interval        int
}

// AuthResult is one poll of an account sign-in. Done is false while pending.
type AuthResult struct {
	Done         bool
	Version      string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	UserID       string
}

type pendingDeviceAuth struct {
	clientID   string
	deviceCode string
	interval   time.Duration
	lastPoll   time.Time
	expiresAt  time.Time
}

var (
	pendingAuthMu sync.Mutex
	pendingAuths  = map[string]*pendingDeviceAuth{}

	refreshMuGuard sync.Mutex
	refreshMus     = map[string]*sync.Mutex{}
)

func pendingAuthKey(accountID, userCode string) string {
	return accountID + "|" + strings.ToUpper(strings.TrimSpace(userCode))
}

// StartAccountAuth starts a sign-in for an account: the AUTH V2 device flow,
// or the V1 PIN flow when the client ID belongs to a V1 app.
func (c *Client) StartAccountAuth(accountID, clientID string) (*AuthStart, error) {
	device, err := c.startDeviceAuth(clientID)
	if errors.Is(err, ErrNotV2Client) {
		pin, pinErr := c.StartPINAuth(clientID)
		if pinErr != nil {
			return nil, pinErr
		}
		return &AuthStart{Version: AuthVersionV1, UserCode: pin.UserCode, VerificationURL: pin.VerificationURL, ExpiresIn: pin.ExpiresIn, Interval: pin.Interval}, nil
	}
	if err != nil {
		return nil, err
	}

	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expiresIn := device.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	now := time.Now()
	pendingAuthMu.Lock()
	for key, pending := range pendingAuths {
		if now.After(pending.expiresAt) {
			delete(pendingAuths, key)
		}
	}
	pendingAuths[pendingAuthKey(accountID, device.UserCode)] = &pendingDeviceAuth{
		clientID:   clientID,
		deviceCode: device.DeviceCode,
		interval:   interval,
		lastPoll:   now,
		expiresAt:  now.Add(time.Duration(expiresIn) * time.Second),
	}
	pendingAuthMu.Unlock()

	verification := device.VerificationURIComplete
	if verification == "" {
		verification = device.VerificationURI
	}
	return &AuthStart{Version: AuthVersionV2, UserCode: device.UserCode, VerificationURL: verification, ExpiresIn: expiresIn, Interval: int(interval / time.Second)}, nil
}

// CheckAccountAuth polls a sign-in started by StartAccountAuth. A user code
// with no pending V2 device code is a V1 PIN.
func (c *Client) CheckAccountAuth(accountID, clientID, userCode string) (*AuthResult, error) {
	key := pendingAuthKey(accountID, userCode)
	pendingAuthMu.Lock()
	pending := pendingAuths[key]
	pendingAuthMu.Unlock()

	if pending == nil {
		token, err := c.CheckPINAuth(clientID, userCode)
		if err != nil {
			return nil, err
		}
		if token.AccessToken == "" {
			return &AuthResult{Version: AuthVersionV1}, nil
		}
		return &AuthResult{Done: true, Version: AuthVersionV1, AccessToken: token.AccessToken, UserID: token.UserID}, nil
	}

	now := time.Now()
	if now.After(pending.expiresAt) {
		pendingAuthMu.Lock()
		delete(pendingAuths, key)
		pendingAuthMu.Unlock()
		return nil, errors.New("simkl device code expired; start again")
	}
	// Simkl resets its poll window on every attempt, including rejected ones,
	// so never poll faster than the interval it asked for.
	pendingAuthMu.Lock()
	tooSoon := now.Sub(pending.lastPoll) < pending.interval
	if !tooSoon {
		pending.lastPoll = now
	}
	pendingAuthMu.Unlock()
	if tooSoon {
		return &AuthResult{Version: AuthVersionV2}, nil
	}

	token, status, err := c.tokenRequest(url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {pending.clientID},
		"device_code": {pending.deviceCode},
	})
	if err != nil {
		switch status {
		case "authorization_pending":
			return &AuthResult{Version: AuthVersionV2}, nil
		case "slow_down":
			pendingAuthMu.Lock()
			pending.interval += slowDownBackoff
			pendingAuthMu.Unlock()
			return &AuthResult{Version: AuthVersionV2}, nil
		case "expired_token":
			pendingAuthMu.Lock()
			delete(pendingAuths, key)
			pendingAuthMu.Unlock()
			return nil, errors.New("simkl device code expired; start again")
		}
		return nil, err
	}

	pendingAuthMu.Lock()
	delete(pendingAuths, key)
	pendingAuthMu.Unlock()
	if !strings.Contains(token.Scope, "media:write") {
		log.Printf("[simkl] warning: V2 grant scope %q has no media:write; syncing to Simkl will fail", token.Scope)
	}
	return &AuthResult{Done: true, Version: AuthVersionV2, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: expiryFrom(token.ExpiresIn)}, nil
}

// EnsureValidToken returns a usable access token for the account, refreshing
// an AUTH V2 token that is near expiry and saving it. V1 tokens are returned
// as-is. The account struct is updated in place.
//
// Simkl keeps one live access token per grant and a refresh replaces it, so
// refreshes are serialised per account and re-read the saved account first.
func (c *Client) EnsureValidToken(account *config.SimklAccount, configManager *config.Manager) (string, error) {
	if account == nil {
		return "", nil
	}
	if !needsRefresh(account) {
		return account.AccessToken, nil
	}

	mu := accountRefreshMu(account.ID)
	mu.Lock()
	defer mu.Unlock()

	settings, err := configManager.Load()
	if err != nil {
		return "", fmt.Errorf("load config for simkl token refresh: %w", err)
	}
	fresh := settings.Simkl.GetAccountByID(account.ID)
	if fresh == nil {
		return "", fmt.Errorf("simkl account %s not found", account.ID)
	}
	if !needsRefresh(fresh) {
		copyTokens(account, fresh)
		return fresh.AccessToken, nil
	}

	log.Printf("[simkl] Refreshing AUTH V2 token for account %s (%s)", fresh.Name, fresh.ID)
	token, _, err := c.tokenRequest(url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {fresh.ClientID},
		"refresh_token": {fresh.RefreshToken},
	})
	if err != nil {
		return "", fmt.Errorf("refresh simkl token: %w", err)
	}
	fresh.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		fresh.RefreshToken = token.RefreshToken // non-rotating, but harmless to store
	}
	fresh.ExpiresAt = expiryFrom(token.ExpiresIn)
	settings.Simkl.UpdateAccount(*fresh)
	if err := configManager.Save(settings); err != nil {
		return "", fmt.Errorf("save refreshed simkl token: %w", err)
	}
	copyTokens(account, fresh)
	return fresh.AccessToken, nil
}

// RevokeToken ends an AUTH V2 grant. Simkl always answers 200, so errors are
// only transport failures.
func (c *Client) RevokeToken(clientID, token string) error {
	if clientID == "" || token == "" {
		return nil
	}
	_, err := c.postForm("/oauth2/revoke", url.Values{"client_id": {clientID}, "token": {token}})
	return err
}

func needsRefresh(account *config.SimklAccount) bool {
	if account.AuthVersion != authVersionV2 || account.RefreshToken == "" {
		return false
	}
	return account.AccessToken == "" || account.ExpiresAt == 0 || time.Until(time.Unix(account.ExpiresAt, 0)) < refreshLeeway
}

func copyTokens(dst, src *config.SimklAccount) {
	dst.AccessToken = src.AccessToken
	dst.RefreshToken = src.RefreshToken
	dst.ExpiresAt = src.ExpiresAt
	dst.AuthVersion = src.AuthVersion
}

func expiryFrom(expiresIn int64) int64 {
	if expiresIn <= 0 {
		expiresIn = int64((7 * 24 * time.Hour) / time.Second)
	}
	return time.Now().Unix() + expiresIn
}

func accountRefreshMu(accountID string) *sync.Mutex {
	refreshMuGuard.Lock()
	defer refreshMuGuard.Unlock()
	mu, ok := refreshMus[accountID]
	if !ok {
		mu = &sync.Mutex{}
		refreshMus[accountID] = mu
	}
	return mu
}

func (c *Client) startDeviceAuth(clientID string) (*deviceAuthResponse, error) {
	body, err := c.postForm("/oauth2/device", url.Values{"client_id": {clientID}, "scope": {oauth2Scope}})
	if err != nil {
		return nil, err
	}
	var out deviceAuthResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode simkl device response: %w", err)
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return nil, errors.New("simkl device response missing device or user code")
	}
	return &out, nil
}

// tokenRequest posts to /oauth2/token. On an OAuth error it returns the error
// code (authorization_pending, slow_down, …) alongside err.
func (c *Client) tokenRequest(form url.Values) (*OAuth2Token, string, error) {
	body, err := c.postForm("/oauth2/token", form)
	if err != nil {
		var oe *oauth2RequestError
		if errors.As(err, &oe) {
			return nil, oe.code, err
		}
		return nil, "", err
	}
	var token OAuth2Token
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, "", fmt.Errorf("decode simkl token response: %w", err)
	}
	if token.AccessToken == "" {
		return nil, "", errors.New("simkl token response has no access token")
	}
	return &token, "", nil
}

type oauth2RequestError struct {
	path   string
	status int
	code   string
	detail string
}

func (e *oauth2RequestError) Error() string {
	return fmt.Sprintf("simkl %s returned %d %s: %s", e.path, e.status, e.code, e.detail)
}

func (c *Client) postForm(path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, apiBaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simkl %s request: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	var oe oauth2Error
	_ = json.Unmarshal(body, &oe)
	if path == "/oauth2/device" && resp.StatusCode == http.StatusUnauthorized && oe.Error == "invalid_client" {
		return nil, ErrNotV2Client
	}
	detail := oe.ErrorDescription
	if detail == "" {
		detail = oe.Message
	}
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}
	return nil, &oauth2RequestError{path: path, status: resp.StatusCode, code: oe.Error, detail: detail}
}
