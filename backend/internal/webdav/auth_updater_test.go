package webdav

import (
	"sync"
	"testing"
)

func TestNewAuthCredentials(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{
			name:     "basic credentials",
			username: "user",
			password: "pass",
		},
		{
			name:     "empty credentials",
			username: "",
			password: "",
		},
		{
			name:     "special characters",
			username: "user@domain.com",
			password: "p@ss!word#123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds := NewAuthCredentials(tt.username, tt.password)
			if creds == nil {
				t.Fatal("NewAuthCredentials returned nil")
			}

			gotUser, gotPass := creds.GetCredentials()
			if gotUser != tt.username {
				t.Errorf("username = %q, want %q", gotUser, tt.username)
			}
			if gotPass != tt.password {
				t.Errorf("password = %q, want %q", gotPass, tt.password)
			}
		})
	}
}

func TestAuthCredentials_GetCredentials(t *testing.T) {
	creds := NewAuthCredentials("testuser", "testpass")

	user, pass := creds.GetCredentials()
	if user != "testuser" {
		t.Errorf("username = %q, want %q", user, "testuser")
	}
	if pass != "testpass" {
		t.Errorf("password = %q, want %q", pass, "testpass")
	}
}

func TestAuthCredentials_UpdateCredentials(t *testing.T) {
	creds := NewAuthCredentials("olduser", "oldpass")

	// Update credentials
	creds.UpdateCredentials("newuser", "newpass")

	user, pass := creds.GetCredentials()
	if user != "newuser" {
		t.Errorf("username after update = %q, want %q", user, "newuser")
	}
	if pass != "newpass" {
		t.Errorf("password after update = %q, want %q", pass, "newpass")
	}
}

func TestAuthCredentials_ConcurrentAccess(t *testing.T) {
	creds := NewAuthCredentials("initial", "initial")

	var wg sync.WaitGroup
	iterations := 100

	// Concurrent reads
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			creds.GetCredentials()
		}()
	}

	// Concurrent writes
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			creds.UpdateCredentials("user"+string(rune(n)), "pass"+string(rune(n)))
		}(i)
	}

	wg.Wait()

	// Verify credentials are readable after concurrent access
	user, pass := creds.GetCredentials()
	if user == "" || pass == "" {
		t.Error("credentials should not be empty after concurrent access")
	}
}

func TestNewAuthUpdater(t *testing.T) {
	updater := NewAuthUpdater()
	if updater == nil {
		t.Fatal("NewAuthUpdater returned nil")
	}
	if updater.logger == nil {
		t.Error("logger should not be nil")
	}
}

func TestAuthUpdater_SetAuthCredentials(t *testing.T) {
	updater := NewAuthUpdater()
	creds := NewAuthCredentials("user", "pass")

	updater.SetAuthCredentials(creds)

	if updater.credentials != creds {
		t.Error("credentials not set correctly")
	}
}

func TestAuthUpdater_UpdateAuth(t *testing.T) {
	tests := []struct {
		name        string
		setupCreds  bool
		newUsername string
		newPassword string
		wantErr     bool
	}{
		{
			name:        "successful update",
			setupCreds:  true,
			newUsername: "newuser",
			newPassword: "newpass",
			wantErr:     false,
		},
		{
			name:        "credentials not initialized",
			setupCreds:  false,
			newUsername: "user",
			newPassword: "pass",
			wantErr:     true,
		},
		{
			name:        "empty credentials",
			setupCreds:  true,
			newUsername: "",
			newPassword: "",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updater := NewAuthUpdater()

			if tt.setupCreds {
				creds := NewAuthCredentials("old", "old")
				updater.SetAuthCredentials(creds)
			}

			err := updater.UpdateAuth(tt.newUsername, tt.newPassword)
			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateAuth() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.setupCreds {
				user, pass := updater.credentials.GetCredentials()
				if user != tt.newUsername {
					t.Errorf("username = %q, want %q", user, tt.newUsername)
				}
				if pass != tt.newPassword {
					t.Errorf("password = %q, want %q", pass, tt.newPassword)
				}
			}
		})
	}
}

func TestAuthUpdater_UpdateAuth_NilCredentials(t *testing.T) {
	updater := NewAuthUpdater()
	// Don't set credentials

	err := updater.UpdateAuth("user", "pass")
	if err == nil {
		t.Error("expected error when credentials not initialized")
	}
	if err.Error() != "auth credentials not initialized" {
		t.Errorf("unexpected error message: %v", err)
	}
}
