package sessions

import (
	"testing"
	"time"
)

// setupTestService creates a new sessions service for testing with a temp directory.
func setupTestService(t *testing.T) *Service {
	t.Helper()
	tmpDir := t.TempDir()
	svc, err := NewService(tmpDir, DefaultSessionDuration)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	return svc
}

// setupTestServiceWithDuration creates a sessions service with a custom session duration.
func setupTestServiceWithDuration(t *testing.T, duration time.Duration) *Service {
	t.Helper()
	tmpDir := t.TempDir()
	svc, err := NewService(tmpDir, duration)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	return svc
}

func TestNewService_Success(t *testing.T) {
	tmpDir := t.TempDir()
	svc, err := NewService(tmpDir, DefaultSessionDuration)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestNewService_DefaultDuration(t *testing.T) {
	tmpDir := t.TempDir()
	svc, err := NewService(tmpDir, 0) // Zero duration should use default
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if svc.sessionDuration != DefaultSessionDuration {
		t.Errorf("expected default duration %v, got %v", DefaultSessionDuration, svc.sessionDuration)
	}
}

func TestNewService_NegativeDuration(t *testing.T) {
	tmpDir := t.TempDir()
	svc, err := NewService(tmpDir, -1*time.Hour)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if svc.sessionDuration != DefaultSessionDuration {
		t.Errorf("expected default duration %v, got %v", DefaultSessionDuration, svc.sessionDuration)
	}
}

func TestNewService_InMemoryOnly(t *testing.T) {
	// Empty storage dir should work (in-memory only)
	svc, err := NewService("", DefaultSessionDuration)
	if err != nil {
		t.Fatalf("NewService with empty dir failed: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	if svc.path != "" {
		t.Error("expected empty path for in-memory service")
	}
}

func TestCreate_GeneratesValidToken(t *testing.T) {
	svc := setupTestService(t)

	session, err := svc.Create("account-123", true, "Mozilla/5.0", "192.168.1.1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if session.Token == "" {
		t.Error("expected non-empty token")
	}
	// Token should be base64-encoded, so at least 32 bytes * 4/3 ≈ 43 chars
	if len(session.Token) < 40 {
		t.Errorf("expected token length >= 40, got %d", len(session.Token))
	}
}

func TestCreate_StoresSessionMetadata(t *testing.T) {
	svc := setupTestService(t)

	session, err := svc.Create("account-123", true, "Mozilla/5.0", "192.168.1.1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if session.AccountID != "account-123" {
		t.Errorf("expected AccountID 'account-123', got %q", session.AccountID)
	}
	if !session.IsMaster {
		t.Error("expected IsMaster to be true")
	}
	if session.UserAgent != "Mozilla/5.0" {
		t.Errorf("expected UserAgent 'Mozilla/5.0', got %q", session.UserAgent)
	}
	if session.IPAddress != "192.168.1.1" {
		t.Errorf("expected IPAddress '192.168.1.1', got %q", session.IPAddress)
	}
	if session.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if session.ExpiresAt.IsZero() {
		t.Error("expected non-zero ExpiresAt")
	}
	if !session.ExpiresAt.After(session.CreatedAt) {
		t.Error("expected ExpiresAt to be after CreatedAt")
	}
}

func TestCreate_UniqueTokens(t *testing.T) {
	svc := setupTestService(t)

	tokens := make(map[string]bool)
	for i := 0; i < 100; i++ {
		session, err := svc.Create("account", false, "", "")
		if err != nil {
			t.Fatalf("Create failed on iteration %d: %v", i, err)
		}
		if tokens[session.Token] {
			t.Fatalf("duplicate token generated on iteration %d", i)
		}
		tokens[session.Token] = true
	}
}

func TestCreatePersistent_LongExpiry(t *testing.T) {
	svc := setupTestService(t)

	session, err := svc.CreatePersistent("account-123", false, "Agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreatePersistent failed: %v", err)
	}

	// Persistent sessions should expire in ~100 years
	expectedExpiry := time.Now().Add(PersistentSessionDuration)
	diff := session.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Hour || diff > time.Hour {
		t.Errorf("expected expiry around %v, got %v", expectedExpiry, session.ExpiresAt)
	}
}

func TestCreateWithDuration_CustomDuration(t *testing.T) {
	svc := setupTestService(t)

	customDuration := 5 * time.Minute
	session, err := svc.CreateWithDuration("account-123", false, "Agent", "127.0.0.1", customDuration)
	if err != nil {
		t.Fatalf("CreateWithDuration failed: %v", err)
	}

	expectedExpiry := time.Now().Add(customDuration)
	diff := session.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("expected expiry around %v, got %v", expectedExpiry, session.ExpiresAt)
	}
}

