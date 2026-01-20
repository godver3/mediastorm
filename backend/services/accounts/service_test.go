package accounts

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"novastream/models"
)

// setupTestService creates a new accounts service for testing with a temp directory.
func setupTestService(t *testing.T) *Service {
	t.Helper()
	tmpDir := t.TempDir()
	svc, err := NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	return svc
}

func TestNewService_InitializesMasterAccount(t *testing.T) {
	svc := setupTestService(t)

	master, ok := svc.GetMasterAccount()
	if !ok {
		t.Fatal("expected master account to exist")
	}

	if master.ID != "master" {
		t.Errorf("expected master ID 'master', got %q", master.ID)
	}
	if master.Username != "admin" {
		t.Errorf("expected master username 'admin', got %q", master.Username)
	}
	if !master.IsMaster {
		t.Error("expected master account IsMaster to be true")
	}
}

func TestNewService_EmptyStorageDir(t *testing.T) {
	_, err := NewService("")
	if err != ErrStorageDirRequired {
		t.Errorf("expected ErrStorageDirRequired, got %v", err)
	}

	_, err = NewService("   ")
	if err != ErrStorageDirRequired {
		t.Errorf("expected ErrStorageDirRequired for whitespace, got %v", err)
	}
}

func TestNewService_LoadsExistingAccounts(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first service and add an account
	svc1, err := NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create first service: %v", err)
	}
	_, err = svc1.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	// Create second service pointing to same directory
	svc2, err := NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create second service: %v", err)
	}

	// Should have loaded the account
	_, ok := svc2.GetByUsername("testuser")
	if !ok {
		t.Error("expected testuser to be loaded from disk")
	}
}

func TestCreate_Success(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("newuser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if account.ID == "" {
		t.Error("expected non-empty ID")
	}
	if account.Username != "newuser" {
		t.Errorf("expected username 'newuser', got %q", account.Username)
	}
	if account.IsMaster {
		t.Error("expected IsMaster to be false for non-master account")
	}
	if account.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if account.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}

	// Verify password was hashed
	err = bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte("password123"))
	if err != nil {
		t.Error("expected password to be correctly hashed")
	}
}

func TestCreate_EmptyUsername(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("", "password123")
	if err != ErrUsernameRequired {
		t.Errorf("expected ErrUsernameRequired, got %v", err)
	}

	_, err = svc.Create("   ", "password123")
	if err != ErrUsernameRequired {
		t.Errorf("expected ErrUsernameRequired for whitespace, got %v", err)
	}
}

func TestCreate_EmptyPassword(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("testuser", "")
	if err != ErrPasswordRequired {
		t.Errorf("expected ErrPasswordRequired, got %v", err)
	}

	_, err = svc.Create("testuser", "   ")
	if err != ErrPasswordRequired {
		t.Errorf("expected ErrPasswordRequired for whitespace, got %v", err)
	}
}

func TestCreate_DuplicateUsername(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	_, err = svc.Create("testuser", "differentpassword")
	if err != ErrUsernameExists {
		t.Errorf("expected ErrUsernameExists, got %v", err)
	}

	// Case insensitive check
	_, err = svc.Create("TESTUSER", "anotherpassword")
	if err != ErrUsernameExists {
		t.Errorf("expected ErrUsernameExists for case-insensitive match, got %v", err)
	}
}

func TestCreate_CannotDuplicateMasterUsername(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("admin", "password123")
	if err != ErrUsernameExists {
		t.Errorf("expected ErrUsernameExists when creating user with master username, got %v", err)
	}
}

func TestAuthenticate_Success(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("testuser", "mypassword")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	account, err := svc.Authenticate("testuser", "mypassword")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	if account.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", account.Username)
	}
}

func TestAuthenticate_CaseInsensitiveUsername(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("TestUser", "mypassword")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	account, err := svc.Authenticate("testuser", "mypassword")
	if err != nil {
		t.Fatalf("Authenticate failed with lowercase: %v", err)
	}
	if account.Username != "TestUser" {
		t.Errorf("expected original username 'TestUser', got %q", account.Username)
	}

	account, err = svc.Authenticate("TESTUSER", "mypassword")
	if err != nil {
		t.Fatalf("Authenticate failed with uppercase: %v", err)
	}
}

