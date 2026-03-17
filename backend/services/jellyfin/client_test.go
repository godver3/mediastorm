package jellyfin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticate(t *testing.T) {
	t.Run("successful auth", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/Users/AuthenticateByName" {
				t.Errorf("expected path /Users/AuthenticateByName, got %s", r.URL.Path)
			}
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				t.Error("expected Authorization header")
			}

			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["Username"] != "testuser" {
				t.Errorf("expected Username 'testuser', got %s", body["Username"])
			}
			if body["Pw"] != "testpass" {
				t.Errorf("expected Pw 'testpass', got %s", body["Pw"])
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(AuthResult{
				AccessToken: "abc123token",
				User: struct {
					ID   string `json:"Id"`
					Name string `json:"Name"`
				}{
					ID:   "user-id-1",
					Name: "testuser",
				},
			})
		}))
		defer server.Close()

		client := NewClient()
		result, err := client.Authenticate(server.URL, "testuser", "testpass")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.AccessToken != "abc123token" {
			t.Errorf("expected AccessToken 'abc123token', got %s", result.AccessToken)
		}
		if result.User.ID != "user-id-1" {
			t.Errorf("expected User.ID 'user-id-1', got %s", result.User.ID)
		}
		if result.User.Name != "testuser" {
			t.Errorf("expected User.Name 'testuser', got %s", result.User.Name)
		}
	})

	t.Run("failed auth wrong credentials", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("Invalid username or password"))
		}))
		defer server.Close()

		client := NewClient()
		result, err := client.Authenticate(server.URL, "baduser", "badpass")
		if err == nil {
			t.Fatal("expected error for failed auth, got nil")
		}
		if result != nil {
			t.Errorf("expected nil result on failure, got %+v", result)
		}
	})
}

