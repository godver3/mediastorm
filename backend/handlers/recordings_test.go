package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novastream/internal/auth"
	"novastream/models"

	"github.com/gorilla/mux"
)

type fakeRecordingService struct {
	recording models.Recording
}

func (f *fakeRecordingService) List(filter models.RecordingListFilter) ([]models.Recording, error) {
	return []models.Recording{f.recording}, nil
}

func (f *fakeRecordingService) Get(id string) (*models.Recording, error) {
	if id != f.recording.ID {
		return nil, nil
	}
	recording := f.recording
	return &recording, nil
}

func (f *fakeRecordingService) CreateFromEPG(req models.CreateEPGRecordingRequest) (models.Recording, error) {
	return models.Recording{}, nil
}

func (f *fakeRecordingService) CreateTimeBlock(req models.CreateTimeBlockRecordingRequest) (models.Recording, error) {
	return models.Recording{}, nil
}

func (f *fakeRecordingService) Cancel(id string) error { return nil }

func (f *fakeRecordingService) Delete(id string) error { return nil }

type fakeRecordingUsersProvider struct {
	users []models.User
}

func (f *fakeRecordingUsersProvider) BelongsToAccount(profileID, accountID string) bool {
	for _, user := range f.users {
		if user.ID == profileID && user.AccountID == accountID {
			return true
		}
	}
	return false
}

func (f *fakeRecordingUsersProvider) Exists(id string) bool {
	for _, user := range f.users {
		if user.ID == id {
			return true
		}
	}
	return false
}

func (f *fakeRecordingUsersProvider) ListAll() []models.User {
	return append([]models.User(nil), f.users...)
}

type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {}

func TestRecordingsHandlerTracksRunningRecordingStreamUsage(t *testing.T) {
	originalTracker := globalStreamTracker
	globalStreamTracker = newTestTracker()
	defer func() { globalStreamTracker = originalTracker }()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "recording.ts")
	if err := os.WriteFile(outputPath, []byte("recording-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	service := &fakeRecordingService{
		recording: models.Recording{
			ID:         "rec-1",
			UserID:     "profile-1",
			Status:     models.RecordingStatusRunning,
			OutputPath: outputPath,
		},
	}
	users := &fakeRecordingUsersProvider{
		users: []models.User{{ID: "profile-1", AccountID: "acct-1", Name: "Profile 1"}},
	}
	handler := NewRecordingsHandler(service, users)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)

	req := httptest.NewRequest(http.MethodGet, "/api/live/recordings/rec-1/stream?profileId=profile-1", nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"recordingID": "rec-1"})
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Stream(rec, req)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if globalStreamTracker.CountForAccount("acct-1") == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for recording stream to be tracked; count=%d", globalStreamTracker.CountForAccount("acct-1"))
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}

	if count := globalStreamTracker.CountForAccount("acct-1"); count != 0 {
		t.Fatalf("expected tracked recording stream to be removed after completion, got %d", count)
	}
}
