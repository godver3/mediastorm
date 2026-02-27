package users_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/models"
	"novastream/services/users"
)

func TestServiceInitialisesDefaultUser(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly one user, got %d", len(list))
	}

	if list[0].ID == "" {
		t.Fatal("expected default user to have an ID")
	}
	if list[0].Name != models.DefaultUserName {
		t.Fatalf("expected default user name %q, got %q", models.DefaultUserName, list[0].Name)
	}
}

func TestServiceCreateRenameAndDelete(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	created, err := svc.Create("Evening Watcher")
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if created.ID == "" {
		t.Fatalf("expected created user to have id")
	}

	renamed, err := svc.Rename(created.ID, "Night Owl")
	if err != nil {
		t.Fatalf("rename returned error: %v", err)
	}

	if renamed.Name != "Night Owl" {
		t.Fatalf("expected renamed user to have updated name, got %q", renamed.Name)
	}

	if err := svc.Delete(created.ID); err != nil {
		t.Fatalf("delete returned error: %v", err)
	}

	if svc.Exists(created.ID) {
		t.Fatalf("expected user to be deleted")
	}
}

func TestDeleteLastUserFails(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly one user, got %d", len(list))
	}

	if err := svc.Delete(list[0].ID); err == nil {
		t.Fatal("expected delete to fail for last remaining user")
	}
}

func TestSetIconURLSendsUserAgent(t *testing.T) {
	var receivedUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "image/png")
		// Return a minimal valid PNG (1x1 transparent pixel)
		png := []byte{
			0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
			0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
			0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
			0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
			0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, // IDAT chunk
			0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
			0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
			0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, // IEND chunk
			0x42, 0x60, 0x82,
		}
		w.Write(png)
	}))
	defer server.Close()

	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	userID := list[0].ID

	_, err = svc.SetIconURL(userID, server.URL+"/test.png")
	if err != nil {
		t.Fatalf("SetIconURL failed: %v", err)
	}

	if receivedUserAgent == "" {
		t.Fatal("expected User-Agent header to be set, got empty string")
	}
	if receivedUserAgent != "mediastorm/1.0" {
		t.Fatalf("expected User-Agent 'mediastorm/1.0', got %q", receivedUserAgent)
	}
}

func TestSetIconURLInvalidURL(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	userID := list[0].ID

	_, err = svc.SetIconURL(userID, "not-a-url")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}

	_, err = svc.SetIconURL(userID, "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestSetIconURLUserNotFound(t *testing.T) {
	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	_, err = svc.SetIconURL("nonexistent-user", "https://example.com/image.png")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
}

func TestSetIconURLServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	svc, err := users.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	list := svc.List()
	userID := list[0].ID

	_, err = svc.SetIconURL(userID, server.URL+"/test.png")
	if err == nil {
		t.Fatal("expected error for server 403 response")
	}
}
