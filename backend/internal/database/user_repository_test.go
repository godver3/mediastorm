package database

import (
	"path/filepath"
	"testing"
)

// setupTestUserRepo creates a test database and user repository.
func setupTestUserRepo(t *testing.T) (*DB, *UserRepository) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := NewDB(Config{DatabasePath: dbPath})
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := NewUserRepository(db.Connection())
	return db, repo
}

func TestCreateUser_Success(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "user123",
		Provider: "direct",
		IsAdmin:  false,
	}

	err := repo.CreateUser(user)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	if user.ID == 0 {
		t.Error("expected non-zero ID after insert")
	}
	if user.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestCreateUser_WithAllFields(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	email := "test@example.com"
	name := "Test User"
	avatarURL := "https://example.com/avatar.png"
	providerID := "github123"
	passwordHash := "$2a$10$hashedpassword"
	apiKey := "test-api-key"

	user := &User{
		UserID:       "user456",
		Email:        &email,
		Name:         &name,
		AvatarURL:    &avatarURL,
		Provider:     "github",
		ProviderID:   &providerID,
		PasswordHash: &passwordHash,
		APIKey:       &apiKey,
		IsAdmin:      true,
	}

	err := repo.CreateUser(user)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Verify all fields were stored
	retrieved, _ := repo.GetUserByID("user456")
	if retrieved == nil {
		t.Fatal("expected user to be retrievable")
	}
	if *retrieved.Email != email {
		t.Errorf("expected email %q, got %q", email, *retrieved.Email)
	}
	if *retrieved.Name != name {
		t.Errorf("expected name %q, got %q", name, *retrieved.Name)
	}
	if !retrieved.IsAdmin {
		t.Error("expected IsAdmin to be true")
	}
}

func TestGetUserByID_Found(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "testuser",
		Provider: "direct",
	}
	repo.CreateUser(user)

	retrieved, err := repo.GetUserByID("testuser")
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected user to be found")
	}
	if retrieved.UserID != "testuser" {
		t.Errorf("expected UserID 'testuser', got %q", retrieved.UserID)
	}
}

func TestGetUserByID_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	retrieved, err := repo.GetUserByID("nonexistent")
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil for non-existent user")
	}
}

func TestGetUserByProvider_Found(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	providerID := "github456"
	user := &User{
		UserID:     "githubuser",
		Provider:   "github",
		ProviderID: &providerID,
	}
	repo.CreateUser(user)

	retrieved, err := repo.GetUserByProvider("github", "github456")
	if err != nil {
		t.Fatalf("GetUserByProvider failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected user to be found")
	}
	if retrieved.UserID != "githubuser" {
		t.Errorf("expected UserID 'githubuser', got %q", retrieved.UserID)
	}
}

func TestGetUserByProvider_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	retrieved, err := repo.GetUserByProvider("github", "nonexistent")
	if err != nil {
		t.Fatalf("GetUserByProvider failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil for non-existent provider")
	}
}

func TestUpdateUser_Success(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "updateuser",
		Provider: "direct",
	}
	repo.CreateUser(user)

	// Update user
	newEmail := "updated@example.com"
	newName := "Updated Name"
	newAvatar := "https://example.com/new-avatar.png"
	user.Email = &newEmail
	user.Name = &newName
	user.AvatarURL = &newAvatar

	err := repo.UpdateUser(user)
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}

	// Verify update
	retrieved, _ := repo.GetUserByID("updateuser")
	if *retrieved.Email != newEmail {
		t.Errorf("expected email %q, got %q", newEmail, *retrieved.Email)
	}
	if *retrieved.Name != newName {
		t.Errorf("expected name %q, got %q", newName, *retrieved.Name)
	}
}

func TestUpdateUser_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "nonexistent",
		Provider: "direct",
	}

	err := repo.UpdateUser(user)
	if err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestListUsers_Pagination(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	// Create multiple users
	for i := 0; i < 5; i++ {
		user := &User{
			UserID:   "user" + string(rune('0'+i)),
			Provider: "direct",
		}
		repo.CreateUser(user)
	}

	// Get first page
	users, err := repo.ListUsers(2, 0)
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}

	// Get second page
	users, err = repo.ListUsers(2, 2)
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users on second page, got %d", len(users))
	}

	// Get last page
	users, err = repo.ListUsers(2, 4)
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("expected 1 user on last page, got %d", len(users))
	}
}

func TestDeleteUser_Success(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "deleteuser",
		Provider: "direct",
	}
	repo.CreateUser(user)

	err := repo.DeleteUser("deleteuser")
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	// Verify deleted
	retrieved, _ := repo.GetUserByID("deleteuser")
	if retrieved != nil {
		t.Error("expected user to be deleted")
	}
}

