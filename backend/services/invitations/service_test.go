package invitations

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupService(t *testing.T) *Service {
	t.Helper()

	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	return svc
}

func TestNewService_RequiresStorageDir(t *testing.T) {
	t.Parallel()

	if _, err := NewService(""); err != ErrStorageDirRequired {
		t.Fatalf("expected ErrStorageDirRequired, got %v", err)
	}

	if _, err := NewService("   "); err != ErrStorageDirRequired {
		t.Fatalf("expected ErrStorageDirRequired for whitespace, got %v", err)
	}
}

func TestCreateAndValidate_Success(t *testing.T) {
	t.Parallel()

	svc := setupService(t)
	inv, err := svc.Create("master", 0)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if inv.ID == "" {
		t.Fatal("expected non-empty invitation ID")
	}
	if inv.Token == "" {
		t.Fatal("expected non-empty invitation token")
	}
	if inv.CreatedBy != "master" {
		t.Fatalf("expected CreatedBy master, got %q", inv.CreatedBy)
	}

	if _, err := base64.URLEncoding.DecodeString(inv.Token); err != nil {
		t.Fatalf("expected URL-safe base64 token, decode failed: %v", err)
	}

	delta := inv.ExpiresAt.Sub(inv.CreatedAt)
	if delta < DefaultExpirationDuration-time.Minute || delta > DefaultExpirationDuration+time.Minute {
		t.Fatalf("expected default expiration around %v, got %v", DefaultExpirationDuration, delta)
	}

	if err := svc.Validate(inv.Token); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestGetByToken_RejectsEmptyToken(t *testing.T) {
	t.Parallel()

	svc := setupService(t)

	if _, err := svc.GetByToken(""); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for empty token, got %v", err)
	}
	if _, err := svc.GetByToken("   "); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for whitespace token, got %v", err)
	}
}

func TestMarkUsedAndValidate_UsedInvitation(t *testing.T) {
	t.Parallel()

	svc := setupService(t)
	inv, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := svc.MarkUsed(inv.Token, "user-1"); err != nil {
		t.Fatalf("MarkUsed failed: %v", err)
	}

	stored, err := svc.GetByToken(inv.Token)
	if err != nil {
		t.Fatalf("GetByToken failed: %v", err)
	}
	if stored.UsedAt == nil {
		t.Fatal("expected UsedAt to be set")
	}
	if stored.UsedBy != "user-1" {
		t.Fatalf("expected UsedBy user-1, got %q", stored.UsedBy)
	}

	if err := svc.Validate(inv.Token); err != ErrInvitationUsed {
		t.Fatalf("expected ErrInvitationUsed from Validate, got %v", err)
	}
	if err := svc.MarkUsed(inv.Token, "user-2"); err != ErrInvitationUsed {
		t.Fatalf("expected ErrInvitationUsed on second MarkUsed, got %v", err)
	}
}

func TestValidate_ExpiredInvitation(t *testing.T) {
	t.Parallel()

	svc := setupService(t)
	inv, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	svc.mu.Lock()
	updated := svc.invitations[inv.ID]
	updated.ExpiresAt = time.Now().Add(-time.Hour)
	svc.invitations[inv.ID] = updated
	svc.mu.Unlock()

	if err := svc.Validate(inv.Token); err != ErrInvitationExpired {
		t.Fatalf("expected ErrInvitationExpired, got %v", err)
	}
}

func TestListAndDelete(t *testing.T) {
	t.Parallel()

	svc := setupService(t)
	older, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create older failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	newer, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create newer failed: %v", err)
	}

	list := svc.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 invitations, got %d", len(list))
	}
	if list[0].ID != newer.ID || list[1].ID != older.ID {
		t.Fatalf("expected newest first ordering, got %q then %q", list[0].ID, list[1].ID)
	}

	if err := svc.Delete(older.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if err := svc.Delete(older.ID); err != ErrInvitationNotFound {
		t.Fatalf("expected ErrInvitationNotFound on second delete, got %v", err)
	}
}

func TestCleanupExpired_RemovesOnlyOldExpiredOrUsed(t *testing.T) {
	t.Parallel()

	svc := setupService(t)

	keepValid, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create keepValid failed: %v", err)
	}
	removeExpired, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create removeExpired failed: %v", err)
	}
	removeUsed, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create removeUsed failed: %v", err)
	}
	keepUsedRecent, err := svc.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create keepUsedRecent failed: %v", err)
	}

	now := time.Now()
	old := now.Add(-3 * time.Hour)
	recent := now.Add(-30 * time.Minute)

	svc.mu.Lock()
	expired := svc.invitations[removeExpired.ID]
	expired.ExpiresAt = old
	svc.invitations[removeExpired.ID] = expired

	usedOld := svc.invitations[removeUsed.ID]
	usedOld.UsedAt = &old
	svc.invitations[removeUsed.ID] = usedOld

	usedRecent := svc.invitations[keepUsedRecent.ID]
	usedRecent.UsedAt = &recent
	svc.invitations[keepUsedRecent.ID] = usedRecent
	svc.mu.Unlock()

	removed, err := svc.CleanupExpired(2 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupExpired failed: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed invitations, got %d", removed)
	}

	if _, err := svc.GetByToken(keepValid.Token); err != nil {
		t.Fatalf("expected keepValid to remain, got error: %v", err)
	}
	if _, err := svc.GetByToken(keepUsedRecent.Token); err != nil {
		t.Fatalf("expected keepUsedRecent to remain, got error: %v", err)
	}
	if _, err := svc.GetByToken(removeExpired.Token); err != ErrInvitationNotFound {
		t.Fatalf("expected removeExpired to be deleted, got %v", err)
	}
	if _, err := svc.GetByToken(removeUsed.Token); err != ErrInvitationNotFound {
		t.Fatalf("expected removeUsed to be deleted, got %v", err)
	}
}

func TestNewService_LoadsPersistedInvitations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	svc1, err := NewService(dir)
	if err != nil {
		t.Fatalf("first NewService failed: %v", err)
	}

	inv, err := svc1.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("second NewService failed: %v", err)
	}

	loaded, err := svc2.GetByToken(inv.Token)
	if err != nil {
		t.Fatalf("GetByToken on reloaded service failed: %v", err)
	}
	if loaded.ID != inv.ID {
		t.Fatalf("expected loaded invitation ID %q, got %q", inv.ID, loaded.ID)
	}
}

func TestNewService_FailsOnInvalidJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invitations.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("failed to write invalid invitations file: %v", err)
	}

	if _, err := NewService(dir); err == nil {
		t.Fatal("expected NewService to fail on invalid JSON")
	}
}
