package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An AIOStreams base URL without its /stremio/<uuid>/<config> path still
// serves a manifest and answers stream requests with HTTP 200, but the only
// "stream" is AIO's error notice. The admin test must fail, not report
// "working, 1 streams found".
func TestAIOStreamsScraperTestFailsOnErrorEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/manifest.json" {
			w.Write([]byte(`{"id":"aio","name":"AIOStreams","version":"2.37.0"}`))
			return
		}
		w.Write([]byte(`{"streams":[{"name":"[❌] AIOStreams","description":"Invalid config:\n missing user data","externalUrl":"https://aio.example/","streamData":{"type":"error"}}]}`))
	}))
	defer server.Close()

	body, _ := json.Marshal(TestScraperRequest{Name: "MY_AIO", Type: "aiostreams", URL: server.URL})
	rec := httptest.NewRecorder()
	(&AdminUIHandler{}).TestScraper(rec, httptest.NewRequest(http.MethodPost, "/api/test/scraper", bytes.NewReader(body)))

	var result struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
	if !strings.Contains(result.Error, "Invalid config: missing user data") || !strings.Contains(result.Error, "manifest URL") {
		t.Fatalf("error should carry AIO's message and the URL hint: %q", result.Error)
	}
}

func TestAIOStreamsScraperTestReportsNoPlayableStreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/manifest.json" {
			w.Write([]byte(`{"id":"aio","name":"AIOStreams","version":"2.37.0"}`))
			return
		}
		w.Write([]byte(`{"streams":[{"name":"stats","streamData":{"type":"statistic"}}]}`))
	}))
	defer server.Close()

	body, _ := json.Marshal(TestScraperRequest{Name: "MY_AIO", Type: "aiostreams", URL: server.URL})
	rec := httptest.NewRecorder()
	(&AdminUIHandler{}).TestScraper(rec, httptest.NewRequest(http.MethodPost, "/api/test/scraper", bytes.NewReader(body)))
	if strings.Contains(rec.Body.String(), "is working") || !strings.Contains(rec.Body.String(), "no playable streams") {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

// Every Stremio-addon test must fail with the addon's message when the only
// "stream" is an error notice, and must not report success when the stream
// request itself fails.
func TestAddonScraperTestsFailOnErrorEntriesAndStreamFailures(t *testing.T) {
	errorBody := `{"streams":[{"name":"[⚠️] Addon","description":"Invalid config.","url":"https://addon.example"}]}`
	for _, sourceType := range []string{"aiostreams", "comet", "mediafusion", "stremio-direct"} {
		for _, mode := range []string{"error-entry", "http-500", "not-json", "empty"} {
			t.Run(sourceType+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/manifest.json") {
						w.Header().Set("Content-Type", "application/json")
						w.Write([]byte(`{"id":"fixture","name":"Fixture","version":"1.0"}`))
						return
					}
					switch mode {
					case "error-entry":
						w.Write([]byte(errorBody))
					case "http-500":
						w.WriteHeader(http.StatusInternalServerError)
					case "not-json":
						w.Write([]byte(`<html>login</html>`))
					case "empty":
						w.Write([]byte(`{"streams":[]}`))
					}
				}))
				defer server.Close()

				reqBody := TestScraperRequest{Name: "Fixture", Type: sourceType, URL: server.URL + "/manifest.json"}
				body, _ := json.Marshal(reqBody)
				rec := httptest.NewRecorder()
				(&AdminUIHandler{}).TestScraper(rec, httptest.NewRequest(http.MethodPost, "/api/test/scraper", bytes.NewReader(body)))
				var result struct {
					Success bool   `json:"success"`
					Error   string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatalf("decode %q: %v", rec.Body.String(), err)
				}
				if result.Success || result.Error == "" {
					t.Fatalf("expected a failed test with an error, got %s", rec.Body.String())
				}
				if mode == "error-entry" && !strings.Contains(result.Error, "Invalid config.") {
					t.Fatalf("error should carry the addon's message: %q", result.Error)
				}
			})
		}
	}
}
