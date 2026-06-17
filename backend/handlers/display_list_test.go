package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/handlers"
	"novastream/models"
	"novastream/services/customlists"
	"novastream/services/users"
	"novastream/services/watchlist"

	"github.com/gorilla/mux"
)

func TestDisplayListWatchlist(t *testing.T) {
	dir := t.TempDir()
	wl, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create watchlist service: %v", err)
	}
	custom, err := customlists.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create custom list service: %v", err)
	}
	userSvc, err := users.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create users service: %v", err)
	}
	userID := userSvc.ListAll()[0].ID

	if _, err := wl.AddOrUpdate(userID, models.WatchlistUpsert{ID: "m1", MediaType: "movie", Name: "Sample"}); err != nil {
		t.Fatalf("failed to seed watchlist: %v", err)
	}

	h := handlers.NewDisplayListHandler(wl, custom, userSvc)
	req := httptest.NewRequest(http.MethodGet, "/api/users/"+userID+"/display-list?source=watchlist", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": userID})
	rec := httptest.NewRecorder()

	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Source string                 `json:"source"`
		Items  []models.WatchlistItem `json:"items"`
		Total  int                    `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Source != "watchlist" || resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Items[0].Name != "Sample" {
		t.Fatalf("unexpected item: %+v", resp.Items[0])
	}
}

func TestDisplayListCustomListRequiresListID(t *testing.T) {
	dir := t.TempDir()
	wl, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create watchlist service: %v", err)
	}
	custom, err := customlists.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create custom list service: %v", err)
	}
	userSvc, err := users.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create users service: %v", err)
	}
	userID := userSvc.ListAll()[0].ID

	h := handlers.NewDisplayListHandler(wl, custom, userSvc)
	req := httptest.NewRequest(http.MethodGet, "/api/users/"+userID+"/display-list?source=custom-list", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": userID})
	rec := httptest.NewRecorder()

	h.Get(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
