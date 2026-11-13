package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"novastream/config"
	"novastream/internal/apierror"
	"novastream/internal/auth"
	"novastream/internal/parentauth"
	"novastream/models"
	"novastream/services/profileunlock"
	"novastream/services/users"

	"github.com/gorilla/mux"
)

type usersService interface {
	List() []models.User
	ListForAccount(accountID string) []models.User
	Create(name string) (models.User, error)
	CreateForAccount(accountID, name string) (models.User, error)
	BelongsToAccount(profileID, accountID string) bool
	Get(id string) (models.User, bool)
	Rename(id, name string) (models.User, error)
	SetColor(id, color string) (models.User, error)
	SetIconURL(id, iconURL string) (models.User, error)
	SetIconFile(id string, data []byte, contentType string) (models.User, error)
	ClearIconURL(id string) (models.User, error)
	GetIconPath(id string) (string, error)
	Delete(id string) error
	Exists(id string) bool
	SetPin(id, pin string) (models.User, error)
	ClearPin(id string) (models.User, error)
	VerifyPin(id, pin string) error
	HasPin(id string) bool
	SetMdblistAccountID(id, mdblistAccountID string) (models.User, error)
	ClearMdblistAccountID(id string) (models.User, error)
	SetTraktAccountID(id, traktAccountID string) (models.User, error)
	ClearTraktAccountID(id string) (models.User, error)
	SetSimklAccountID(id, simklAccountID string) (models.User, error)
	ClearSimklAccountID(id string) (models.User, error)
	SetScrobAccountID(id, scrobAccountID string) (models.User, error)
	ClearScrobAccountID(id string) (models.User, error)
	SetPlexAccountID(id, plexAccountID string) (models.User, error)
	ClearPlexAccountID(id string) (models.User, error)
	SetKidsProfile(id string, isKids bool) (models.User, error)
	SetKidsMode(id, mode string) (models.User, error)
	SetKidsMaxRating(id, rating string) (models.User, error)
	SetKidsMaxMovieRating(id, rating string) (models.User, error)
	SetKidsMaxTVRating(id, rating string) (models.User, error)
	SetKidsAllowedLists(id string, lists []string) (models.User, error)
	AddKidsAllowedList(id, listURL string) (models.User, error)
	RemoveKidsAllowedList(id, listURL string) (models.User, error)
}

var _ usersService = (*users.Service)(nil)

type UsersHandler struct {
	Service             usersService
	accounts            accountPasswordVerifier
	parentAuthorization *parentauth.Guard
	configManager       *config.Manager
	unlocks             profileUnlockIssuer
	unlockTTL           func() time.Duration
}

// profileUnlockIssuer issues and revokes server-side profile unlock tokens.
type profileUnlockIssuer interface {
	Issue(req profileunlock.IssueRequest) (string, time.Time, error)
	RevokeProfile(profileID string)
}

type pinFingerprinter interface {
	PinFingerprint(id string) string
}

// SetProfileUnlocks enables unlock-token issuance on PIN verification. ttl
// returns the configured unlock lifetime (0 = session lifetime).
func (h *UsersHandler) SetProfileUnlocks(unlocks profileUnlockIssuer, ttl func() time.Duration) {
	h.unlocks = unlocks
	h.unlockTTL = ttl
}

// VerifyPinResponse is returned by POST /api/users/{id}/pin/verify.
type VerifyPinResponse struct {
	Valid bool `json:"valid"`
	// UnlockToken proves the PIN was verified for this profile within the
	// calling session. Send it as X-Profile-Unlock (or ?profileUnlock=).
	UnlockToken string `json:"unlockToken,omitempty"`
	// ExpiresAt is when UnlockToken stops working (RFC 3339, UTC).
	ExpiresAt string `json:"expiresAt,omitempty"`
}

type accountPasswordVerifier interface {
	Get(id string) (models.Account, bool)
	Authenticate(username, password string) (models.Account, error)
}