func TestTestConnection(t *testing.T) {
	t.Run("successful connection", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/System/Info" {
				t.Errorf("expected path /System/Info, got %s", r.URL.Path)
			}
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				t.Error("expected Authorization header")
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"ServerName":"TestServer"}`))
		}))
		defer server.Close()

		client := NewClient()
		err := client.TestConnection(server.URL, "test-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client := NewClient()
		err := client.TestConnection(server.URL, "test-token")
		if err == nil {
			t.Fatal("expected error for server error, got nil")
		}
	})
}

func TestGetFavorites(t *testing.T) {
	t.Run("fetches favorites with provider ID normalization", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}
			expectedPath := "/Users/user-123/Items"
			if r.URL.Path != expectedPath {
				t.Errorf("expected path %s, got %s", expectedPath, r.URL.Path)
			}

			q := r.URL.Query()
			if q.Get("Filters") != "IsFavorite" {
				t.Errorf("expected Filters=IsFavorite, got %s", q.Get("Filters"))
			}
			if q.Get("Recursive") != "true" {
				t.Errorf("expected Recursive=true, got %s", q.Get("Recursive"))
			}

			resp := map[string]interface{}{
				"Items": []map[string]interface{}{
					{
						"Id":             "item-1",
						"Name":           "Test Movie",
						"Type":           "Movie",
						"ProductionYear": 2024,
						"ProviderIds": map[string]string{
							"Tmdb": "12345",
							"Imdb": "tt0000001",
						},
					},
					{
						"Id":             "item-2",
						"Name":           "Test Series",
						"Type":           "Series",
						"ProductionYear": 2023,
						"ProviderIds": map[string]string{
							"Tmdb":  "67890",
							"Tvdb":  "111",
							"Imdb":  "tt0000002",
							"EXTRA": "val",
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := NewClient()
		items, err := client.GetFavorites(server.URL, "test-token", "user-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d", len(items))
		}

		// Check first item
		if items[0].Name != "Test Movie" {
			t.Errorf("expected name 'Test Movie', got %s", items[0].Name)
		}
		if items[0].Type != "Movie" {
			t.Errorf("expected type 'Movie', got %s", items[0].Type)
		}
		if items[0].Year != 2024 {
			t.Errorf("expected year 2024, got %d", items[0].Year)
		}

		// Verify provider IDs are normalized to lowercase
		if items[0].ProviderIDs["tmdb"] != "12345" {
			t.Errorf("expected tmdb=12345, got %s", items[0].ProviderIDs["tmdb"])
		}
		if items[0].ProviderIDs["imdb"] != "tt0000001" {
			t.Errorf("expected imdb=tt0000001, got %s", items[0].ProviderIDs["imdb"])
		}
		// Ensure original casing keys are gone
		if _, ok := items[0].ProviderIDs["Tmdb"]; ok {
			t.Error("expected 'Tmdb' key to be normalized to 'tmdb'")
		}

		// Check second item normalization
		if items[1].ProviderIDs["tvdb"] != "111" {
			t.Errorf("expected tvdb=111, got %s", items[1].ProviderIDs["tvdb"])
		}
		if items[1].ProviderIDs["extra"] != "val" {
			t.Errorf("expected extra=val, got %s", items[1].ProviderIDs["extra"])
		}
	})
}

func TestGetWatchHistory(t *testing.T) {
	t.Run("fetches watch history with provider ID normalization", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}
			expectedPath := "/Users/user-456/Items"
			if r.URL.Path != expectedPath {
				t.Errorf("expected path %s, got %s", expectedPath, r.URL.Path)
			}

			q := r.URL.Query()
			if q.Get("Filters") != "IsPlayed" {
				t.Errorf("expected Filters=IsPlayed, got %s", q.Get("Filters"))
			}
			if q.Get("SortBy") != "DatePlayed" {
				t.Errorf("expected SortBy=DatePlayed, got %s", q.Get("SortBy"))
			}
			if q.Get("SortOrder") != "Descending" {
				t.Errorf("expected SortOrder=Descending, got %s", q.Get("SortOrder"))
			}

			resp := map[string]interface{}{
				"Items": []map[string]interface{}{
					{
						"Id":             "item-10",
						"Name":           "Watched Movie",
						"Type":           "Movie",
						"ProductionYear": 2022,
						"ProviderIds": map[string]string{
							"Tmdb": "99999",
							"Imdb": "tt9999999",
						},
					},
					{
						"Id":                "item-11",
						"Name":              "Episode Title",
						"Type":              "Episode",
						"SeriesName":        "Some Series",
						"ParentIndexNumber": 2,
						"IndexNumber":       5,
						"ProviderIds": map[string]string{
							"Tvdb": "55555",
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := NewClient()
		items, err := client.GetWatchHistory(server.URL, "test-token", "user-456")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d", len(items))
		}

		// Check movie with normalized provider IDs
		if items[0].Name != "Watched Movie" {
			t.Errorf("expected name 'Watched Movie', got %s", items[0].Name)
		}
		if items[0].ProviderIDs["tmdb"] != "99999" {
			t.Errorf("expected tmdb=99999, got %s", items[0].ProviderIDs["tmdb"])
		}
		if items[0].ProviderIDs["imdb"] != "tt9999999" {
			t.Errorf("expected imdb=tt9999999, got %s", items[0].ProviderIDs["imdb"])
		}
		if _, ok := items[0].ProviderIDs["Tmdb"]; ok {
			t.Error("expected 'Tmdb' key to be normalized to 'tmdb'")
		}

		// Check episode fields
		if items[1].Type != "Episode" {
			t.Errorf("expected type 'Episode', got %s", items[1].Type)
		}
		if items[1].SeriesName != "Some Series" {
			t.Errorf("expected SeriesName 'Some Series', got %s", items[1].SeriesName)
		}
		if items[1].SeasonNum != 2 {
			t.Errorf("expected SeasonNum 2, got %d", items[1].SeasonNum)
		}
		if items[1].EpisodeNum != 5 {
			t.Errorf("expected EpisodeNum 5, got %d", items[1].EpisodeNum)
		}
		if items[1].ProviderIDs["tvdb"] != "55555" {
			t.Errorf("expected tvdb=55555, got %s", items[1].ProviderIDs["tvdb"])
		}
	})
}
