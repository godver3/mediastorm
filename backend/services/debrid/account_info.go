package debrid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AccountInfo contains premium/subscription information for a debrid provider
type AccountInfo struct {
	Username        string     `json:"username"`
	Email           string     `json:"email,omitempty"`
	PremiumActive   bool       `json:"premium_active"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	DaysRemaining   int        `json:"days_remaining,omitempty"`
	IsLifetime      bool       `json:"is_lifetime,omitempty"`
	Error           string     `json:"error,omitempty"`
}

// GetAccountInfo returns account/subscription info for a Real-Debrid account
func (c *RealDebridClient) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("real-debrid API key not configured")
	}

	endpoint := fmt.Sprintf("%s/user", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build user request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return nil, fmt.Errorf("user request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("user request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var rdUser struct {
		ID         int    `json:"id"`
		Username   string `json:"username"`
		Email      string `json:"email"`
		Points     int    `json:"points"`
		Locale     string `json:"locale"`
		Avatar     string `json:"avatar"`
		Type       string `json:"type"`
		Premium    int64  `json:"premium"`    // Unix timestamp of expiration (0 = no premium)
		Expiration string `json:"expiration"` // ISO 8601 date string
	}

	if err := json.NewDecoder(resp.Body).Decode(&rdUser); err != nil {
		return nil, fmt.Errorf("decode user response: %w", err)
	}

	info := &AccountInfo{
		Username: rdUser.Username,
		Email:    rdUser.Email,
	}

	// Premium field is seconds remaining (0 = no premium/expired)
	// The expiration field contains the actual expiration date in ISO 8601
	if rdUser.Premium > 0 {
		info.PremiumActive = true
		// Use the expiration field which is an ISO 8601 date string
		if rdUser.Expiration != "" {
			if expiresAt, err := time.Parse(time.RFC3339, rdUser.Expiration); err == nil {
				info.ExpiresAt = &expiresAt
				info.DaysRemaining = int(time.Until(expiresAt).Hours() / 24)
			}
		}
		// Fallback: calculate from premium seconds if expiration parsing fails
		if info.ExpiresAt == nil {
			expiresAt := time.Now().Add(time.Duration(rdUser.Premium) * time.Second)
			info.ExpiresAt = &expiresAt
			info.DaysRemaining = int(time.Duration(rdUser.Premium).Hours() / 24)
		}
	}

	return info, nil
}

// GetAccountInfo returns account/subscription info for a TorBox account
func (c *TorboxClient) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("torbox API key not configured")
	}

	endpoint := fmt.Sprintf("%s/user/me", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build user request: %w", err)
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("user request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result struct {
		Success bool `json:"success"`
		Data    struct {
			ID               int    `json:"id"`
			Email            string `json:"email"`
			Plan             int    `json:"plan"`
			PremiumExpiresAt string `json:"premium_expires_at"` // ISO 8601
			TotalDownloaded  int64  `json:"total_downloaded"`
			IsSubscribed     bool   `json:"is_subscribed"`
		} `json:"data"`
		Detail string      `json:"detail"`
		Error  interface{} `json:"error,omitempty"` // Can be null, string, or object
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode user response: %w (body: %s)", err, string(body))
	}

	if !result.Success {
		return nil, fmt.Errorf("user request failed: %s", result.Detail)
	}

	info := &AccountInfo{
		Email: result.Data.Email,
	}

	// Derive username from email (TorBox doesn't have a username field)
	if result.Data.Email != "" {
		if atIdx := strings.Index(result.Data.Email, "@"); atIdx > 0 {
			info.Username = result.Data.Email[:atIdx]
		}
	}

	// Plan > 0 or is_subscribed means premium
	if result.Data.Plan > 0 || result.Data.IsSubscribed {
		info.PremiumActive = true
		if result.Data.PremiumExpiresAt != "" {
			if expiresAt, err := time.Parse(time.RFC3339, result.Data.PremiumExpiresAt); err == nil {
				info.ExpiresAt = &expiresAt
				info.DaysRemaining = int(time.Until(expiresAt).Hours() / 24)
				if info.DaysRemaining < 0 {
					info.DaysRemaining = 0
					info.PremiumActive = false
				}
			}
		}
	}

	return info, nil
}

// GetAccountInfo returns account/subscription info for an AllDebrid account
func (c *AllDebridClient) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("alldebrid API key not configured")
	}

	// AllDebrid uses query parameter auth, not Bearer header
	endpoint := fmt.Sprintf("%s/user?agent=%s&apikey=%s", c.baseURL, c.agent, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build user request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("user request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("alldebrid authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			User struct {
				Username          string `json:"username"`
				Email             string `json:"email"`
				IsPremium         bool   `json:"isPremium"`
				IsSubscribed      bool   `json:"isSubscribed"`
				IsTrial           bool   `json:"isTrial"`
				PremiumUntil      int64  `json:"premiumUntil"` // Unix timestamp
				Lang              string `json:"lang"`
				PreferedDomain    string `json:"preferedDomain"`
				FidelityPoints    int    `json:"fidelityPoints"`
				LimitedHostersQuota map[string]interface{} `json:"limitedHostersQuota,omitempty"`
			} `json:"user"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode user response: %w (body: %s)", err, string(body))
	}

	if result.Status != "success" {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Message
		}
		return nil, fmt.Errorf("user request failed: %s", errMsg)
	}

	info := &AccountInfo{
		Username:      result.Data.User.Username,
		Email:         result.Data.User.Email,
		PremiumActive: result.Data.User.IsPremium,
	}

	if result.Data.User.PremiumUntil > 0 {
		expiresAt := time.Unix(result.Data.User.PremiumUntil, 0)
		info.ExpiresAt = &expiresAt
		info.DaysRemaining = int(time.Until(expiresAt).Hours() / 24)
		if info.DaysRemaining < 0 {
			info.DaysRemaining = 0
			info.PremiumActive = false
		}
	}

	return info, nil
}
