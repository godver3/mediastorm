package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	nzbfilesystemlegacy "novastream/internal/nzbfilesystem"
	"novastream/internal/usenet"
)

func TestStreamFailureRegistryRecordsMissingArticleFailures(t *testing.T) {
	registry := &streamFailureRegistry{records: make(map[string]streamFailureRecord)}
	err := &nzbfilesystemlegacy.PartialContentError{
		BytesRead:     1024,
		TotalExpected: 2048,
		UnderlyingErr: &usenet.ArticleNotFoundError{
			UnderlyingErr: errors.New("article not found"),
			BytesRead:     1024,
		},
	}

	if !registry.recordIfMissingArticles("/webdav/movies/title.mkv", err) {
		t.Fatal("recordIfMissingArticles returned false")
	}

	record, ok := registry.confirmedRecent("movies/title.mkv", time.Minute)
	if !ok {
		t.Fatal("confirmedRecent returned false")
	}
	if record.Reason != "article_not_found" {
		t.Fatalf("reason = %q, want article_not_found", record.Reason)
	}
}

func TestMigrateStreamRejectsUnconfirmedFailures(t *testing.T) {
	handler := &PrequeueHandler{
		failures: &streamFailureRegistry{records: make(map[string]streamFailureRecord)},
	}
	body, err := json.Marshal(MigrateStreamRequest{
		TitleName:        "Example Movie",
		MediaType:        "movie",
		FailedStreamPath: "/webdav/movies/example.mkv",
		LastPosition:     120,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/playback/migrate", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.MigrateStream(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp["code"] != "STREAM_FAILURE_NOT_CONFIRMED" {
		t.Fatalf("code = %q, want STREAM_FAILURE_NOT_CONFIRMED", resp["code"])
	}
}
