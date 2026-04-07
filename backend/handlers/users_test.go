package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/handlers"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/users"

	"github.com/gorilla/mux"
)

// fakeUsersService implements the usersService interface for testing.
type fakeUsersService struct {
	listForAccountUsers []models.User
	createUser          models.User
	createErr           error
	belongsTo           bool
	getUser             models.User
	getOK               bool
	existsVal           bool
	renameUser          models.User
	renameErr           error
	deleteErr           error
	setColorUser        models.User
	setColorErr         error
	setPinUser          models.User
	setPinErr           error
	clearPinUser        models.User
	clearPinErr         error
	verifyPinErr        error
	hasPinVal           bool
	setTraktUser        models.User
	setTraktErr         error
	clearTraktUser      models.User
	clearTraktErr       error
	setPlexUser         models.User
	setPlexErr          error
	clearPlexUser       models.User
	clearPlexErr        error
	setKidsUser         models.User
	setKidsErr          error
	setKidsModeUser     models.User
	setKidsModeErr      error
	setIconURLUser      models.User
	setIconURLErr       error
	clearIconURLUser    models.User
	clearIconURLErr     error
	setIconFileUser     models.User
	setIconFileErr      error
	iconPath            string
	iconPathErr         error
	setMaxRatingUser    models.User
	setMaxRatingErr     error
	setMaxMovieUser     models.User
	setMaxMovieErr      error
	setMaxTVUser        models.User
	setMaxTVErr         error
	setAllowedUser      models.User
	setAllowedErr       error
	addAllowedUser      models.User
	addAllowedErr       error
	removeAllowedUser   models.User
	removeAllowedErr    error
}

func (f *fakeUsersService) List() []models.User                      { return nil }
func (f *fakeUsersService) ListForAccount(accountID string) []models.User {
	return f.listForAccountUsers
}
func (f *fakeUsersService) Create(name string) (models.User, error) {
	return f.createUser, f.createErr
}
func (f *fakeUsersService) CreateForAccount(accountID, name string) (models.User, error) {
	return f.createUser, f.createErr
}
func (f *fakeUsersService) BelongsToAccount(profileID, accountID string) bool {
	return f.belongsTo
}
func (f *fakeUsersService) Get(id string) (models.User, bool) { return f.getUser, f.getOK }
func (f *fakeUsersService) Rename(id, name string) (models.User, error) {
	return f.renameUser, f.renameErr
}
func (f *fakeUsersService) SetColor(id, color string) (models.User, error) {
	return f.setColorUser, f.setColorErr
}
func (f *fakeUsersService) SetIconURL(id, iconURL string) (models.User, error) {
	return f.setIconURLUser, f.setIconURLErr
}
func (f *fakeUsersService) SetIconFile(id string, data []byte, contentType string) (models.User, error) {
	return f.setIconFileUser, f.setIconFileErr
}
func (f *fakeUsersService) ClearIconURL(id string) (models.User, error) {
	return f.clearIconURLUser, f.clearIconURLErr
}
func (f *fakeUsersService) GetIconPath(id string) (string, error) {
	return f.iconPath, f.iconPathErr
}
func (f *fakeUsersService) Delete(id string) error { return f.deleteErr }
func (f *fakeUsersService) Exists(id string) bool  { return f.existsVal }
func (f *fakeUsersService) SetPin(id, pin string) (models.User, error) {
	return f.setPinUser, f.setPinErr
}
func (f *fakeUsersService) ClearPin(id string) (models.User, error) {
	return f.clearPinUser, f.clearPinErr
}
func (f *fakeUsersService) VerifyPin(id, pin string) error { return f.verifyPinErr }
func (f *fakeUsersService) HasPin(id string) bool          { return f.hasPinVal }
func (f *fakeUsersService) SetMdblistAccountID(id, mdblistAccountID string) (models.User, error) {
	return models.User{}, nil
}
func (f *fakeUsersService) ClearMdblistAccountID(id string) (models.User, error) {
	return models.User{}, nil
}
func (f *fakeUsersService) SetTraktAccountID(id, traktAccountID string) (models.User, error) {
	return f.setTraktUser, f.setTraktErr
}
func (f *fakeUsersService) ClearTraktAccountID(id string) (models.User, error) {
	return f.clearTraktUser, f.clearTraktErr
}
func (f *fakeUsersService) SetPlexAccountID(id, plexAccountID string) (models.User, error) {
	return f.setPlexUser, f.setPlexErr
}
func (f *fakeUsersService) ClearPlexAccountID(id string) (models.User, error) {
	return f.clearPlexUser, f.clearPlexErr
}
func (f *fakeUsersService) SetKidsProfile(id string, isKids bool) (models.User, error) {
	return f.setKidsUser, f.setKidsErr
}
func (f *fakeUsersService) SetKidsMode(id, mode string) (models.User, error) {
	return f.setKidsModeUser, f.setKidsModeErr
}
func (f *fakeUsersService) SetKidsMaxRating(id, rating string) (models.User, error) {
	return f.setMaxRatingUser, f.setMaxRatingErr
}
func (f *fakeUsersService) SetKidsMaxMovieRating(id, rating string) (models.User, error) {
	return f.setMaxMovieUser, f.setMaxMovieErr
}
func (f *fakeUsersService) SetKidsMaxTVRating(id, rating string) (models.User, error) {
	return f.setMaxTVUser, f.setMaxTVErr
}
func (f *fakeUsersService) SetKidsAllowedLists(id string, lists []string) (models.User, error) {
	return f.setAllowedUser, f.setAllowedErr
}
func (f *fakeUsersService) AddKidsAllowedList(id, listURL string) (models.User, error) {
	return f.addAllowedUser, f.addAllowedErr
}
func (f *fakeUsersService) RemoveKidsAllowedList(id, listURL string) (models.User, error) {
	return f.removeAllowedUser, f.removeAllowedErr
}