func TestDeleteUser_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	err := repo.DeleteUser("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestGetUserCount_ReturnsCorrectCount(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	// Initially no users
	count, err := repo.GetUserCount()
	if err != nil {
		t.Fatalf("GetUserCount failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 users initially, got %d", count)
	}

	// Create users
	for i := 0; i < 3; i++ {
		user := &User{
			UserID:   "user" + string(rune('0'+i)),
			Provider: "direct",
		}
		repo.CreateUser(user)
	}

	count, err = repo.GetUserCount()
	if err != nil {
		t.Fatalf("GetUserCount failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 users, got %d", count)
	}
}

func TestUpdateLastLogin_Success(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "loginuser",
		Provider: "direct",
	}
	repo.CreateUser(user)

	err := repo.UpdateLastLogin("loginuser")
	if err != nil {
		t.Fatalf("UpdateLastLogin failed: %v", err)
	}

	// Verify last login was set
	retrieved, _ := repo.GetUserByID("loginuser")
	if retrieved.LastLogin == nil {
		t.Error("expected LastLogin to be set")
	}
}

func TestUpdateLastLogin_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	err := repo.UpdateLastLogin("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestSetAdminStatus_Success(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "adminuser",
		Provider: "direct",
		IsAdmin:  false,
	}
	repo.CreateUser(user)

	// Set admin status to true
	err := repo.SetAdminStatus("adminuser", true)
	if err != nil {
		t.Fatalf("SetAdminStatus failed: %v", err)
	}

	retrieved, _ := repo.GetUserByID("adminuser")
	if !retrieved.IsAdmin {
		t.Error("expected IsAdmin to be true")
	}

	// Set admin status to false
	err = repo.SetAdminStatus("adminuser", false)
	if err != nil {
		t.Fatalf("SetAdminStatus failed: %v", err)
	}

	retrieved, _ = repo.GetUserByID("adminuser")
	if retrieved.IsAdmin {
		t.Error("expected IsAdmin to be false")
	}
}

func TestGetUserByEmail_Found(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	email := "test@example.com"
	user := &User{
		UserID:   "emailuser",
		Email:    &email,
		Provider: "direct",
	}
	repo.CreateUser(user)

	retrieved, err := repo.GetUserByEmail("test@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected user to be found")
	}
	if retrieved.UserID != "emailuser" {
		t.Errorf("expected UserID 'emailuser', got %q", retrieved.UserID)
	}
}

func TestGetUserByEmail_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	retrieved, err := repo.GetUserByEmail("nonexistent@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil for non-existent email")
	}
}

func TestGetUserByUsername_Found(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "usernametest",
		Provider: "direct",
	}
	repo.CreateUser(user)

	retrieved, err := repo.GetUserByUsername("usernametest")
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected user to be found")
	}
}

func TestUpdatePassword_Success(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "passworduser",
		Provider: "direct",
	}
	repo.CreateUser(user)

	newHash := "$2a$10$newhash"
	err := repo.UpdatePassword("passworduser", newHash)
	if err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}

	retrieved, _ := repo.GetUserByID("passworduser")
	if retrieved.PasswordHash == nil || *retrieved.PasswordHash != newHash {
		t.Error("expected password hash to be updated")
	}
}

func TestRegenerateAPIKey_Success(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "apikeyuser",
		Provider: "direct",
	}
	repo.CreateUser(user)

	apiKey, err := repo.RegenerateAPIKey("apikeyuser")
	if err != nil {
		t.Fatalf("RegenerateAPIKey failed: %v", err)
	}

	if apiKey == "" {
		t.Error("expected non-empty API key")
	}

	// Verify API key was stored
	retrieved, _ := repo.GetUserByID("apikeyuser")
	if retrieved.APIKey == nil || *retrieved.APIKey != apiKey {
		t.Error("expected API key to be stored")
	}
}

func TestRegenerateAPIKey_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	_, err := repo.RegenerateAPIKey("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestGetUserByAPIKey_Found(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	user := &User{
		UserID:   "apilookupuser",
		Provider: "direct",
	}
	repo.CreateUser(user)

	apiKey, _ := repo.RegenerateAPIKey("apilookupuser")

	retrieved, err := repo.GetUserByAPIKey(apiKey)
	if err != nil {
		t.Fatalf("GetUserByAPIKey failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected user to be found")
	}
	if retrieved.UserID != "apilookupuser" {
		t.Errorf("expected UserID 'apilookupuser', got %q", retrieved.UserID)
	}
}

func TestGetUserByAPIKey_NotFound(t *testing.T) {
	_, repo := setupTestUserRepo(t)

	retrieved, err := repo.GetUserByAPIKey("nonexistent-key")
	if err != nil {
		t.Fatalf("GetUserByAPIKey failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil for non-existent API key")
	}
}