func TestValidate_ValidToken(t *testing.T) {
	svc := setupTestService(t)

	created, err := svc.Create("account-123", true, "Agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	validated, err := svc.Validate(created.Token)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	if validated.Token != created.Token {
		t.Errorf("expected token %q, got %q", created.Token, validated.Token)
	}
	if validated.AccountID != created.AccountID {
		t.Errorf("expected AccountID %q, got %q", created.AccountID, validated.AccountID)
	}
}

func TestValidate_InvalidToken(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Validate("nonexistent-token")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestValidate_EmptyToken(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Validate("")
	if err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestValidate_ExpiredToken(t *testing.T) {
	// Use short duration for testing
	svc := setupTestServiceWithDuration(t, 1*time.Millisecond)

	created, err := svc.Create("account-123", false, "", "")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Wait for session to expire
	time.Sleep(10 * time.Millisecond)

	_, err = svc.Validate(created.Token)
	if err != ErrSessionExpired {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}

	// Session should be cleaned up
	if svc.Count() != 0 {
		t.Errorf("expected 0 sessions after expiration cleanup, got %d", svc.Count())
	}
}

func TestRevoke_Success(t *testing.T) {
	svc := setupTestService(t)

	session, err := svc.Create("account-123", false, "", "")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = svc.Revoke(session.Token)
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	// Validate should now fail
	_, err = svc.Validate(session.Token)
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound after revoke, got %v", err)
	}
}

func TestRevoke_NonexistentToken(t *testing.T) {
	svc := setupTestService(t)

	err := svc.Revoke("nonexistent-token")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestRevokeAllForAccount_MultipleSessions(t *testing.T) {
	svc := setupTestService(t)

	// Create multiple sessions for same account
	session1, _ := svc.Create("account-123", false, "Agent1", "")
	session2, _ := svc.Create("account-123", false, "Agent2", "")
	session3, _ := svc.Create("account-123", false, "Agent3", "")

	// Create session for different account
	session4, _ := svc.Create("account-456", false, "Agent4", "")

	count := svc.RevokeAllForAccount("account-123")
	if count != 3 {
		t.Errorf("expected 3 sessions revoked, got %d", count)
	}

	// Verify all account-123 sessions are invalid
	for _, token := range []string{session1.Token, session2.Token, session3.Token} {
		_, err := svc.Validate(token)
		if err != ErrSessionNotFound {
			t.Errorf("expected ErrSessionNotFound for revoked session, got %v", err)
		}
	}

	// Verify account-456 session is still valid
	_, err := svc.Validate(session4.Token)
	if err != nil {
		t.Errorf("expected session4 to still be valid, got %v", err)
	}
}

func TestRevokeAllForAccount_NoSessions(t *testing.T) {
	svc := setupTestService(t)

	count := svc.RevokeAllForAccount("nonexistent-account")
	if count != 0 {
		t.Errorf("expected 0 sessions revoked, got %d", count)
	}
}

func TestRefresh_ExtendsExpiry(t *testing.T) {
	svc := setupTestServiceWithDuration(t, 1*time.Hour)

	session, err := svc.Create("account-123", false, "", "")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	originalExpiry := session.ExpiresAt

	// Wait a bit then refresh
	time.Sleep(10 * time.Millisecond)

	refreshed, err := svc.Refresh(session.Token)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	if !refreshed.ExpiresAt.After(originalExpiry) {
		t.Errorf("expected new expiry %v to be after original %v", refreshed.ExpiresAt, originalExpiry)
	}
}

func TestRefresh_InvalidToken(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Refresh("nonexistent-token")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestRefresh_ExpiredToken(t *testing.T) {
	svc := setupTestServiceWithDuration(t, 1*time.Millisecond)

	session, err := svc.Create("account-123", false, "", "")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Wait for session to expire
	time.Sleep(10 * time.Millisecond)

	_, err = svc.Refresh(session.Token)
	if err != ErrSessionExpired {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}
}

func TestCleanup_RemovesExpiredSessions(t *testing.T) {
	svc := setupTestServiceWithDuration(t, 1*time.Millisecond)

	// Create some sessions
	svc.Create("account-1", false, "", "")
	svc.Create("account-2", false, "", "")
	svc.Create("account-3", false, "", "")

	if svc.Count() != 3 {
		t.Fatalf("expected 3 sessions, got %d", svc.Count())
	}

	// Wait for sessions to expire
	time.Sleep(10 * time.Millisecond)

	// Run cleanup
	cleaned := svc.Cleanup()
	if cleaned != 3 {
		t.Errorf("expected 3 sessions cleaned, got %d", cleaned)
	}

	if svc.Count() != 0 {
		t.Errorf("expected 0 sessions after cleanup, got %d", svc.Count())
	}
}

func TestCleanup_KeepsValidSessions(t *testing.T) {
	svc := setupTestServiceWithDuration(t, 1*time.Hour)

	// Create sessions
	svc.Create("account-1", false, "", "")
	svc.Create("account-2", false, "", "")

	// Run cleanup - should not remove anything
	cleaned := svc.Cleanup()
	if cleaned != 0 {
		t.Errorf("expected 0 sessions cleaned, got %d", cleaned)
	}

	if svc.Count() != 2 {
		t.Errorf("expected 2 sessions after cleanup, got %d", svc.Count())
	}
}

func TestGetSessionsForAccount_ReturnsSessions(t *testing.T) {
	svc := setupTestService(t)

	// Create sessions for different accounts
	svc.Create("account-123", false, "Agent1", "IP1")
	svc.Create("account-123", false, "Agent2", "IP2")
	svc.Create("account-456", false, "Agent3", "IP3")

	sessions := svc.GetSessionsForAccount("account-123")
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}

	for _, s := range sessions {
		if s.AccountID != "account-123" {
			t.Errorf("expected AccountID 'account-123', got %q", s.AccountID)
		}
	}
}

func TestGetSessionsForAccount_NoSessions(t *testing.T) {
	svc := setupTestService(t)

	sessions := svc.GetSessionsForAccount("nonexistent-account")
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestGetSessionsForAccount_ExcludesExpired(t *testing.T) {
	svc := setupTestServiceWithDuration(t, 1*time.Millisecond)

	svc.Create("account-123", false, "", "")

	// Wait for expiration
	time.Sleep(10 * time.Millisecond)

	sessions := svc.GetSessionsForAccount("account-123")
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions (expired), got %d", len(sessions))
	}
}

func TestCount_ReturnsCorrectCount(t *testing.T) {
	svc := setupTestService(t)

	if svc.Count() != 0 {
		t.Errorf("expected 0 initial sessions, got %d", svc.Count())
	}

	svc.Create("account-1", false, "", "")
	svc.Create("account-2", false, "", "")

	if svc.Count() != 2 {
		t.Errorf("expected 2 sessions, got %d", svc.Count())
	}
}

func TestPersistence_LoadsExistingSessions(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first service and add sessions
	svc1, err := NewService(tmpDir, DefaultSessionDuration)
	if err != nil {
		t.Fatalf("failed to create first service: %v", err)
	}

	session, err := svc1.Create("account-123", true, "Agent", "IP")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Create second service pointing to same directory
	svc2, err := NewService(tmpDir, DefaultSessionDuration)
	if err != nil {
		t.Fatalf("failed to create second service: %v", err)
	}

	// Should have loaded the session
	validated, err := svc2.Validate(session.Token)
	if err != nil {
		t.Fatalf("expected session to be loaded from disk: %v", err)
	}

	if validated.AccountID != "account-123" {
		t.Errorf("expected AccountID 'account-123', got %q", validated.AccountID)
	}
}

func TestPersistence_DoesNotLoadExpired(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first service with short duration
	svc1, err := NewService(tmpDir, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create first service: %v", err)
	}

	_, err = svc1.Create("account-123", false, "", "")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Wait for expiration
	time.Sleep(10 * time.Millisecond)

	// Create second service - should not load expired sessions
	svc2, err := NewService(tmpDir, DefaultSessionDuration)
	if err != nil {
		t.Fatalf("failed to create second service: %v", err)
	}

	if svc2.Count() != 0 {
		t.Errorf("expected 0 sessions (expired filtered), got %d", svc2.Count())
	}
}

func TestGenerateToken_Length(t *testing.T) {
	token, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken failed: %v", err)
	}

	// TokenLength is 32 bytes, base64 encoded = 44 chars (with padding) or 43 (URL encoding)
	if len(token) < 40 {
		t.Errorf("expected token length >= 40, got %d", len(token))
	}
}

func TestGenerateToken_Uniqueness(t *testing.T) {
	tokens := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		token, err := generateToken()
		if err != nil {
			t.Fatalf("generateToken failed on iteration %d: %v", i, err)
		}
		if tokens[token] {
			t.Fatalf("duplicate token generated on iteration %d", i)
		}
		tokens[token] = true
	}
}