// helper to build a request with mux vars and auth context
func usersRequest(method, path string, body any, vars map[string]string, accountID string, isMaster bool) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if len(vars) > 0 {
		r = mux.SetURLVars(r, vars)
	}
	ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, accountID)
	ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, isMaster)
	return r.WithContext(ctx)
}

func TestUsersHandler_List(t *testing.T) {
	expected := []models.User{{ID: "u1", Name: "Alice"}}
	svc := &fakeUsersService{listForAccountUsers: expected}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodGet, "/api/users/u1/profiles", nil, nil, "acct-1", false)
	w := httptest.NewRecorder()
	h.List(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestUsersHandler_Create_Success(t *testing.T) {
	expected := models.User{ID: "u2", Name: "Bob"}
	svc := &fakeUsersService{createUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"name": "Bob"}
	r := usersRequest(http.MethodPost, "/api/users/u1/profiles", body, nil, "acct-1", false)
	w := httptest.NewRecorder()
	h.Create(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestUsersHandler_Create_EmptyName(t *testing.T) {
	svc := &fakeUsersService{createErr: users.ErrNameRequired}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"name": ""}
	r := usersRequest(http.MethodPost, "/api/users/u1/profiles", body, nil, "acct-1", false)
	w := httptest.NewRecorder()
	h.Create(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUsersHandler_Create_InvalidJSON(t *testing.T) {
	svc := &fakeUsersService{}
	h := handlers.NewUsersHandler(svc)

	r := httptest.NewRequest(http.MethodPost, "/api/users/u1/profiles", bytes.NewBufferString("{bad"))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "acct-1")
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	h.Create(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUsersHandler_Rename_Success(t *testing.T) {
	expected := models.User{ID: "u1", Name: "NewName"}
	svc := &fakeUsersService{belongsTo: true, renameUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"name": "NewName"}
	r := usersRequest(http.MethodPut, "/api/users/u1/profiles/u1/name", body,
		map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.Rename(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_Rename_NotOwned(t *testing.T) {
	svc := &fakeUsersService{belongsTo: false}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"name": "NewName"}
	r := usersRequest(http.MethodPut, "/api/users/u1/profiles/u1/name", body,
		map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.Rename(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_Rename_EmptyUserID(t *testing.T) {
	svc := &fakeUsersService{}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"name": "NewName"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": ""}, "acct-1", false)
	w := httptest.NewRecorder()
	h.Rename(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUsersHandler_Rename_NotFound(t *testing.T) {
	svc := &fakeUsersService{belongsTo: true, renameErr: users.ErrUserNotFound}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"name": "NewName"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.Rename(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_Delete_Success(t *testing.T) {
	svc := &fakeUsersService{belongsTo: true}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodDelete, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.Delete(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestUsersHandler_Delete_NotOwned(t *testing.T) {
	svc := &fakeUsersService{belongsTo: false}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodDelete, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.Delete(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_Delete_NotFound(t *testing.T) {
	svc := &fakeUsersService{belongsTo: true, deleteErr: users.ErrUserNotFound}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodDelete, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.Delete(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_SetColor_Success(t *testing.T) {
	expected := models.User{ID: "u1", Color: "#ff0000"}
	svc := &fakeUsersService{belongsTo: true, setColorUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"color": "#ff0000"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetColor(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetColor_NotOwned(t *testing.T) {
	svc := &fakeUsersService{belongsTo: false}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"color": "#ff0000"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetColor(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_SetPin_Success(t *testing.T) {
	expected := models.User{ID: "u1"}
	svc := &fakeUsersService{belongsTo: true, setPinUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"pin": "1234"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetPin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetPin_TooShort(t *testing.T) {
	svc := &fakeUsersService{belongsTo: true, setPinErr: users.ErrPinTooShort}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"pin": "12"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetPin(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUsersHandler_ClearPin_Success(t *testing.T) {
	expected := models.User{ID: "u1"}
	svc := &fakeUsersService{belongsTo: true, clearPinUser: expected}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodDelete, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.ClearPin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_VerifyPin_Success(t *testing.T) {
	svc := &fakeUsersService{verifyPinErr: nil}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"pin": "1234"}
	r := usersRequest(http.MethodPost, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.VerifyPin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_VerifyPin_Invalid(t *testing.T) {
	svc := &fakeUsersService{verifyPinErr: users.ErrPinInvalid}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"pin": "wrong"}
	r := usersRequest(http.MethodPost, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.VerifyPin(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestUsersHandler_VerifyPin_UserNotFound(t *testing.T) {
	svc := &fakeUsersService{verifyPinErr: users.ErrUserNotFound}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"pin": "1234"}
	r := usersRequest(http.MethodPost, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.VerifyPin(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_SetTraktAccount_Success(t *testing.T) {
	expected := models.User{ID: "u1", TraktAccountID: "trakt-1"}
	svc := &fakeUsersService{belongsTo: true, setTraktUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"traktAccountId": "trakt-1"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetTraktAccount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetTraktAccount_MasterBypass(t *testing.T) {
	expected := models.User{ID: "u1", TraktAccountID: "trakt-1"}
	svc := &fakeUsersService{existsVal: true, setTraktUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"traktAccountId": "trakt-1"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", true)
	w := httptest.NewRecorder()
	h.SetTraktAccount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetTraktAccount_MasterNotExists(t *testing.T) {
	svc := &fakeUsersService{existsVal: false}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"traktAccountId": "trakt-1"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", true)
	w := httptest.NewRecorder()
	h.SetTraktAccount(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_ClearTraktAccount_Success(t *testing.T) {
	expected := models.User{ID: "u1"}
	svc := &fakeUsersService{belongsTo: true, clearTraktUser: expected}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodDelete, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.ClearTraktAccount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetPlexAccount_Success(t *testing.T) {
	expected := models.User{ID: "u1", PlexAccountID: "plex-1"}
	svc := &fakeUsersService{belongsTo: true, setPlexUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"plexAccountId": "plex-1"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetPlexAccount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_ClearPlexAccount_Success(t *testing.T) {
	expected := models.User{ID: "u1"}
	svc := &fakeUsersService{belongsTo: true, clearPlexUser: expected}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodDelete, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.ClearPlexAccount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetKidsProfile_Success(t *testing.T) {
	expected := models.User{ID: "u1", IsKidsProfile: true}
	svc := &fakeUsersService{belongsTo: true, setKidsUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]bool{"isKidsProfile": true}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetKidsProfile(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetKidsProfile_MasterBypass(t *testing.T) {
	// Master account should be able to configure kids profiles on non-default accounts
	// even when BelongsToAccount returns false (different account)
	expected := models.User{ID: "u1", IsKidsProfile: true}
	svc := &fakeUsersService{existsVal: true, belongsTo: false, setKidsUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]bool{"isKidsProfile": true}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "master", true)
	w := httptest.NewRecorder()
	h.SetKidsProfile(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (master should bypass ownership check)", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetKidsMode_Success(t *testing.T) {
	expected := models.User{ID: "u1", KidsMode: "rating"}
	svc := &fakeUsersService{getOK: true, getUser: models.User{ID: "u1"}, belongsTo: true, setKidsModeUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"mode": "rating"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetKidsMode(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_SetKidsMode_InvalidMode(t *testing.T) {
	svc := &fakeUsersService{getOK: true, getUser: models.User{ID: "u1"}, belongsTo: true}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"mode": "invalid_mode"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetKidsMode(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUsersHandler_SetKidsMode_Forbidden(t *testing.T) {
	svc := &fakeUsersService{getOK: true, getUser: models.User{ID: "u1"}, belongsTo: false}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"mode": "rating"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.SetKidsMode(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestUsersHandler_SetKidsMode_MasterBypass(t *testing.T) {
	// Master account should be able to configure kids mode on any profile,
	// even when BelongsToAccount returns false (profile belongs to non-default account)
	expected := models.User{ID: "u1", KidsMode: "rating"}
	svc := &fakeUsersService{getOK: true, getUser: models.User{ID: "u1"}, belongsTo: false, setKidsModeUser: expected}
	h := handlers.NewUsersHandler(svc)

	body := map[string]string{"mode": "rating"}
	r := usersRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}, "master", true)
	w := httptest.NewRecorder()
	h.SetKidsMode(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (master should bypass ownership check)", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_ClearIconURL_Success(t *testing.T) {
	expected := models.User{ID: "u1"}
	svc := &fakeUsersService{belongsTo: true, clearIconURLUser: expected}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodDelete, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.ClearIconURL(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUsersHandler_ServeProfileIcon_NoIcon(t *testing.T) {
	svc := &fakeUsersService{iconPath: ""}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.ServeProfileIcon(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_ServeProfileIcon_UserNotFound(t *testing.T) {
	svc := &fakeUsersService{iconPathErr: users.ErrUserNotFound}
	h := handlers.NewUsersHandler(svc)

	r := usersRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"}, "acct-1", false)
	w := httptest.NewRecorder()
	h.ServeProfileIcon(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUsersHandler_Options(t *testing.T) {
	h := handlers.NewUsersHandler(&fakeUsersService{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/", nil)
	h.Options(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}
