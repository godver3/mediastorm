package backup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

func ptr[T any](value T) *T { return &value }

// mockConfigManager creates a minimal config manager for testing
func mockConfigManager(t *testing.T) *config.Manager {
	t.Helper()
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")
	return config.NewManager(settingsPath)
}

// setupTestService creates a backup service with test files
func setupTestService(t *testing.T) (*Service, string) {
	t.Helper()
	cacheDir := t.TempDir()
	configMgr := mockConfigManager(t)

	// Create test files that would normally be backed up
	testFiles := map[string]string{
		"settings.json":          `{"key":"value"}`,
		"users.json":             `[{"id":"1","name":"Test"}]`,
		"watchlist.json":         `{"items":[]}`,
		"accounts.json":          `{}`,
		"playback_progress.json": `{}`,
		"watched_items.json":     `{}`,
		"user_settings.json":     `{}`,
	}

	for filename, content := range testFiles {
		path := filepath.Join(cacheDir, filename)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create test file %s: %v", filename, err)
		}
	}

	svc, err := NewService(cacheDir, configMgr)
	if err != nil {
		t.Fatalf("failed to create backup service: %v", err)
	}

	return svc, cacheDir
}

func TestNewService_Success(t *testing.T) {
	cacheDir := t.TempDir()
	configMgr := mockConfigManager(t)

	svc, err := NewService(cacheDir, configMgr)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}

	// Check backup directory was created
	backupDir := filepath.Join(cacheDir, "backups")
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		t.Error("expected backup directory to exist")
	}
}

func TestNewService_CreatesBackupDir(t *testing.T) {
	cacheDir := t.TempDir()
	configMgr := mockConfigManager(t)

	_, err := NewService(cacheDir, configMgr)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	backupDir := filepath.Join(cacheDir, "backups")
	info, err := os.Stat(backupDir)
	if err != nil {
		t.Fatalf("backup directory should exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("backup path should be a directory")
	}
}

func TestCreateBackup_CreatesValidZip(t *testing.T) {
	svc, _ := setupTestService(t)

	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	if info.Filename == "" {
		t.Error("expected non-empty filename")
	}
	if info.Size <= 0 {
		t.Error("expected positive file size")
	}
	if info.Type != BackupTypeManual {
		t.Errorf("expected type %s, got %s", BackupTypeManual, info.Type)
	}

	// Verify the zip file exists and is valid
	backupPath := filepath.Join(svc.backupDir, info.Filename)
	reader, err := zip.OpenReader(backupPath)
	if err != nil {
		t.Fatalf("failed to open backup zip: %v", err)
	}
	defer reader.Close()

	// Check manifest exists
	var hasManifest bool
	for _, f := range reader.File {
		if f.Name == "manifest.json" {
			hasManifest = true
			break
		}
	}
	if !hasManifest {
		t.Error("expected manifest.json in backup")
	}
}

func TestCreateBackup_ContainsExpectedFiles(t *testing.T) {
	svc, _ := setupTestService(t)

	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	backupPath := filepath.Join(svc.backupDir, info.Filename)
	reader, err := zip.OpenReader(backupPath)
	if err != nil {
		t.Fatalf("failed to open backup zip: %v", err)
	}
	defer reader.Close()

	// Collect files in zip
	filesInZip := make(map[string]bool)
	for _, f := range reader.File {
		filesInZip[f.Name] = true
	}

	// Check expected files are present
	expectedFiles := []string{
		"manifest.json",
		"settings.json",
		"users.json",
		"watchlist.json",
	}
	for _, expected := range expectedFiles {
		if !filesInZip[expected] {
			t.Errorf("expected %s in backup", expected)
		}
	}
}

func TestCreateBackup_ManifestHasCorrectMetadata(t *testing.T) {
	svc, _ := setupTestService(t)

	info, err := svc.CreateBackup(BackupTypeScheduled)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	backupPath := filepath.Join(svc.backupDir, info.Filename)
	reader, err := zip.OpenReader(backupPath)
	if err != nil {
		t.Fatalf("failed to open backup zip: %v", err)
	}
	defer reader.Close()

	// Find and read manifest
	var manifest Manifest
	for _, f := range reader.File {
		if f.Name == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("failed to open manifest: %v", err)
			}
			defer rc.Close()

			if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
				t.Fatalf("failed to decode manifest: %v", err)
			}
			break
		}
	}

	if manifest.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", manifest.Version)
	}
	if manifest.Type != BackupTypeScheduled {
		t.Errorf("expected type %s, got %s", BackupTypeScheduled, manifest.Type)
	}
	if len(manifest.Files) == 0 {
		t.Error("expected files in manifest")
	}
}