func NewUsersHandler(service usersService, accountServices ...accountPasswordVerifier) *UsersHandler {
	handler := &UsersHandler{Service: service}
	if len(accountServices) > 0 {
		handler.accounts = accountServices[0]
		handler.parentAuthorization = parentauth.New(accountServices[0])
	}
	return handler
}

func (h *UsersHandler) SetConfigManager(configManager *config.Manager) {
	h.configManager = configManager
}

func (h *UsersHandler) canLinkIntegration(r *http.Request, kind, integrationID string) bool {
	if auth.IsMaster(r) || auth.GetAccountID(r) == "" {
		return true
	}
	if strings.TrimSpace(integrationID) == "" {
		return false
	}
	// Production wiring always supplies the config manager. Permit direct
	// handler invocations without one so isolated callers retain old behavior.
	if h.configManager == nil {
		return true
	}
	settings, err := h.configManager.Load()
	if err != nil {
		return false
	}
	accountID := auth.GetAccountID(r)
	switch kind {
	case "plex":
		account := settings.Plex.GetAccountByID(integrationID)
		return account != nil && account.OwnerAccountID == accountID
	case "trakt":
		account := settings.Trakt.GetAccountByID(integrationID)
		return account != nil && account.OwnerAccountID == accountID
	case "mdblist":
		account := settings.MDBList.GetAccountByID(integrationID)
		if account == nil {
			return false
		}
		if account.OwnerAccountID != "" {
			return account.OwnerAccountID == accountID
		}
		linkedToAccount := false
		for _, profile := range h.Service.List() {
			if profile.MdblistAccountID != integrationID {
				continue
			}
			if profile.AccountID != accountID {
				return false
			}
			linkedToAccount = true
		}
		return linkedToAccount
	case "simkl":
		account := settings.Simkl.GetAccountByID(integrationID)
		if account == nil {
			return false
		}
		if account.OwnerAccountID != "" {
			return account.OwnerAccountID == accountID
		}
		linkedToAccount := false
		for _, profile := range h.Service.List() {
			if profile.SimklAccountID != integrationID {
				continue
			}
			if profile.AccountID != accountID {
				return false
			}
			linkedToAccount = true
		}
		return linkedToAccount
	case "scrob":
		account := settings.Scrob.GetAccountByID(integrationID)
		return account != nil && account.OwnerAccountID == accountID
	}
	return false
}

func (h *UsersHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// All accounts (including master) only see their own profiles in the frontend app
	// Admin web UI uses a separate endpoint (/admin/api/profiles) to see all profiles
	accountID := auth.GetAccountID(r)
	json.NewEncoder(w).Encode(h.Service.ListForAccount(accountID))
}

