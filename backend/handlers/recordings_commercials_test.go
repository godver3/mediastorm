package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/internal/auth"
	"novastream/models"
)

// /video/segments with a recordingId adds the recording's commercial breaks,
// in order with the other segments, for an account that owns the recording's
// profile; others get none.
func TestResolveSkipSegmentsAddsRecordingCommercials(t *testing.T) {
	rec := models.Recording{ID: "rec-1", UserID: "profile-1", OutputPath: "/recordings/show.ts", Status: models.RecordingStatusCompleted}
	users := &fakeRecordingUsersProvider{users: []models.User{{ID: "profile-1", AccountID: "acct-1"}}}
	recordings := NewRecordingsHandler(&fakeRecordingService{recording: rec}, users)
	recordings.SetCommercials(func(outputPath string) []models.CommercialBreak {
		if outputPath != rec.OutputPath {
			return nil
		}
		return []models.CommercialBreak{{Start: 1261.88, End: 1604.5}, {Start: 30, End: 30}, {Start: 0, End: 15.84}}
	})
	video := &VideoHandler{}
	video.SetRecordingSegments(recordings.CommercialSegments)

	post := func(account string, master bool, body string) []map[string]any {
		t.Helper()
		ctx := context.WithValue(context.Background(), auth.ContextKeyAccountID, account)
		if master {
			ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
		}
		rr := httptest.NewRecorder()
		video.GetIntroSegments(rr, httptest.NewRequest(http.MethodPost, "/video/segments", strings.NewReader(body)).WithContext(ctx))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var out struct {
			Segments []map[string]any `json:"segments"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Segments
	}

	body := `{"recordingId":"rec-1","duration":3600,"chapters":[{"title":"Intro","start":100,"end":160}]}`
	got := post("acct-1", false, body)
	var types []string
	for _, s := range got {
		types = append(types, s["type"].(string))
	}
	if strings.Join(types, ",") != "commercial,intro,commercial" {
		t.Fatalf("segments = %v", got)
	}
	if got[0]["source"] != "comskip" || got[0]["end"] != 15.84 {
		t.Fatalf("first commercial = %v", got[0])
	}
	if n := len(post("acct-2", false, body)); n != 1 {
		t.Fatalf("another account got %d segments, want only the chapter intro", n)
	}
	if n := len(post("", true, body)); n != 3 {
		t.Fatalf("master got %d segments", n)
	}
	if n := len(post("acct-1", false, `{"recordingId":"missing","duration":3600}`)); n != 0 {
		t.Fatalf("unknown recording got %d segments", n)
	}
}