func TestListBackups_EmptyWhenNoBackups(t *testing.T) {
	cacheDir := t.TempDir()
	configMgr := mockConfigManager(t)
	svc, err := NewService(cacheDir, configMgr)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	backups, err := svc.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}

	if len(backups) != 0 {
		t.Errorf("expected 0 backups, got %d", len(backups))
	}
}

func TestListBackups_ReturnsCreatedBackups(t *testing.T) {
	svc, _ := setupTestService(t)

	// Create a backup
	_, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	backups, err := svc.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}

	if len(backups) != 1 {
		t.Errorf("expected 1 backup, got %d", len(backups))
	}
}

func TestListBackups_SortsNewestFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	svc, _ := setupTestService(t)

	// Create multiple backups with longer delay to ensure different filenames
	info1, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("first CreateBackup failed: %v", err)
	}
	// Sleep for at least 1 second to get different timestamp in filename
	time.Sleep(1100 * time.Millisecond)

	info2, err := svc.CreateBackup(BackupTypeScheduled)
	if err != nil {
		t.Fatalf("second CreateBackup failed: %v", err)
	}

	// Verify the filenames are different
	if info1.Filename == info2.Filename {
		t.Skip("timestamps too close, filenames are the same")
	}

	backups, err := svc.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}

	if len(backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(backups))
	}

	// Newest should be first
	if backups[0].Filename != info2.Filename {
		t.Error("expected newest backup first")
	}
}

func TestDeleteBackup_RemovesFile(t *testing.T) {
	svc, _ := setupTestService(t)

	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	err = svc.DeleteBackup(info.Filename)
	if err != nil {
		t.Fatalf("DeleteBackup failed: %v", err)
	}

	// Verify file is gone
	backupPath := filepath.Join(svc.backupDir, info.Filename)
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("expected backup file to be deleted")
	}

	// Verify list is empty
	backups, _ := svc.ListBackups()
	if len(backups) != 0 {
		t.Error("expected no backups after delete")
	}
}

func TestDeleteBackup_RejectsPathTraversal(t *testing.T) {
	svc, _ := setupTestService(t)

	err := svc.DeleteBackup("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestDeleteBackup_NonexistentFile(t *testing.T) {
	svc, _ := setupTestService(t)

	err := svc.DeleteBackup("nonexistent.zip")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestGetBackupReader_ReturnsReader(t *testing.T) {
	svc, _ := setupTestService(t)

	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	reader, size, err := svc.GetBackupReader(info.Filename)
	if err != nil {
		t.Fatalf("GetBackupReader failed: %v", err)
	}
	defer reader.Close()

	if size <= 0 {
		t.Error("expected positive size")
	}
	if size != info.Size {
		t.Errorf("size mismatch: got %d, expected %d", size, info.Size)
	}
}

func TestGetBackupReader_RejectsPathTraversal(t *testing.T) {
	svc, _ := setupTestService(t)

	_, _, err := svc.GetBackupReader("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestRestoreBackup_RestoresFiles(t *testing.T) {
	svc, cacheDir := setupTestService(t)

	// Create backup
	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// Modify a file
	settingsPath := filepath.Join(cacheDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"modified":true}`), 0644); err != nil {
		t.Fatalf("failed to modify settings: %v", err)
	}

	// Restore
	err = svc.RestoreBackup(info.Filename)
	if err != nil {
		t.Fatalf("RestoreBackup failed: %v", err)
	}

	// Verify file was restored
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("failed to read settings: %v", err)
	}
	if string(content) != `{"key":"value"}` {
		t.Errorf("expected original content, got %s", string(content))
	}
}

func TestBackupPreservesDashboardLayoutBesideConfiguredSettings(t *testing.T) {
	cacheDir := t.TempDir()
	configDir := t.TempDir()
	settingsPath := filepath.Join(configDir, "settings.json")
	layoutPath := filepath.Join(configDir, adminDashboardLayoutFile)
	original := `{"version":1,"modules":[]}`
	if err := os.WriteFile(layoutPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write dashboard layout: %v", err)
	}

	svc, err := NewService(cacheDir, config.NewManager(settingsPath))
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}
	if err := os.WriteFile(layoutPath, []byte(`{"modified":true}`), 0o600); err != nil {
		t.Fatalf("modify dashboard layout: %v", err)
	}
	if err := svc.RestoreBackup(info.Filename); err != nil {
		t.Fatalf("RestoreBackup failed: %v", err)
	}

	restored, err := os.ReadFile(layoutPath)
	if err != nil {
		t.Fatalf("read restored dashboard layout: %v", err)
	}
	if string(restored) != original {
		t.Fatalf("dashboard layout restored to %q, want %q", restored, original)
	}
}

func TestRestoreBackupUpload_RestoresFilesWithoutKeepingUpload(t *testing.T) {
	svc, cacheDir := setupTestService(t)

	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}
	archive, err := os.ReadFile(filepath.Join(svc.backupDir, info.Filename))
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if err := svc.DeleteBackup(info.Filename); err != nil {
		t.Fatalf("DeleteBackup failed: %v", err)
	}

	settingsPath := filepath.Join(cacheDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"modified":true}`), 0644); err != nil {
		t.Fatalf("modify settings: %v", err)
	}

	if err := svc.RestoreBackupUpload(bytes.NewReader(archive), "downloaded-backup.ZIP"); err != nil {
		t.Fatalf("RestoreBackupUpload failed: %v", err)
	}

	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read restored settings: %v", err)
	}
	if string(content) != `{"key":"value"}` {
		t.Errorf("expected original content, got %s", string(content))
	}
	backups, err := svc.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected uploaded backup not to be retained, got %d backups", len(backups))
	}
}