func TestAuthenticate_InvalidPassword(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("testuser", "correctpassword")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err = svc.Authenticate("testuser", "wrongpassword")
	if err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestAuthenticate_NonexistentUser(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Authenticate("nonexistent", "anypassword")
	if err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestAuthenticate_EmptyCredentials(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Authenticate("", "password")
	if err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials for empty username, got %v", err)
	}

	_, err = svc.Authenticate("user", "")
	if err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials for empty password, got %v", err)
	}
}

func TestAuthenticate_MasterAccount(t *testing.T) {
	svc := setupTestService(t)

	// Authenticate with default password
	account, err := svc.Authenticate("admin", DefaultMasterPassword)
	if err != nil {
		t.Fatalf("Authenticate master failed: %v", err)
	}

	if !account.IsMaster {
		t.Error("expected IsMaster to be true")
	}
}

func TestRename_Success(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("oldname", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = svc.Rename(account.ID, "newname")
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	// Verify old name no longer works
	_, ok := svc.GetByUsername("oldname")
	if ok {
		t.Error("expected old username to not be found")
	}

	// Verify new name works
	renamed, ok := svc.GetByUsername("newname")
	if !ok {
		t.Error("expected new username to be found")
	}
	if renamed.Username != "newname" {
		t.Errorf("expected username 'newname', got %q", renamed.Username)
	}
}

func TestRename_NotFound(t *testing.T) {
	svc := setupTestService(t)

	err := svc.Rename("nonexistent-id", "newname")
	if err != ErrAccountNotFound {
		t.Errorf("expected ErrAccountNotFound, got %v", err)
	}
}

func TestRename_UsernameConflict(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("user1", "password123")
	if err != nil {
		t.Fatalf("Create user1 failed: %v", err)
	}

	user2, err := svc.Create("user2", "password123")
	if err != nil {
		t.Fatalf("Create user2 failed: %v", err)
	}

	err = svc.Rename(user2.ID, "user1")
	if err != ErrUsernameExists {
		t.Errorf("expected ErrUsernameExists, got %v", err)
	}
}

func TestRename_EmptyUsername(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = svc.Rename(account.ID, "")
	if err != ErrUsernameRequired {
		t.Errorf("expected ErrUsernameRequired, got %v", err)
	}
}

func TestUpdatePassword_Success(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("testuser", "oldpassword")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = svc.UpdatePassword(account.ID, "newpassword")
	if err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}

	// Old password should fail
	_, err = svc.Authenticate("testuser", "oldpassword")
	if err != ErrInvalidCredentials {
		t.Errorf("expected old password to fail, got %v", err)
	}

	// New password should work
	_, err = svc.Authenticate("testuser", "newpassword")
	if err != nil {
		t.Fatalf("new password should work: %v", err)
	}
}

func TestUpdatePassword_NotFound(t *testing.T) {
	svc := setupTestService(t)

	err := svc.UpdatePassword("nonexistent-id", "newpassword")
	if err != ErrAccountNotFound {
		t.Errorf("expected ErrAccountNotFound, got %v", err)
	}
}

func TestUpdatePassword_EmptyPassword(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = svc.UpdatePassword(account.ID, "")
	if err != ErrPasswordRequired {
		t.Errorf("expected ErrPasswordRequired, got %v", err)
	}
}

func TestDelete_Success(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = svc.Delete(account.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify account is gone
	_, ok := svc.Get(account.ID)
	if ok {
		t.Error("expected account to be deleted")
	}
}

func TestDelete_NotFound(t *testing.T) {
	svc := setupTestService(t)

	err := svc.Delete("nonexistent-id")
	if err != ErrAccountNotFound {
		t.Errorf("expected ErrAccountNotFound, got %v", err)
	}
}

func TestDelete_CannotDeleteMaster(t *testing.T) {
	svc := setupTestService(t)

	master, ok := svc.GetMasterAccount()
	if !ok {
		t.Fatal("master account not found")
	}

	err := svc.Delete(master.ID)
	if err != ErrCannotDeleteMaster {
		t.Errorf("expected ErrCannotDeleteMaster, got %v", err)
	}
}

func TestDelete_CannotDeleteLastAccount(t *testing.T) {
	tmpDir := t.TempDir()

	// Manually create a service without the master account to test this edge case
	svc := &Service{
		path:     filepath.Join(tmpDir, "accounts.json"),
		accounts: make(map[string]models.Account),
	}

	// Add a single non-master account
	svc.accounts["single"] = models.Account{
		ID:       "single",
		Username: "single",
		IsMaster: false,
	}

	err := svc.Delete("single")
	if err != ErrCannotDeleteLastAcct {
		t.Errorf("expected ErrCannotDeleteLastAcct, got %v", err)
	}
}

func TestHasDefaultPassword_True(t *testing.T) {
	svc := setupTestService(t)

	// Master account should have default password initially
	if !svc.HasDefaultPassword() {
		t.Error("expected HasDefaultPassword to be true initially")
	}
}

func TestHasDefaultPassword_False(t *testing.T) {
	svc := setupTestService(t)

	master, ok := svc.GetMasterAccount()
	if !ok {
		t.Fatal("master account not found")
	}

	err := svc.UpdatePassword(master.ID, "newpassword")
	if err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}

	if svc.HasDefaultPassword() {
		t.Error("expected HasDefaultPassword to be false after password change")
	}
}

