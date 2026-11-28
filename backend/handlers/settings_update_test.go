package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gorilla/mux"

	"novastream/config"
)

// Two different handlers that each read, change and save settings run at
// once; every one of their writes must survive.
func TestConcurrentSettingsHandlerWritesKeepEveryChange(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.DefaultSettings()); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	trakt := &TraktAccountsHandler{configManager: mgr}
	admin := &AdminUIHandler{configManager: mgr}

	const perHandler = 12
	var wg sync.WaitGroup
	errs := make(chan string, 2*perHandler)
	post := func(handler http.HandlerFunc, body any, want int) {
		defer wg.Done()
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		if rec.Code != want {
			errs <- fmt.Sprintf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
	for i := 0; i < perHandler; i++ {
		wg.Add(2)
		go post(trakt.CreateAccount, map[string]string{"name": fmt.Sprintf("trakt-%d", i), "clientId": "id"}, http.StatusOK)
		go post(admin.CreateMDBListAccount, map[string]string{"name": fmt.Sprintf("mdb-%d", i), "apiKey": "key"}, http.StatusCreated)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	got, err := mgr.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := len(got.Trakt.Accounts); n != perHandler {
		t.Errorf("trakt accounts = %d, want %d", n, perHandler)
	}
	if n := len(got.MDBList.Accounts); n != perHandler {
		t.Errorf("mdblist accounts = %d, want %d", n, perHandler)
	}
}

// A validation failure inside the update callback writes its response and
// saves nothing.
func TestUpdateSettingsAbortSavesNothing(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.DefaultSettings()); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	h := &TraktAccountsHandler{configManager: mgr}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"name":"x"}`)))
	req = mux.SetURLVars(req, map[string]string{"accountID": "missing"})
	h.UpdateAccount(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	got, _ := mgr.Load()
	if len(got.Trakt.Accounts) != 0 {
		t.Fatalf("accounts = %d, want 0", len(got.Trakt.Accounts))
	}
}
