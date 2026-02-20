package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/handlers"
	"novastream/models"
	"novastream/services/users"
	"novastream/services/watchlist"

	"github.com/gorilla/mux"
)

func TestWatchlistAddAndList(t *testing.T) {
	dir := t.TempDir()
	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create watchlist service: %v", err)
	}

	userSvc, err := users.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create users service: %v", err)
	}

	// Get the actual user ID created by ensureDefaultUser (it's a UUID, not "default")
	allUsers := userSvc.ListAll()
	if len(allUsers) == 0 {
		t.Fatalf("expected at least one user to be created")
	}
	userID := allUsers[0].ID

	h := handlers.NewWatchlistHandler(svc, userSvc, false)

	body := models.WatchlistUpsert{
		ID:        "m1",
		MediaType: "movie",
		Name:      "Sample",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users/"+userID+"/watchlist", bytes.NewReader(payload))
	req = mux.SetURLVars(req, map[string]string{"userID": userID})
	rec := httptest.NewRecorder()
	h.Add(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	reqList := httptest.NewRequest(http.MethodGet, "/api/users/"+userID+"/watchlist", nil)
	reqList = mux.SetURLVars(reqList, map[string]string{"userID": userID})
	recList := httptest.NewRecorder()
	h.List(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", recList.Code)
	}

	var items []models.WatchlistItem
	if err := json.Unmarshal(recList.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	if items[0].Name != "Sample" || items[0].MediaType != "movie" {
		t.Fatalf("unexpected item returned: %+v", items[0])
	}
}

func TestWatchlistAddWithGenresAndRuntime(t *testing.T) {
	dir := t.TempDir()
	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create watchlist service: %v", err)
	}

	userSvc, err := users.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create users service: %v", err)
	}

	allUsers := userSvc.ListAll()
	if len(allUsers) == 0 {
		t.Fatalf("expected at least one user to be created")
	}
	userID := allUsers[0].ID

	h := handlers.NewWatchlistHandler(svc, userSvc, false)

	body := models.WatchlistUpsert{
		ID:             "m2",
		MediaType:      "movie",
		Name:           "Genre Movie",
		Genres:         []string{"Action", "Sci-Fi"},
		RuntimeMinutes: 148,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users/"+userID+"/watchlist", bytes.NewReader(payload))
	req = mux.SetURLVars(req, map[string]string{"userID": userID})
	rec := httptest.NewRecorder()
	h.Add(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var item models.WatchlistItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(item.Genres) != 2 || item.Genres[0] != "Action" || item.Genres[1] != "Sci-Fi" {
		t.Fatalf("expected genres [Action Sci-Fi], got %v", item.Genres)
	}
	if item.RuntimeMinutes != 148 {
		t.Fatalf("expected runtimeMinutes 148, got %d", item.RuntimeMinutes)
	}

	// Verify via list
	reqList := httptest.NewRequest(http.MethodGet, "/api/users/"+userID+"/watchlist", nil)
	reqList = mux.SetURLVars(reqList, map[string]string{"userID": userID})
	recList := httptest.NewRecorder()
	h.List(recList, reqList)

	var items []models.WatchlistItem
	if err := json.Unmarshal(recList.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Genres) != 2 {
		t.Fatalf("expected genres to persist in list, got %v", items[0].Genres)
	}
	if items[0].RuntimeMinutes != 148 {
		t.Fatalf("expected runtimeMinutes to persist in list, got %d", items[0].RuntimeMinutes)
	}
}

func TestWatchlistUpdateAndRemove(t *testing.T) {
	dir := t.TempDir()
	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create watchlist service: %v", err)
	}
	userSvc, err := users.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create users service: %v", err)
	}

	// Get the actual user ID created by ensureDefaultUser (it's a UUID, not "default")
	allUsers := userSvc.ListAll()
	if len(allUsers) == 0 {
		t.Fatalf("expected at least one user to be created")
	}
	userID := allUsers[0].ID

	h := handlers.NewWatchlistHandler(svc, userSvc, false)

	_, err = svc.AddOrUpdate(userID, models.WatchlistUpsert{ID: "show1", MediaType: "series", Name: "Show"})
	if err != nil {
		t.Fatalf("failed to seed watchlist: %v", err)
	}

	updateBody := map[string]any{
		"watched": true,
		"progress": map[string]any{
			"percentage":     25,
			"currentSeason":  2,
			"currentEpisode": 4,
		},
	}
	payload, _ := json.Marshal(updateBody)
	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+userID+"/watchlist/series/show1", bytes.NewReader(payload))
	req = mux.SetURLVars(req, map[string]string{"userID": userID, "mediaType": "series", "id": "show1"})
	rec := httptest.NewRecorder()
	h.UpdateState(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var item models.WatchlistItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("failed to decode update response: %v", err)
	}

	// Note: Watch progress tracking has been moved to a separate service (history service)
	// Just verify the item was returned successfully
	if item.ID != "show1" {
		t.Fatalf("expected watchlist item with ID show1, got %+v", item)
	}

	reqDelete := httptest.NewRequest(http.MethodDelete, "/api/users/"+userID+"/watchlist/series/show1", nil)
	reqDelete = mux.SetURLVars(reqDelete, map[string]string{"userID": userID, "mediaType": "series", "id": "show1"})
	recDelete := httptest.NewRecorder()
	h.Remove(recDelete, reqDelete)

	if recDelete.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", recDelete.Code)
	}

	if items, err := svc.List(userID); err != nil {
		t.Fatalf("list after removal returned error: %v", err)
	} else if len(items) != 0 {
		t.Fatalf("expected empty watchlist after removal, got %d", len(items))
	}
}
