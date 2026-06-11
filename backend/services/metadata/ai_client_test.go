package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAIClientOpenAICompatibleProviderUsesChatCompletions(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotModel string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var req openAIChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[{\"title\":\"Arrival\",\"year\":2016,\"mediaType\":\"movie\"}]"}}]}`))
	}))
	defer server.Close()

	client := newAIClient(AIConfig{
		Provider: "openrouter",
		APIKey:   "test-key",
		Model:    "test-model",
		BaseURL:  server.URL,
	}, server.Client(), nil)

	recs, err := client.getCustomRecommendations(context.Background(), "thoughtful sci-fi")
	if err != nil {
		t.Fatalf("getCustomRecommendations error: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if gotModel != "test-model" {
		t.Fatalf("model = %q, want test-model", gotModel)
	}
	if len(recs) != 1 || recs[0].Title != "Arrival" {
		t.Fatalf("unexpected recommendations: %+v", recs)
	}
}

func TestAIClientAnthropicProviderUsesMessagesAPI(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	var gotVersion string
	var gotModel string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"[{\"title\":\"Severance\",\"year\":2022,\"mediaType\":\"series\"}]"}]}`))
	}))
	defer server.Close()

	client := newAIClient(AIConfig{
		Provider: "claude",
		APIKey:   "anthropic-key",
		Model:    "claude-test",
		BaseURL:  server.URL,
	}, server.Client(), nil)

	recs, err := client.getCustomRecommendations(context.Background(), "office mystery shows")
	if err != nil {
		t.Fatalf("getCustomRecommendations error: %v", err)
	}
	if gotPath != "/messages" {
		t.Fatalf("path = %q, want /messages", gotPath)
	}
	if gotAPIKey != "anthropic-key" {
		t.Fatalf("x-api-key = %q, want anthropic-key", gotAPIKey)
	}
	if gotVersion == "" {
		t.Fatal("anthropic-version header was empty")
	}
	if gotModel != "claude-test" {
		t.Fatalf("model = %q, want claude-test", gotModel)
	}
	if len(recs) != 1 || recs[0].Title != "Severance" {
		t.Fatalf("unexpected recommendations: %+v", recs)
	}
}