func TestRestoreBackupUpload_RejectsNonZipFilename(t *testing.T) {
	svc, _ := setupTestService(t)

	err := svc.RestoreBackupUpload(strings.NewReader("not a backup"), "backup.txt")
	if err == nil {
		t.Fatal("expected non-ZIP filename to be rejected")
	}
}

func TestIsRestorableBackupFile(t *testing.T) {
	for _, name := range append(append([]string{}, backupFiles...), "database.json") {
		if !isRestorableBackupFile(name) {
			t.Errorf("expected %q to be restorable", name)
		}
	}
	for _, name := range []string{"manifest.json", "../settings.json", "nested/settings.json", "unknown.json"} {
		if isRestorableBackupFile(name) {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestRestoreBackup_HandlerCreatesPreRestoreBackup(t *testing.T) {
	// Note: The pre-restore backup is created by the HTTP handler,
	// not by the RestoreBackup service method. This test verifies
	// the pattern that would be used by the handler.
	svc, _ := setupTestService(t)

	// Create initial backup
	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// Handler pattern: create pre-restore backup before calling RestoreBackup
	_, err = svc.CreateBackup(BackupTypePreRestore)
	if err != nil {
		t.Fatalf("CreateBackup (pre_restore) failed: %v", err)
	}

	// Now restore
	err = svc.RestoreBackup(info.Filename)
	if err != nil {
		t.Fatalf("RestoreBackup failed: %v", err)
	}

	// Check for pre_restore backup
	backups, err := svc.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}

	var hasPreRestore bool
	for _, b := range backups {
		if b.Type == BackupTypePreRestore {
			hasPreRestore = true
			break
		}
	}

	if !hasPreRestore {
		t.Error("expected pre_restore backup to exist")
	}
}

func TestRestoreBackup_RejectsPathTraversal(t *testing.T) {
	svc, _ := setupTestService(t)

	err := svc.RestoreBackup("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

// setupTestServiceWithRetention creates a backup service with specific retention settings
func setupTestServiceWithRetention(t *testing.T, retentionDays, retentionCount int) (*Service, string) {
	t.Helper()
	cacheDir := t.TempDir()

	// Config manager expects a file path, not a directory
	settingsPath := filepath.Join(cacheDir, "settings.json")
	configMgr := config.NewManager(settingsPath)

	// Set retention settings
	settings, _ := configMgr.Load()
	settings.BackupRetention.RetentionDays = retentionDays
	settings.BackupRetention.RetentionCount = retentionCount
	configMgr.Save(settings)

	// Create test files that would normally be backed up
	testFiles := map[string]string{
		"users.json":             `[{"id":"1","name":"Test"}]`,
		"watchlist.json":         `{"items":[]}`,
		"accounts.json":          `{}`,
		"playback_progress.json": `{}`,
		"watched_items.json":     `{}`,
		"user_settings.json":     `{}`,
	}

	for filename, content := range testFiles {
		path := filepath.Join(cacheDir, filename)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create test file %s: %v", filename, err)
		}
	}

	svc, err := NewService(cacheDir, configMgr)
	if err != nil {
		t.Fatalf("failed to create backup service: %v", err)
	}

	return svc, cacheDir
}

func TestCleanupOldBackups_NoOpWhenDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	svc, _ := setupTestServiceWithRetention(t, 0, 0)

	// Create backups
	for i := 0; i < 3; i++ {
		_, err := svc.CreateBackup(BackupTypeManual)
		if err != nil {
			t.Fatalf("CreateBackup failed: %v", err)
		}
		time.Sleep(1100 * time.Millisecond) // Ensure different timestamps
	}

	cleaned, err := svc.CleanupOldBackups()
	if err != nil {
		t.Fatalf("CleanupOldBackups failed: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}

	backups, _ := svc.ListBackups()
	if len(backups) != 3 {
		t.Errorf("expected 3 backups, got %d", len(backups))
	}
}

func TestCleanupOldBackups_ByCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	svc, _ := setupTestServiceWithRetention(t, 0, 2)

	// Create 4 backups with different timestamps
	for i := 0; i < 4; i++ {
		_, err := svc.CreateBackup(BackupTypeManual)
		if err != nil {
			t.Fatalf("CreateBackup failed: %v", err)
		}
		time.Sleep(1100 * time.Millisecond) // Ensure different timestamps
	}

	cleaned, err := svc.CleanupOldBackups()
	if err != nil {
		t.Fatalf("CleanupOldBackups failed: %v", err)
	}
	if cleaned != 2 {
		t.Errorf("expected 2 cleaned, got %d", cleaned)
	}

	backups, _ := svc.ListBackups()
	if len(backups) != 2 {
		t.Errorf("expected 2 backups after cleanup, got %d", len(backups))
	}
}

func TestBackupTypes(t *testing.T) {
	tests := []struct {
		name       string
		backupType BackupType
	}{
		{"manual", BackupTypeManual},
		{"scheduled", BackupTypeScheduled},
		{"pre_restore", BackupTypePreRestore},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := setupTestService(t)

			info, err := svc.CreateBackup(tt.backupType)
			if err != nil {
				t.Fatalf("CreateBackup failed: %v", err)
			}

			if info.Type != tt.backupType {
				t.Errorf("expected type %s, got %s", tt.backupType, info.Type)
			}
		})
	}
}

func TestDatabaseExportSectionsCoverDurableTables(t *testing.T) {
	// Guard against silently dropping durable user/config tables from backups.
	// When adding a new durable table, include it in export/import and this list
	// (or document why it is intentionally excluded near databaseExport).
	required := map[string]bool{}
	for _, section := range databaseExportSections {
		required[section] = true
	}
	for _, want := range []string{
		"notificationChannels",
		"notificationObservations",
		"hiddenItems",
		"watchlistTombstones",
		"seriesOrdering",
		"shareLinks",
		"remoteAccessInvites",
		"remoteAccessPairings",
		"localMediaLibraries",
		"localMediaItems",
		"remoteMediaLibraries",
		"remoteMediaItems",
		"libraryAccessPolicies",
		"recordings",
		"qualityPresets",
	} {
		if !required[want] {
			t.Errorf("databaseExportSections missing required durable section %q", want)
		}
	}

	// Empty export should still marshal every section key (except omitempty empties).
	// Use a populated struct so omitempty fields are present.
	now := time.Now().UTC()
	export := databaseExport{
		Version:     "2.1",
		ExportedAt:  now,
		Accounts:    []models.AccountStorage{},
		Users:       []rawUser{},
		Sessions:    []models.Session{},
		Invitations: []models.Invitation{},
		RemoteAccessInvites: []rawRemoteAccessInvite{{
			ID: "inv1", TokenHash: "hash", CreatedBy: "master", PeerName: "peer",
			ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		}},
		RemoteAccessPairings: []rawRemoteAccessPairing{{
			ID: "pair1", InviteID: ptr("inv1"), PeerID: "device-1", CredentialHash: "hash",
			CreatedBy: "master", CreatedAt: now,
		}},
		ShareLinks: []rawShareLink{{
			Token: "tok", AccountID: "master", Params: map[string]string{"url": "x"},
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}},
		Clients:             []models.Client{},
		ClientSettings:      map[string]models.ClientFilterSettings{},
		UserSettings:        map[string]models.UserSettings{},
		Watchlist:           map[string][]models.WatchlistItem{},
		WatchlistTombstones: map[string][]models.WatchlistTombstone{"default": {}},
		HiddenItems:         map[string][]models.HiddenItem{"default": {}},
		WatchHistory:        map[string][]models.WatchHistoryItem{},
		PlaybackProgress:    map[string][]models.PlaybackProgress{},
		ContentPreferences:  map[string][]models.ContentPreference{},
		SeriesOrdering:      map[string][]models.SeriesOrderingPref{"default": {}},
		CustomLists:         map[string][]customListExport{},
		NotificationChannels: []models.NotificationChannel{{
			ID: "ch1", ProfileID: "default", Name: "Discord", Type: "discord",
			URL: "https://example.invalid/hook", Enabled: true, Events: []string{"watch.started"},
			CreatedAt: now, UpdatedAt: now,
		}},
		NotificationObservations: []models.NotificationObservation{{
			ProfileID: "default", ItemKey: "k", Status: "upcoming", UpdatedAt: now,
		}},
		LocalMediaLibraries: []models.LocalMediaLibrary{{
			ID: "lib1", Name: "Movies", Type: models.LocalMediaLibraryTypeMovie,
			RootPath: "/media", CreatedAt: now, UpdatedAt: now,
		}},
		LocalMediaItems: []localMediaItemExport{{
			ID: "item1", LibraryID: "lib1", RelativePath: "a.mkv", FilePath: "/media/a.mkv",
			FileName: "a.mkv", CreatedAt: now, UpdatedAt: now,
		}},
		RemoteMediaLibraries: []models.RemoteMediaLibrary{{
			ID: "rm1", Name: "Plex Movies", Provider: "plex", AccountID: "acc",
			ExternalLibraryID: "1", CreatedAt: now, UpdatedAt: now,
		}},
		RemoteMediaItems: []models.RemoteMediaItem{{
			ID: "ri1", LibraryID: "rm1", ExternalItemID: "e1", Title: "Title",
			CreatedAt: now, UpdatedAt: now,
		}},
		LibraryAccessPolicies: map[string]models.LibraryAccessPolicy{
			"lib1": {LibraryID: "lib1", AccessMode: models.LibraryAccessModeAll},
		},
		Recordings: []models.Recording{{
			ID: "rec1", UserID: "default", Type: models.RecordingTypeEPG,
			Status: models.RecordingStatusPending, ChannelID: "ch", ChannelName: "Ch",
			Title: "Show", SourceURL: "http://example.invalid", StartAt: now, EndAt: now.Add(time.Hour),
			CreatedAt: now, UpdatedAt: now,
		}},
		QualityPresets: []*qualityprofiles.Preset{{ID: "qp1", App: "sonarr", Name: "German HD", CreatedAt: now, UpdatedAt: now}},
	}

	raw, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal export map: %v", err)
	}
	for _, section := range databaseExportSections {
		if _, ok := asMap[section]; !ok {
			t.Errorf("marshaled databaseExport missing section %q", section)
		}
	}

	// Round-trip must preserve notification webhook URL (the original bug class).
	var decoded databaseExport
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(decoded.NotificationChannels) != 1 {
		t.Fatalf("expected 1 notification channel, got %d", len(decoded.NotificationChannels))
	}
	if decoded.NotificationChannels[0].URL != "https://example.invalid/hook" {
		t.Errorf("notification channel URL not preserved: %q", decoded.NotificationChannels[0].URL)
	}
	if len(decoded.ShareLinks) != 1 || decoded.ShareLinks[0].Params["url"] != "x" {
		t.Errorf("share link params not preserved: %+v", decoded.ShareLinks)
	}
	if len(decoded.RemoteAccessInvites) != 1 || decoded.RemoteAccessInvites[0].TokenHash != "hash" {
		t.Errorf("remote access token hash not preserved: %+v", decoded.RemoteAccessInvites)
	}
	if len(decoded.LocalMediaItems) != 1 || decoded.LocalMediaItems[0].FilePath != "/media/a.mkv" {
		t.Errorf("local media file path not preserved: %+v", decoded.LocalMediaItems)
	}
}

func TestBackupFilenameFormat(t *testing.T) {
	svc, _ := setupTestService(t)

	info, err := svc.CreateBackup(BackupTypeManual)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// Filename should match pattern: mediastorm_backup_YYYYMMDD-HHMMSS.zip
	if len(info.Filename) < 30 {
		t.Errorf("filename too short: %s", info.Filename)
	}
	if !strings.HasPrefix(info.Filename, "mediastorm_backup_") {
		t.Errorf("expected filename to start with mediastorm_backup_, got %s", info.Filename)
	}
	if info.Filename[len(info.Filename)-4:] != ".zip" {
		t.Errorf("expected filename to end with .zip, got %s", info.Filename)
	}
}