func TestList_SortedByCreationTime(t *testing.T) {
	svc := setupTestService(t)

	// Create accounts in specific order
	_, err := svc.Create("first", "password123")
	if err != nil {
		t.Fatalf("Create first failed: %v", err)
	}
	_, err = svc.Create("second", "password123")
	if err != nil {
		t.Fatalf("Create second failed: %v", err)
	}
	_, err = svc.Create("third", "password123")
	if err != nil {
		t.Fatalf("Create third failed: %v", err)
	}

	accounts := svc.List()

	// Master should always be first
	if !accounts[0].IsMaster {
		t.Error("expected master account to be first")
	}

	// Rest should be in creation order
	if len(accounts) != 4 { // master + 3 created
		t.Fatalf("expected 4 accounts, got %d", len(accounts))
	}

	// Skip master (index 0), check order of others
	if accounts[1].Username != "first" {
		t.Errorf("expected second account to be 'first', got %q", accounts[1].Username)
	}
	if accounts[2].Username != "second" {
		t.Errorf("expected third account to be 'second', got %q", accounts[2].Username)
	}
	if accounts[3].Username != "third" {
		t.Errorf("expected fourth account to be 'third', got %q", accounts[3].Username)
	}
}

func TestGet_Found(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	found, ok := svc.Get(account.ID)
	if !ok {
		t.Error("expected account to be found")
	}
	if found.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", found.Username)
	}
}

func TestGet_NotFound(t *testing.T) {
	svc := setupTestService(t)

	_, ok := svc.Get("nonexistent-id")
	if ok {
		t.Error("expected account to not be found")
	}
}

func TestGet_EmptyID(t *testing.T) {
	svc := setupTestService(t)

	_, ok := svc.Get("")
	if ok {
		t.Error("expected empty ID to not be found")
	}

	_, ok = svc.Get("   ")
	if ok {
		t.Error("expected whitespace ID to not be found")
	}
}

func TestGetByUsername_Found(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	found, ok := svc.GetByUsername("testuser")
	if !ok {
		t.Error("expected account to be found")
	}
	if found.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", found.Username)
	}
}

func TestGetByUsername_CaseInsensitive(t *testing.T) {
	svc := setupTestService(t)

	_, err := svc.Create("TestUser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, ok := svc.GetByUsername("testuser")
	if !ok {
		t.Error("expected case-insensitive match")
	}

	_, ok = svc.GetByUsername("TESTUSER")
	if !ok {
		t.Error("expected uppercase match")
	}
}

func TestGetByUsername_NotFound(t *testing.T) {
	svc := setupTestService(t)

	_, ok := svc.GetByUsername("nonexistent")
	if ok {
		t.Error("expected account to not be found")
	}
}

func TestGetByUsername_EmptyUsername(t *testing.T) {
	svc := setupTestService(t)

	_, ok := svc.GetByUsername("")
	if ok {
		t.Error("expected empty username to not be found")
	}
}

func TestExists_True(t *testing.T) {
	svc := setupTestService(t)

	account, err := svc.Create("testuser", "password123")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if !svc.Exists(account.ID) {
		t.Error("expected account to exist")
	}
}

func TestExists_False(t *testing.T) {
	svc := setupTestService(t)

	if svc.Exists("nonexistent-id") {
		t.Error("expected account to not exist")
	}
}

func TestExists_EmptyID(t *testing.T) {
	svc := setupTestService(t)

	if svc.Exists("") {
		t.Error("expected empty ID to not exist")
	}
}

func TestPersistence_FileCreated(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Check that accounts file was created
	accountsPath := filepath.Join(tmpDir, "accounts.json")
	if _, err := os.Stat(accountsPath); os.IsNotExist(err) {
		t.Error("expected accounts.json to be created")
	}
}