func (h *UsersHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	accountID := auth.GetAccountID(r)
	if h.accounts == nil {
		http.Error(w, "account authentication is unavailable", http.StatusInternalServerError)
		return
	}
	account, ok := h.accounts.Get(accountID)
	if !ok {
		http.Error(w, "account not found", http.StatusUnauthorized)
		return
	}
	verified, err := h.accounts.Authenticate(account.Username, body.Password)
	if err != nil || verified.ID != accountID {
		http.Error(w, "invalid account password", http.StatusUnauthorized)
		return
	}

	// Create the profile only after re-authenticating the logged-in account.
	user, err := h.Service.CreateForAccount(accountID, body.Name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrNameRequired):
			status = http.StatusBadRequest
		case errors.Is(err, users.ErrStorageDirRequired):
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

func (h *UsersHandler) Rename(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.Rename(id, body.Name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrNameRequired):
			status = http.StatusBadRequest
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (h *UsersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	if rejectScheduledTaskProfileDeletion(w, h.configManager, id) {
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	if err := h.Service.Delete(id); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		case strings.Contains(err.Error(), "cannot delete the last user"):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *UsersHandler) SetColor(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		Color string `json:"color"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.SetColor(id, body.Color)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetIconURL downloads an image from the provided URL and sets it as the profile icon.
func (h *UsersHandler) SetIconURL(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		IconURL string `json:"iconUrl"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.SetIconURL(id, body.IconURL)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		case errors.Is(err, users.ErrInvalidIconURL):
			status = http.StatusBadRequest
		case errors.Is(err, users.ErrInvalidImageFormat):
			status = http.StatusBadRequest
		case errors.Is(err, users.ErrIconDownloadFailed):
			status = http.StatusBadGateway
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// UploadProfileIcon accepts a multipart form upload and sets it as the profile icon.
func (h *UsersHandler) UploadProfileIcon(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	// Parse multipart form with 5MB limit
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		http.Error(w, "file too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("icon")
	if err != nil {
		http.Error(w, "icon file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read file data
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	// Get content type from header or detect from file
	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}

	user, err := h.Service.SetIconFile(id, data, contentType)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		case errors.Is(err, users.ErrInvalidImageFormat):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearIconURL removes the profile icon.
func (h *UsersHandler) ClearIconURL(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearIconURL(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ServeProfileIcon serves the profile icon image file.
func (h *UsersHandler) ServeProfileIcon(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	iconPath, err := h.Service.GetIconPath(id)
	if err != nil {
		if errors.Is(err, users.ErrUserNotFound) {
			http.Error(w, "profile not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if iconPath == "" {
		http.Error(w, "no icon set for this profile", http.StatusNotFound)
		return
	}

	// Determine content type from extension
	contentType := "image/png"
	if strings.HasSuffix(iconPath, ".jpg") || strings.HasSuffix(iconPath, ".jpeg") {
		contentType = "image/jpeg"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, iconPath)
}

func (h *UsersHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// SetPin sets or updates a user's PIN.
func (h *UsersHandler) SetPin(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		Pin string `json:"pin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.SetPin(id, body.Pin)
	if err == nil && h.unlocks != nil {
		h.unlocks.RevokeProfile(id)
	}
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		case errors.Is(err, users.ErrPinRequired), errors.Is(err, users.ErrPinTooShort):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearPin removes a user's PIN.
func (h *UsersHandler) ClearPin(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.ClearPin(id)
	if err == nil && h.unlocks != nil {
		h.unlocks.RevokeProfile(id)
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// VerifyPin verifies a user's PIN.
func (h *UsersHandler) VerifyPin(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	var body struct {
		Pin string `json:"pin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := h.Service.VerifyPin(id, body.Pin)
	if err != nil {
		switch {
		case errors.Is(err, users.ErrUserNotFound):
			apierror.Write(w, http.StatusNotFound, "profile_not_found", err.Error())
		case errors.Is(err, users.ErrPinInvalid):
			// Stays 401 with the original "invalid PIN" text: shipped Expo builds
			// detect a wrong PIN by status 401 and the tvOS client by the word
			// "pin" in the body. The session itself remains valid.
			code := apierror.CodePinInvalid
			if strings.TrimSpace(body.Pin) == "" {
				code = apierror.CodePinRequired
			}
			apierror.Write(w, http.StatusUnauthorized, code, err.Error())
		default:
			apierror.Write(w, http.StatusInternalServerError, "", err.Error())
		}
		return
	}

	resp := VerifyPinResponse{Valid: true}
	if h.unlocks != nil {
		if session, ok := auth.GetSession(r); ok && session.Token != "" {
			var ttl time.Duration
			if h.unlockTTL != nil {
				ttl = h.unlockTTL()
			}
			fingerprint := ""
			if fp, ok := h.Service.(pinFingerprinter); ok {
				fingerprint = fp.PinFingerprint(id)
			}
			token, expiresAt, issueErr := h.unlocks.Issue(profileunlock.IssueRequest{
				SessionToken:     session.Token,
				SessionExpiresAt: session.ExpiresAt,
				AccountID:        session.AccountID,
				ProfileID:        id,
				PinFingerprint:   fingerprint,
				TTL:              ttl,
			})
			if issueErr != nil {
				log.Printf("[users] failed to issue profile unlock for %s: %v", id, issueErr)
			} else {
				resp.UnlockToken = token
				resp.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// SetTraktAccount associates a Trakt account with a user profile.
func (h *UsersHandler) SetTraktAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		TraktAccountID string `json:"traktAccountId"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.canLinkIntegration(r, "trakt", body.TraktAccountID) {
		http.Error(w, "integration account not found", http.StatusNotFound)
		return
	}
	user, err := h.Service.SetTraktAccountID(id, body.TraktAccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearTraktAccount removes the Trakt account association from a user profile.
func (h *UsersHandler) ClearTraktAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearTraktAccountID(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetMdblistAccount associates an MDBList account with a user profile.
func (h *UsersHandler) SetMdblistAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		MdblistAccountID string `json:"mdblistAccountId"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.canLinkIntegration(r, "mdblist", body.MdblistAccountID) {
		http.Error(w, "integration account not found", http.StatusNotFound)
		return
	}
	user, err := h.Service.SetMdblistAccountID(id, body.MdblistAccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearMdblistAccount removes the MDBList account association from a user profile.
func (h *UsersHandler) ClearMdblistAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearMdblistAccountID(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetSimklAccount associates a Simkl account with a user profile.
func (h *UsersHandler) SetSimklAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		SimklAccountID string `json:"simklAccountId"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.canLinkIntegration(r, "simkl", body.SimklAccountID) {
		http.Error(w, "integration account not found", http.StatusNotFound)
		return
	}
	user, err := h.Service.SetSimklAccountID(id, body.SimklAccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearSimklAccount removes the Simkl account association from a user profile.
func (h *UsersHandler) ClearSimklAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearSimklAccountID(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetScrobAccount associates a self-hosted Scrob account with a user profile.
func (h *UsersHandler) SetScrobAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}
	if !auth.IsMaster(r) {
		if !h.Service.BelongsToAccount(id, auth.GetAccountID(r)) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	var body struct {
		ScrobAccountID string `json:"scrobAccountId"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.canLinkIntegration(r, "scrob", body.ScrobAccountID) {
		http.Error(w, "integration account not found", http.StatusNotFound)
		return
	}
	user, err := h.Service.SetScrobAccountID(id, body.ScrobAccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(user)
}

// ClearScrobAccount removes the Scrob account association from a user profile.
func (h *UsersHandler) ClearScrobAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}
	if !auth.IsMaster(r) {
		if !h.Service.BelongsToAccount(id, auth.GetAccountID(r)) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	user, err := h.Service.ClearScrobAccountID(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(user)
}

// SetPlexAccount associates a Plex account with a user profile.
func (h *UsersHandler) SetPlexAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		PlexAccountID string `json:"plexAccountId"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.canLinkIntegration(r, "plex", body.PlexAccountID) {
		http.Error(w, "integration account not found", http.StatusNotFound)
		return
	}
	user, err := h.Service.SetPlexAccountID(id, body.PlexAccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearPlexAccount removes the Plex account association from a user profile.
func (h *UsersHandler) ClearPlexAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearPlexAccountID(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetKidsProfile sets or clears the kids profile flag for a user.
func (h *UsersHandler) SetKidsProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		IsKidsProfile bool `json:"isKidsProfile"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.SetKidsProfile(id, body.IsKidsProfile)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// canConfigureKidsProfile checks if the caller can configure a kids profile's settings.
// Master accounts can configure any profile. Regular accounts can only configure profiles
// that belong to their own account.
// Each mutation also requires fresh parental authorization before writing.
func (h *UsersHandler) canConfigureKidsProfile(r *http.Request, profileID string) bool {
	// Master accounts can configure any profile's kids settings
	if auth.IsMaster(r) {
		_, ok := h.Service.Get(profileID)
		return ok
	}

	// Get the target profile
	_, ok := h.Service.Get(profileID)
	if !ok {
		return false
	}

	// Check if caller is in the same account
	accountID := auth.GetAccountID(r)
	return h.Service.BelongsToAccount(profileID, accountID)
}

// SetKidsMode sets the kids restriction mode for a profile.
func (h *UsersHandler) SetKidsMode(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify caller can configure this profile
	if !h.canConfigureKidsProfile(r, id) {
		http.Error(w, "cannot configure kids settings for this profile", http.StatusForbidden)
		return
	}

	var body struct {
		Mode string `json:"mode"` // "rating", "content_list", or ""
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Validate mode
	validModes := map[string]bool{"": true, "rating": true, "content_list": true}
	if !validModes[body.Mode] {
		http.Error(w, "invalid mode, must be 'rating', 'content_list', or empty", http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.SetKidsMode(id, body.Mode)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetKidsMaxRating sets the maximum content rating for a kids profile.
func (h *UsersHandler) SetKidsMaxRating(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify caller can configure this profile
	if !h.canConfigureKidsProfile(r, id) {
		http.Error(w, "cannot configure kids settings for this profile", http.StatusForbidden)
		return
	}

	var body struct {
		Rating string `json:"rating"` // G, PG, PG-13, R, NC-17, TV-Y, TV-Y7, TV-G, TV-PG, TV-14, TV-MA
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.SetKidsMaxRating(id, body.Rating)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetKidsMaxMovieRating sets the maximum movie rating for a kids profile.
func (h *UsersHandler) SetKidsMaxMovieRating(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify caller can configure this profile
	if !h.canConfigureKidsProfile(r, id) {
		http.Error(w, "cannot configure kids settings for this profile", http.StatusForbidden)
		return
	}

	var body struct {
		Rating string `json:"rating"` // G, PG, PG-13, R, NC-17
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.SetKidsMaxMovieRating(id, body.Rating)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetKidsMaxTVRating sets the maximum TV rating for a kids profile.
func (h *UsersHandler) SetKidsMaxTVRating(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify caller can configure this profile
	if !h.canConfigureKidsProfile(r, id) {
		http.Error(w, "cannot configure kids settings for this profile", http.StatusForbidden)
		return
	}

	var body struct {
		Rating string `json:"rating"` // TV-Y, TV-Y7, TV-G, TV-PG, TV-14, TV-MA
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.SetKidsMaxTVRating(id, body.Rating)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetKidsAllowedLists replaces the allowed lists for a kids profile.
func (h *UsersHandler) SetKidsAllowedLists(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify caller can configure this profile
	if !h.canConfigureKidsProfile(r, id) {
		http.Error(w, "cannot configure kids settings for this profile", http.StatusForbidden)
		return
	}

	var body struct {
		Lists []string `json:"lists"` // MDBList URLs
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.SetKidsAllowedLists(id, body.Lists)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// AddKidsAllowedList adds a list to the allowed lists for a kids profile.
func (h *UsersHandler) AddKidsAllowedList(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify caller can configure this profile
	if !h.canConfigureKidsProfile(r, id) {
		http.Error(w, "cannot configure kids settings for this profile", http.StatusForbidden)
		return
	}

	var body struct {
		URL string `json:"url"` // MDBList URL to add
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(body.URL) == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.AddKidsAllowedList(id, body.URL)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// RemoveKidsAllowedList removes a list from the allowed lists for a kids profile.
func (h *UsersHandler) RemoveKidsAllowedList(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify caller can configure this profile
	if !h.canConfigureKidsProfile(r, id) {
		http.Error(w, "cannot configure kids settings for this profile", http.StatusForbidden)
		return
	}

	// Get URL from query parameter
	listURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if listURL == "" {
		http.Error(w, "url query parameter is required", http.StatusBadRequest)
		return
	}

	if !h.parentAuthorization.Require(w, r) {
		return
	}

	user, err := h.Service.RemoveKidsAllowedList(id, listURL)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}
