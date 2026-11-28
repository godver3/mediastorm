package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

// BackupType indicates how the backup was created
type BackupType string

const (
	BackupTypeManual     BackupType = "manual"
	BackupTypeScheduled  BackupType = "scheduled"
	BackupTypePreRestore BackupType = "pre_restore"
)

// BackupInfo contains metadata about a backup file
type BackupInfo struct {
	Filename  string     `json:"filename"`
	Size      int64      `json:"size"`
	CreatedAt time.Time  `json:"createdAt"`
	Type      BackupType `json:"type"`
	Version   string     `json:"version,omitempty"`
}

// Manifest contains metadata about the backup contents
type Manifest struct {
	Version     string            `json:"version"`
	CreatedAt   time.Time         `json:"createdAt"`
	Type        BackupType        `json:"type"`
	Files       map[string]string `json:"files"` // filename -> sha256 checksum
	Description string            `json:"description,omitempty"`
}

// Service handles backup creation, management, and restoration
type Service struct {
	mu                  sync.RWMutex
	backupDir           string
	cacheDir            string
	dashboardLayoutPath string
	configManager       *config.Manager
	store               *datastore.DataStore
}

const adminDashboardLayoutFile = "admin-dashboard-layout.json"

// Files to backup. Most are relative to cacheDir; the dashboard layout lives
// beside the configured settings file.
// When using PostgreSQL, only settings.json is backed up as a file — everything else
// is exported from the database as database.json.
// The legacy JSON file list is kept for backwards compatibility with non-DB deployments.
var backupFiles = []string{
	"settings.json",
	adminDashboardLayoutFile,
	"queue.db",
	"users.json",
	"watchlist.json",
	"accounts.json",
	"playback_progress.json",
	"watched_items.json",
	"user_settings.json",
}

var backupFilesDB = []string{
	"settings.json",
	adminDashboardLayoutFile,
}

func (s *Service) useDB() bool { return s.store != nil }

// SetDataStore sets the PostgreSQL backing store for DB-aware backups.
func (s *Service) SetDataStore(store *datastore.DataStore) {
	s.store = store
}

// NewService creates a new backup service
func NewService(cacheDir string, configManager *config.Manager) (*Service, error) {
	backupDir := filepath.Join(cacheDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}

	service := &Service{
		backupDir:     backupDir,
		cacheDir:      cacheDir,
		configManager: configManager,
	}
	if configManager != nil && configManager.ConfigPath() != "" {
		service.dashboardLayoutPath = filepath.Join(filepath.Dir(configManager.ConfigPath()), adminDashboardLayoutFile)
	}
	return service, nil
}

func (s *Service) sourcePath(filename string) string {
	if filename == adminDashboardLayoutFile && s.dashboardLayoutPath != "" {
		return s.dashboardLayoutPath
	}
	return filepath.Join(s.cacheDir, filename)
}

// CreateBackup creates a new backup archive
func (s *Service) CreateBackup(backupType BackupType) (*BackupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate backup filename with timestamp
	timestamp := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("mediastorm_backup_%s.zip", timestamp)
	backupPath := filepath.Join(s.backupDir, filename)
	// Timestamps have one-second resolution; never overwrite an existing backup
	// (e.g. a pre_restore backup taken in the same second as a scheduled one).
	for n := 2; ; n++ {
		if _, statErr := os.Stat(backupPath); os.IsNotExist(statErr) {
			break
		}
		filename = fmt.Sprintf("mediastorm_backup_%s-%d.zip", timestamp, n)
		backupPath = filepath.Join(s.backupDir, filename)
	}

	// Create temporary file first
	tmpPath := backupPath + ".tmp"
	zipFile, err := os.Create(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("create backup file: %w", err)
	}

	zipWriter := zip.NewWriter(zipFile)

	// Create manifest
	manifest := Manifest{
		Version:   "1.0",
		CreatedAt: time.Now().UTC(),
		Type:      backupType,
		Files:     make(map[string]string),
	}

	// Select file list based on storage mode
	filesToBackup := backupFiles
	if s.useDB() {
		filesToBackup = backupFilesDB
	}

	// Add files to backup
	for _, filename := range filesToBackup {
		srcPath := s.sourcePath(filename)

		// Check if file exists
		stat, err := os.Stat(srcPath)
		if os.IsNotExist(err) {
			log.Printf("[backup] Skipping %s (not found)", filename)
			continue
		}
		if err != nil {
			log.Printf("[backup] Error checking %s: %v", filename, err)
			continue
		}

		// Skip directories
		if stat.IsDir() {
			continue
		}

		// Special handling for SQLite database - use VACUUM INTO for safe copy
		if strings.HasSuffix(filename, ".db") {
			checksum, err := s.backupSQLiteDB(zipWriter, srcPath, filename)
			if err != nil {
				log.Printf("[backup] Warning: failed to backup %s: %v", filename, err)
				continue
			}
			manifest.Files[filename] = checksum
			log.Printf("[backup] Added %s (database)", filename)
		} else {
			checksum, err := s.addFileToZip(zipWriter, srcPath, filename)
			if err != nil {
				log.Printf("[backup] Warning: failed to backup %s: %v", filename, err)
				continue
			}
			manifest.Files[filename] = checksum
			log.Printf("[backup] Added %s", filename)
		}
	}

	// Export database tables as JSON when using PostgreSQL
	if s.useDB() {
		dbChecksum, err := s.exportDatabaseToZip(zipWriter)
		if err != nil {
			zipWriter.Close()
			zipFile.Close()
			os.Remove(tmpPath)
			return nil, fmt.Errorf("export database: %w", err)
		}
		manifest.Files["database.json"] = dbChecksum
		log.Printf("[backup] Added database.json (PostgreSQL export)")
	}

	// Write manifest
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		zipWriter.Close()
		zipFile.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}

	manifestWriter, err := zipWriter.Create("manifest.json")
	if err != nil {
		zipWriter.Close()
		zipFile.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("create manifest in zip: %w", err)
	}

	if _, err := manifestWriter.Write(manifestJSON); err != nil {
		zipWriter.Close()
		zipFile.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	// Close zip writer and file
	if err := zipWriter.Close(); err != nil {
		zipFile.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("close zip writer: %w", err)
	}

	if err := zipFile.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("close zip file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("finalize backup: %w", err)
	}

	// Get file info
	stat, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("stat backup: %w", err)
	}

	info := &BackupInfo{
		Filename:  filename,
		Size:      stat.Size(),
		CreatedAt: manifest.CreatedAt,
		Type:      backupType,
		Version:   manifest.Version,
	}

	log.Printf("[backup] Created backup: %s (%d bytes, %d files)", filename, info.Size, len(manifest.Files))
	return info, nil
}

// addFileToZip adds a regular file to the zip archive
func (s *Service) addFileToZip(zipWriter *zip.Writer, srcPath, destName string) (string, error) {
	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Calculate checksum while reading
	hasher := sha256.New()
	teeReader := io.TeeReader(file, hasher)

	writer, err := zipWriter.Create(destName)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(writer, teeReader); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// backupSQLiteDB safely backs up a SQLite database using a temporary copy
func (s *Service) backupSQLiteDB(zipWriter *zip.Writer, srcPath, destName string) (string, error) {
	// For SQLite databases, we'll copy the file while it might be in use
	// This is safe for reading as SQLite handles concurrent access
	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	teeReader := io.TeeReader(file, hasher)

	writer, err := zipWriter.Create(destName)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(writer, teeReader); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ListBackups returns all available backups sorted by creation time (newest first)
func (s *Service) ListBackups() ([]BackupInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupInfo{}, nil
		}
		return nil, fmt.Errorf("read backup directory: %w", err)
	}

	var backups []BackupInfo

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasPrefix(name, "mediastorm_backup_") || !strings.HasSuffix(name, ".zip") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Printf("[backup] Error getting info for %s: %v", name, err)
			continue
		}

		// Try to read manifest for more details
		backup := BackupInfo{
			Filename:  name,
			Size:      info.Size(),
			CreatedAt: info.ModTime(),
			Type:      BackupTypeManual, // Default, will be overwritten if manifest exists
		}

		// Read manifest from zip
		zipPath := filepath.Join(s.backupDir, name)
		manifest, err := s.readManifest(zipPath)
		if err == nil {
			backup.CreatedAt = manifest.CreatedAt
			backup.Type = manifest.Type
			backup.Version = manifest.Version
		}

		backups = append(backups, backup)
	}

	// Sort by creation time, newest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	return backups, nil
}

// readManifest reads the manifest from a backup zip file
func (s *Service) readManifest(zipPath string) (*Manifest, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	for _, file := range reader.File {
		if file.Name == "manifest.json" {
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()

			var manifest Manifest
			if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
				return nil, err
			}

			return &manifest, nil
		}
	}

	return nil, errors.New("manifest not found in backup")
}

// DeleteBackup removes a backup file
func (s *Service) DeleteBackup(filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate filename to prevent directory traversal
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.HasPrefix(filename, ".") {
		return errors.New("invalid backup filename")
	}

	if !strings.HasPrefix(filename, "mediastorm_backup_") || !strings.HasSuffix(filename, ".zip") {
		return errors.New("invalid backup filename format")
	}

	backupPath := filepath.Join(s.backupDir, filename)

	// Check if file exists
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return errors.New("backup not found")
	}

	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}

	log.Printf("[backup] Deleted backup: %s", filename)
	return nil
}

// RestoreBackup restores from a backup file
func (s *Service) RestoreBackup(filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate filename
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.HasPrefix(filename, ".") {
		return errors.New("invalid backup filename")
	}

	if !strings.HasPrefix(filename, "mediastorm_backup_") || !strings.HasSuffix(filename, ".zip") {
		return errors.New("invalid backup filename format")
	}

	backupPath := filepath.Join(s.backupDir, filename)

	// Check if file exists
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return errors.New("backup not found")
	}

	return s.restoreBackupFile(backupPath, filename)
}

// RestoreBackupUpload restores a backup supplied by the caller without adding it
// to the managed backup list. The upload is staged on disk because archive/zip
// requires random access to the archive.
func (s *Service) RestoreBackupUpload(reader io.Reader, sourceName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if reader == nil {
		return errors.New("backup file is required")
	}
	if !strings.EqualFold(filepath.Ext(sourceName), ".zip") {
		return errors.New("backup file must be a ZIP archive")
	}

	tmpFile, err := os.CreateTemp(s.backupDir, ".restore-upload-*.zip")
	if err != nil {
		return fmt.Errorf("stage uploaded backup: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, reader); err != nil {
		tmpFile.Close()
		return fmt.Errorf("stage uploaded backup: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("stage uploaded backup: %w", err)
	}

	return s.restoreBackupFile(tmpPath, filepath.Base(sourceName))
}

func (s *Service) restoreBackupFile(backupPath, sourceName string) error {
	// Read and validate manifest
	manifest, err := s.readManifest(backupPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	log.Printf("[backup] Restoring from backup: %s (created %s)", sourceName, manifest.CreatedAt.Format(time.RFC3339))

	// Open zip for reading
	reader, err := zip.OpenReader(backupPath)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer reader.Close()

	// In DB mode, restore database.json before mutating file-based settings.
	// This avoids reporting a failed restore after settings have already changed.
	databaseRestored := false
	if s.useDB() {
		for _, file := range reader.File {
			if file.Name != "database.json" {
				continue
			}
			expectedChecksum, ok := manifest.Files[file.Name]
			if !ok {
				return fmt.Errorf("database.json missing from manifest")
			}
			checksum, err := s.importDatabaseFromZip(file)
			if err != nil {
				return fmt.Errorf("restore database: %w", err)
			}
			if checksum != expectedChecksum {
				return fmt.Errorf("checksum mismatch for database.json")
			}
			databaseRestored = true
			log.Printf("[backup] Restored database.json (PostgreSQL import)")
			break
		}
		if !databaseRestored {
			if _, ok := manifest.Files["database.json"]; ok {
				return fmt.Errorf("database.json missing from backup")
			}
		}
	}

	// Extract files
	restoredCount := 0
	if databaseRestored {
		restoredCount++
	}
	for _, file := range reader.File {
		// Skip manifest
		if file.Name == "manifest.json" {
			continue
		}

		// Only restore known files
		if !isRestorableBackupFile(file.Name) {
			log.Printf("[backup] Skipping unknown file in backup: %s", file.Name)
			continue
		}
		expectedChecksum, ok := manifest.Files[file.Name]
		if !ok {
			log.Printf("[backup] Skipping file missing from manifest: %s", file.Name)
			continue
		}

		// Handle database.json restore into PostgreSQL
		if file.Name == "database.json" && s.useDB() {
			if !databaseRestored {
				return fmt.Errorf("database.json was not restored")
			}
			continue
		}

		destPath := s.sourcePath(file.Name)

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("create directory for %s: %w", file.Name, err)
		}

		// Extract file to temp path first
		tmpPath := destPath + ".restore.tmp"
		checksum, err := s.extractFile(file, tmpPath)
		if err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("extract %s: %w", file.Name, err)
		}

		// Verify checksum
		if checksum != expectedChecksum {
			os.Remove(tmpPath)
			return fmt.Errorf("checksum mismatch for %s", file.Name)
		}

		// Atomic rename
		if err := os.Rename(tmpPath, destPath); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("finalize %s: %w", file.Name, err)
		}

		restoredCount++
		log.Printf("[backup] Restored %s", file.Name)
	}

	log.Printf("[backup] Restore completed: %d files restored from %s", restoredCount, sourceName)
	return nil
}

func isRestorableBackupFile(name string) bool {
	if name == "database.json" {
		return true
	}
	for _, allowed := range backupFiles {
		if name == allowed {
			return true
		}
	}
	return false
}

// extractFile extracts a file from the zip archive
func (s *Service) extractFile(file *zip.File, destPath string) (string, error) {
	rc, err := file.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	outFile, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(outFile, hasher)

	if _, err := io.Copy(writer, rc); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// GetBackupReader returns a reader for downloading a backup file
func (s *Service) GetBackupReader(filename string) (io.ReadCloser, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Validate filename
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.HasPrefix(filename, ".") {
		return nil, 0, errors.New("invalid backup filename")
	}

	if !strings.HasPrefix(filename, "mediastorm_backup_") || !strings.HasSuffix(filename, ".zip") {
		return nil, 0, errors.New("invalid backup filename format")
	}

	backupPath := filepath.Join(s.backupDir, filename)

	file, err := os.Open(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, errors.New("backup not found")
		}
		return nil, 0, err
	}

	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}

	return file, stat.Size(), nil
}

// CleanupOldBackups removes old backups based on retention settings
func (s *Service) CleanupOldBackups() (int, error) {
	settings, err := s.configManager.Load()
	if err != nil {
		return 0, fmt.Errorf("load settings: %w", err)
	}

	retention := settings.BackupRetention
	if retention.RetentionDays == 0 && retention.RetentionCount == 0 {
		// No cleanup configured
		return 0, nil
	}

	backups, err := s.ListBackups()
	if err != nil {
		return 0, fmt.Errorf("list backups: %w", err)
	}

	// Track which backups to delete
	toDelete := make(map[string]bool)

	// Apply retention days rule
	if retention.RetentionDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -retention.RetentionDays)
		for _, backup := range backups {
			if backup.Type == BackupTypePreRestore {
				continue // safety net taken before a restore; only deleted manually
			}
			if backup.CreatedAt.Before(cutoff) {
				toDelete[backup.Filename] = true
			}
		}
	}

	// Apply retention count rule (keep newest N backups)
	// Backups are sorted newest first; pre_restore backups are never pruned and
	// do not count against the limit.
	kept := 0
	for _, backup := range backups {
		if backup.Type == BackupTypePreRestore {
			continue
		}
		kept++
		if retention.RetentionCount > 0 && kept > retention.RetentionCount {
			toDelete[backup.Filename] = true
		}
	}

	// Delete marked backups
	deleted := 0
	for filename := range toDelete {
		if err := s.DeleteBackup(filename); err != nil {
			log.Printf("[backup] Warning: failed to delete old backup %s: %v", filename, err)
			continue
		}
		deleted++
	}

	if deleted > 0 {
		log.Printf("[backup] Cleaned up %d old backups", deleted)
	}

	return deleted, nil
}

// GetBackupDir returns the backup directory path
func (s *Service) GetBackupDir() string {
	return s.backupDir
}

// ExportDatabaseJSON returns the full database as portable JSON bytes.
func (s *Service) ExportDatabaseJSON() ([]byte, error) {
	if !s.useDB() {
		return nil, errors.New("database export requires PostgreSQL")
	}
	return s.exportDatabaseBytes()
}

// ImportDatabaseJSON imports data from portable JSON bytes into the database.
func (s *Service) ImportDatabaseJSON(data []byte) error {
	if !s.useDB() {
		return errors.New("database import requires PostgreSQL")
	}
	return s.importDatabaseBytes(data)
}

// --- PostgreSQL database export/import ---

// rawUser aliases models.User to bypass MarshalJSON so PinHash is preserved in exports.
type rawUser models.User

// rawShareLink preserves fields that API responses intentionally omit.
type rawShareLink struct {
	Token      string            `json:"token"`
	AccountID  string            `json:"accountId"`
	IsMaster   bool              `json:"isMaster"`
	Label      string            `json:"label,omitempty"`
	Params     map[string]string `json:"params"`
	MaxUses    int               `json:"maxUses"`
	UseCount   int               `json:"useCount"`
	Active     bool              `json:"active"`
	CreatedAt  time.Time         `json:"createdAt"`
	ExpiresAt  time.Time         `json:"expiresAt"`
	LastUsedAt *time.Time        `json:"lastUsedAt,omitempty"`
}

// rawRemoteAccessInvite preserves TokenHash, which is omitted from API JSON.
type rawRemoteAccessInvite struct {
	ID             string     `json:"id"`
	TokenHash      string     `json:"tokenHash"`
	ConnectionCode string     `json:"connectionCode,omitempty"`
	IrohInvite     string     `json:"irohInvite,omitempty"`
	CreatedBy      string     `json:"createdBy"`
	PeerName       string     `json:"peerName"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	UsedAt         *time.Time `json:"usedAt,omitempty"`
	UsedByPeerID   string     `json:"usedByPeerId,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

type rawRemoteAccessPairing struct {
	ID                  string     `json:"id"`
	InviteID            *string    `json:"inviteId,omitempty"`
	PeerID              string     `json:"peerId"`
	CredentialHash      string     `json:"credentialHash"`
	PeerName            string     `json:"peerName,omitempty"`
	CreatedBy           string     `json:"createdBy"`
	CreatedAt           time.Time  `json:"createdAt"`
	LastAuthenticatedAt *time.Time `json:"lastAuthenticatedAt,omitempty"`
	RevokedAt           *time.Time `json:"revokedAt,omitempty"`
}

// localMediaItemExport includes file path fields that API models hide with json:"-".
type localMediaItemExport struct {
	ID               string                       `json:"id"`
	LibraryID        string                       `json:"libraryId"`
	RelativePath     string                       `json:"relativePath"`
	FilePath         string                       `json:"filePath"`
	FileName         string                       `json:"fileName"`
	LibraryType      models.LocalMediaLibraryType `json:"libraryType"`
	DetectedTitle    string                       `json:"detectedTitle,omitempty"`
	DetectedYear     int                          `json:"detectedYear,omitempty"`
	SeasonNumber     int                          `json:"seasonNumber,omitempty"`
	EpisodeNumber    int                          `json:"episodeNumber,omitempty"`
	Confidence       float64                      `json:"confidence"`
	MatchStatus      models.LocalMediaMatchStatus `json:"matchStatus"`
	MatchedTitleID   string                       `json:"matchedTitleId,omitempty"`
	MatchedMediaType string                       `json:"matchedMediaType,omitempty"`
	MatchedName      string                       `json:"matchedName,omitempty"`
	MatchedYear      int                          `json:"matchedYear,omitempty"`
	IsMissing        bool                         `json:"isMissing,omitempty"`
	MissingSince     *time.Time                   `json:"missingSince,omitempty"`
	Metadata         *models.Title                `json:"metadata,omitempty"`
	Probe            *models.LocalMediaProbe      `json:"probe,omitempty"`
	SizeBytes        int64                        `json:"sizeBytes"`
	ModifiedAt       *time.Time                   `json:"modifiedAt,omitempty"`
	LastScannedAt    *time.Time                   `json:"lastScannedAt,omitempty"`
	LastSeenScanID   string                       `json:"lastSeenScanId,omitempty"`
	CreatedAt        time.Time                    `json:"createdAt"`
	UpdatedAt        time.Time                    `json:"updatedAt"`
}

// databaseExport is the JSON structure for database backups.
//
// Intentionally excluded (regenerable / ephemeral / system):
//   - prequeue, prewarm (warm cache, expires)
//   - notification_progress_messages (live Discord message IDs)
//   - import_queue, file_health, media_files, queue_stats
//   - goose_db_version, app_data_migrations
//   - quality_snapshot_cache (downloaded TRaSH archives, refetched on demand)
//   - title_quality_presets (presets remembered per title and device; the
//     next pick remembers again)
//
// Global app settings (scheduled tasks / automations / scrapers / indexers / etc.)
// live in settings.json and are backed up as a separate archive member.
type databaseExport struct {
	Version                  string                                 `json:"version"`
	ExportedAt               time.Time                              `json:"exportedAt"`
	Accounts                 []models.AccountStorage                `json:"accounts"`
	Users                    []rawUser                              `json:"users"`
	Sessions                 []models.Session                       `json:"sessions"`
	Invitations              []models.Invitation                    `json:"invitations"`
	RemoteAccessInvites      []rawRemoteAccessInvite                `json:"remoteAccessInvites,omitempty"`
	RemoteAccessPairings     []rawRemoteAccessPairing               `json:"remoteAccessPairings,omitempty"`
	ShareLinks               []rawShareLink                         `json:"shareLinks,omitempty"`
	Clients                  []models.Client                        `json:"clients"`
	ClientSettings           map[string]models.ClientFilterSettings `json:"clientSettings"`
	UserSettings             map[string]models.UserSettings         `json:"userSettings"`
	Watchlist                map[string][]models.WatchlistItem      `json:"watchlist"`
	WatchlistTombstones      map[string][]models.WatchlistTombstone `json:"watchlistTombstones,omitempty"`
	HiddenItems              map[string][]models.HiddenItem         `json:"hiddenItems,omitempty"`
	WatchHistory             map[string][]models.WatchHistoryItem   `json:"watchHistory"`
	PlaybackProgress         map[string][]models.PlaybackProgress   `json:"playbackProgress"`
	ContentPreferences       map[string][]models.ContentPreference  `json:"contentPreferences"`
	SeriesOrdering           map[string][]models.SeriesOrderingPref `json:"seriesOrdering,omitempty"`
	CustomLists              map[string][]customListExport          `json:"customLists"`
	NotificationChannels     []models.NotificationChannel           `json:"notificationChannels,omitempty"`
	NotificationObservations []models.NotificationObservation       `json:"notificationObservations,omitempty"`
	LocalMediaLibraries      []models.LocalMediaLibrary             `json:"localMediaLibraries,omitempty"`
	LocalMediaItems          []localMediaItemExport                 `json:"localMediaItems,omitempty"`
	RemoteMediaLibraries     []models.RemoteMediaLibrary            `json:"remoteMediaLibraries,omitempty"`
	RemoteMediaItems         []models.RemoteMediaItem               `json:"remoteMediaItems,omitempty"`
	LibraryAccessPolicies    map[string]models.LibraryAccessPolicy  `json:"libraryAccessPolicies,omitempty"`
	Recordings               []models.Recording                     `json:"recordings,omitempty"`
	NumbersStationProgress   []models.NumbersStationProgress        `json:"numbersStationProgress"`
	QualityPresets           []*qualityprofiles.Preset              `json:"qualityPresets,omitempty"`
}

type customListExport struct {
	List  models.CustomList      `json:"list"`
	Items []models.WatchlistItem `json:"items"`
}

// databaseExportSections lists durable database sections expected in modern backups.
// Used by tests to prevent silent coverage regressions.
var databaseExportSections = []string{
	"accounts",
	"users",
	"sessions",
	"invitations",
	"remoteAccessInvites",
	"remoteAccessPairings",
	"shareLinks",
	"clients",
	"clientSettings",
	"userSettings",
	"watchlist",
	"watchlistTombstones",
	"hiddenItems",
	"watchHistory",
	"playbackProgress",
	"contentPreferences",
	"seriesOrdering",
	"customLists",
	"notificationChannels",
	"notificationObservations",
	"localMediaLibraries",
	"localMediaItems",
	"remoteMediaLibraries",
	"remoteMediaItems",
	"libraryAccessPolicies",
	"recordings",
	"numbersStationProgress",
	"qualityPresets",
}

// exportDatabaseBytes queries durable tables and returns the export as JSON bytes.
func (s *Service) exportDatabaseBytes() ([]byte, error) {
	ctx := context.Background()
	export := databaseExport{
		Version:    "2.1",
		ExportedAt: time.Now().UTC(),
	}

	accounts, err := s.store.Accounts().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export accounts: %w", err)
	}
	export.Accounts = make([]models.AccountStorage, 0, len(accounts))
	for _, a := range accounts {
		export.Accounts = append(export.Accounts, a.ToStorage())
	}

	numbersStationProgress, err := s.store.NumbersStation().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export numbers station progress: %w", err)
	}
	export.NumbersStationProgress = numbersStationProgress

	users, err := s.store.Users().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export users: %w", err)
	}
	export.Users = make([]rawUser, len(users))
	for i, u := range users {
		export.Users[i] = rawUser(u)
	}

	sessions, err := s.store.Sessions().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export sessions: %w", err)
	}
	export.Sessions = sessions

	invitations, err := s.store.Invitations().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export invitations: %w", err)
	}
	export.Invitations = invitations

	remoteInvites, err := s.store.RemoteAccessInvites().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export remote access invites: %w", err)
	}
	export.RemoteAccessInvites = make([]rawRemoteAccessInvite, 0, len(remoteInvites))
	for _, inv := range remoteInvites {
		export.RemoteAccessInvites = append(export.RemoteAccessInvites, rawRemoteAccessInvite{
			ID: inv.ID, TokenHash: inv.TokenHash, ConnectionCode: inv.ConnectionCode,
			IrohInvite: inv.IrohInvite, CreatedBy: inv.CreatedBy, PeerName: inv.PeerName,
			ExpiresAt: inv.ExpiresAt, UsedAt: inv.UsedAt, UsedByPeerID: inv.UsedByPeerID,
			RevokedAt: inv.RevokedAt, CreatedAt: inv.CreatedAt,
		})
	}
	remotePairings, err := s.store.RemoteAccessPairings().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export remote access pairings: %w", err)
	}
	export.RemoteAccessPairings = make([]rawRemoteAccessPairing, 0, len(remotePairings))
	for _, pairing := range remotePairings {
		export.RemoteAccessPairings = append(export.RemoteAccessPairings, rawRemoteAccessPairing{
			ID: pairing.ID, InviteID: pairing.InviteID, PeerID: pairing.PeerID,
			CredentialHash: pairing.CredentialHash, PeerName: pairing.PeerName, CreatedBy: pairing.CreatedBy,
			CreatedAt: pairing.CreatedAt, LastAuthenticatedAt: pairing.LastAuthenticatedAt, RevokedAt: pairing.RevokedAt,
		})
	}

	shareLinks, err := s.store.ShareLinks().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("export share links: %w", err)
	}
	export.ShareLinks = make([]rawShareLink, 0, len(shareLinks))
	for _, link := range shareLinks {
		params := link.Params
		if params == nil {
			params = map[string]string{}
		}
		export.ShareLinks = append(export.ShareLinks, rawShareLink{
			Token: link.Token, AccountID: link.AccountID, IsMaster: link.IsMaster,
			Label: link.Label, Params: params, MaxUses: link.MaxUses, UseCount: link.UseCount,
			Active: link.Active, CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt,
			LastUsedAt: link.LastUsedAt,
		})
	}

	clients, err := s.store.Clients().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export clients: %w", err)
	}
	export.Clients = clients

	clientSettings, err := s.store.ClientSettings().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export client settings: %w", err)
	}
	export.ClientSettings = clientSettings

	userSettings, err := s.store.UserSettings().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export user settings: %w", err)
	}
	export.UserSettings = userSettings

	watchlist, err := s.store.Watchlist().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("export watchlist: %w", err)
	}
	export.Watchlist = watchlist

	tombstones, err := s.store.Watchlist().ListTombstonesAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("export watchlist tombstones: %w", err)
	}
	export.WatchlistTombstones = tombstones

	hiddenItems, err := s.store.HiddenItems().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("export hidden items: %w", err)
	}
	export.HiddenItems = hiddenItems

	watchHistory, err := s.store.WatchHistory().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("export watch history: %w", err)
	}
	export.WatchHistory = watchHistory

	playbackProgress, err := s.store.PlaybackProgress().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("export playback progress: %w", err)
	}
	export.PlaybackProgress = playbackProgress

	seriesOrdering, err := s.store.SeriesOrdering().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("export series ordering: %w", err)
	}
	export.SeriesOrdering = seriesOrdering

	notificationChannels, err := s.store.Notifications().ListAllChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("export notification channels: %w", err)
	}
	export.NotificationChannels = notificationChannels

	notificationObservations, err := s.store.Notifications().ListAllObservations(ctx)
	if err != nil {
		return nil, fmt.Errorf("export notification observations: %w", err)
	}
	export.NotificationObservations = notificationObservations

	localLibraries, err := s.store.LocalMedia().ListLibraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("export local media libraries: %w", err)
	}
	export.LocalMediaLibraries = localLibraries
	export.LocalMediaItems = make([]localMediaItemExport, 0)
	for _, library := range localLibraries {
		items, err := s.store.LocalMedia().ListAllItemsByLibrary(ctx, library.ID)
		if err != nil {
			return nil, fmt.Errorf("export local media items for %s: %w", library.ID, err)
		}
		for _, item := range items {
			export.LocalMediaItems = append(export.LocalMediaItems, localMediaItemExport{
				ID: item.ID, LibraryID: item.LibraryID, RelativePath: item.RelativePath,
				FilePath: item.FilePath, FileName: item.FileName, LibraryType: item.LibraryType,
				DetectedTitle: item.DetectedTitle, DetectedYear: item.DetectedYear,
				SeasonNumber: item.SeasonNumber, EpisodeNumber: item.EpisodeNumber,
				Confidence: item.Confidence, MatchStatus: item.MatchStatus,
				MatchedTitleID: item.MatchedTitleID, MatchedMediaType: item.MatchedMediaType,
				MatchedName: item.MatchedName, MatchedYear: item.MatchedYear,
				IsMissing: item.IsMissing, MissingSince: item.MissingSince,
				Metadata: item.Metadata, Probe: item.Probe, SizeBytes: item.SizeBytes,
				ModifiedAt: item.ModifiedAt, LastScannedAt: item.LastScannedAt,
				LastSeenScanID: item.LastSeenScanID, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
			})
		}
	}

	remoteLibraries, err := s.store.RemoteMedia().ListLibraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("export remote media libraries: %w", err)
	}
	export.RemoteMediaLibraries = remoteLibraries
	export.RemoteMediaItems = make([]models.RemoteMediaItem, 0)
	for _, library := range remoteLibraries {
		items, err := s.store.RemoteMedia().ListItems(ctx, library.ID, true)
		if err != nil {
			return nil, fmt.Errorf("export remote media items for %s: %w", library.ID, err)
		}
		export.RemoteMediaItems = append(export.RemoteMediaItems, items...)
	}

	libraryAccess, err := s.store.LibraryAccess().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export library access policies: %w", err)
	}
	export.LibraryAccessPolicies = libraryAccess

	recordings, err := s.store.Recordings().List(ctx, models.RecordingListFilter{IncludeAll: true})
	if err != nil {
		return nil, fmt.Errorf("export recordings: %w", err)
	}
	export.Recordings = recordings

	presets, err := s.store.QualityPresets().ListPresets(ctx)
	if err != nil {
		return nil, fmt.Errorf("export quality presets: %w", err)
	}
	export.QualityPresets = presets

	export.ContentPreferences = make(map[string][]models.ContentPreference)
	export.CustomLists = make(map[string][]customListExport)
	for _, u := range users {
		prefs, err := s.store.ContentPreferences().ListByUser(ctx, u.ID)
		if err != nil {
			return nil, fmt.Errorf("export content preferences for %s: %w", u.ID, err)
		}
		if len(prefs) > 0 {
			export.ContentPreferences[u.ID] = prefs
		}

		lists, err := s.store.CustomLists().ListByUser(ctx, u.ID)
		if err != nil {
			return nil, fmt.Errorf("export custom lists for %s: %w", u.ID, err)
		}
		for _, list := range lists {
			items, err := s.store.CustomLists().GetItems(ctx, list.ID)
			if err != nil {
				return nil, fmt.Errorf("export custom list %s items: %w", list.ID, err)
			}
			export.CustomLists[u.ID] = append(export.CustomLists[u.ID], customListExport{
				List:  list,
				Items: items,
			})
		}
	}

	return json.MarshalIndent(export, "", "  ")
}

// importDatabaseBytes parses JSON data and restores all tables in a transaction.
func (s *Service) importDatabaseBytes(data []byte) error {
	var export databaseExport
	if err := json.Unmarshal(data, &export); err != nil {
		return fmt.Errorf("unmarshal database export: %w", err)
	}

	ctx := context.Background()
	err := s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Clear durable data that does not cascade from users/accounts first.
		if err := clearNonCascadedBackupTables(ctx, tx); err != nil {
			return err
		}

		// Delete all existing account/profile data. Profiles no longer cascade
		// from accounts, so remove them explicitly before inserting the backup.
		// User delete cascades notifications, hidden items, tombstones, recordings, etc.
		existingUsers, err := tx.Users().List(ctx)
		if err != nil {
			return fmt.Errorf("list existing users: %w", err)
		}
		for _, u := range existingUsers {
			if err := tx.Users().Delete(ctx, u.ID); err != nil {
				return fmt.Errorf("delete user %s: %w", u.ID, err)
			}
		}

		existingAccounts, err := tx.Accounts().List(ctx)
		if err != nil {
			return fmt.Errorf("list existing accounts: %w", err)
		}
		for _, a := range existingAccounts {
			if err := tx.Accounts().Delete(ctx, a.ID); err != nil {
				return fmt.Errorf("delete account %s: %w", a.ID, err)
			}
		}

		// Insert in FK dependency order
		for _, as := range export.Accounts {
			acct := as.ToAccount()
			if err := tx.Accounts().Create(ctx, &acct); err != nil {
				return fmt.Errorf("restore account %s: %w", acct.ID, err)
			}
		}
		for i := range export.Users {
			u := models.User(export.Users[i])
			if err := tx.Users().Create(ctx, &u); err != nil {
				return fmt.Errorf("restore user %s: %w", u.ID, err)
			}
		}
		for i := range export.NumbersStationProgress {
			progress := export.NumbersStationProgress[i]
			if err := tx.NumbersStation().Upsert(ctx, &progress); err != nil {
				return fmt.Errorf("restore numbers station progress for %s: %w", progress.AccountID, err)
			}
		}
		for i := range export.Sessions {
			if err := tx.Sessions().Create(ctx, &export.Sessions[i]); err != nil {
				return fmt.Errorf("restore session: %w", err)
			}
		}
		for i := range export.Invitations {
			if err := tx.Invitations().Create(ctx, &export.Invitations[i]); err != nil {
				return fmt.Errorf("restore invitation %s: %w", export.Invitations[i].ID, err)
			}
		}
		for i := range export.RemoteAccessInvites {
			inv := models.RemoteAccessInvite{
				ID: export.RemoteAccessInvites[i].ID, TokenHash: export.RemoteAccessInvites[i].TokenHash,
				ConnectionCode: export.RemoteAccessInvites[i].ConnectionCode,
				IrohInvite:     export.RemoteAccessInvites[i].IrohInvite, CreatedBy: export.RemoteAccessInvites[i].CreatedBy,
				PeerName: export.RemoteAccessInvites[i].PeerName, ExpiresAt: export.RemoteAccessInvites[i].ExpiresAt,
				UsedAt: export.RemoteAccessInvites[i].UsedAt, UsedByPeerID: export.RemoteAccessInvites[i].UsedByPeerID,
				RevokedAt: export.RemoteAccessInvites[i].RevokedAt, CreatedAt: export.RemoteAccessInvites[i].CreatedAt,
			}
			if err := tx.RemoteAccessInvites().Create(ctx, &inv); err != nil {
				return fmt.Errorf("restore remote access invite %s: %w", inv.ID, err)
			}
		}
		for i := range export.RemoteAccessPairings {
			pairing := models.RemoteAccessPairing{
				ID: export.RemoteAccessPairings[i].ID, InviteID: export.RemoteAccessPairings[i].InviteID,
				PeerID: export.RemoteAccessPairings[i].PeerID, CredentialHash: export.RemoteAccessPairings[i].CredentialHash,
				PeerName: export.RemoteAccessPairings[i].PeerName, CreatedBy: export.RemoteAccessPairings[i].CreatedBy,
				CreatedAt:           export.RemoteAccessPairings[i].CreatedAt,
				LastAuthenticatedAt: export.RemoteAccessPairings[i].LastAuthenticatedAt,
				RevokedAt:           export.RemoteAccessPairings[i].RevokedAt,
			}
			if err := tx.RemoteAccessPairings().Create(ctx, &pairing); err != nil {
				return fmt.Errorf("restore remote access pairing %s: %w", pairing.ID, err)
			}
		}
		for i := range export.ShareLinks {
			params := export.ShareLinks[i].Params
			if params == nil {
				params = map[string]string{}
			}
			link := models.ShareLink{
				Token: export.ShareLinks[i].Token, AccountID: export.ShareLinks[i].AccountID,
				IsMaster: export.ShareLinks[i].IsMaster, Label: export.ShareLinks[i].Label,
				Params: params, MaxUses: export.ShareLinks[i].MaxUses, UseCount: export.ShareLinks[i].UseCount,
				Active: export.ShareLinks[i].Active, CreatedAt: export.ShareLinks[i].CreatedAt,
				ExpiresAt: export.ShareLinks[i].ExpiresAt, LastUsedAt: export.ShareLinks[i].LastUsedAt,
			}
			if err := tx.ShareLinks().Create(ctx, &link); err != nil {
				return fmt.Errorf("restore share link: %w", err)
			}
		}
		for i := range export.Clients {
			c := export.Clients[i]
			if err := tx.Clients().Create(ctx, &c); err != nil {
				return fmt.Errorf("restore client %s: %w", c.ID, err)
			}
			if c.UserID != "" {
				if err := tx.Clients().UpsertProfile(ctx, models.ClientProfileAssociation{
					ClientID:    c.ID,
					UserID:      c.UserID,
					FirstSeenAt: c.FirstSeenAt,
					LastSeenAt:  c.LastSeenAt,
				}); err != nil {
					return fmt.Errorf("restore client profile %s/%s: %w", c.ID, c.UserID, err)
				}
			}
		}
		for userID, lists := range export.CustomLists {
			for _, entry := range lists {
				list := entry.List
				if err := tx.CustomLists().CreateList(ctx, userID, &list); err != nil {
					return fmt.Errorf("restore custom list %s: %w", list.ID, err)
				}
				for _, item := range entry.Items {
					it := item
					if err := tx.CustomLists().UpsertItem(ctx, list.ID, &it); err != nil {
						return fmt.Errorf("restore custom list %s item %s: %w", list.ID, it.Key(), err)
					}
				}
			}
		}
		clientLastUser := make(map[string]string, len(export.Clients))
		for _, c := range export.Clients {
			clientLastUser[c.ID] = c.UserID
		}
		for key, settings := range export.ClientSettings {
			cs := settings
			clientID, userID, ok := models.SplitClientSettingsKey(key)
			if !ok {
				clientID = key
				userID = clientLastUser[clientID]
			}
			if userID == "" {
				return fmt.Errorf("restore client settings %s: missing user id", key)
			}
			if err := tx.ClientSettings().Upsert(ctx, clientID, userID, &cs); err != nil {
				return fmt.Errorf("restore client settings %s/%s: %w", clientID, userID, err)
			}
			// Ensure association exists when settings imply a person×device pair
			if err := tx.Clients().UpsertProfile(ctx, models.ClientProfileAssociation{
				ClientID: clientID,
				UserID:   userID,
			}); err != nil {
				return fmt.Errorf("restore client profile from settings %s/%s: %w", clientID, userID, err)
			}
		}
		for userID, settings := range export.UserSettings {
			us := settings
			if err := tx.UserSettings().Upsert(ctx, userID, &us); err != nil {
				return fmt.Errorf("restore user settings %s: %w", userID, err)
			}
		}
		for userID, items := range export.Watchlist {
			if err := tx.Watchlist().BulkUpsert(ctx, userID, items); err != nil {
				return fmt.Errorf("restore watchlist for %s: %w", userID, err)
			}
		}
		for userID, items := range export.WatchlistTombstones {
			if err := tx.Watchlist().BulkUpsertTombstones(ctx, userID, items); err != nil {
				return fmt.Errorf("restore watchlist tombstones for %s: %w", userID, err)
			}
		}
		for userID, items := range export.HiddenItems {
			if err := tx.HiddenItems().BulkUpsert(ctx, userID, items); err != nil {
				return fmt.Errorf("restore hidden items for %s: %w", userID, err)
			}
		}
		for userID, items := range export.WatchHistory {
			if err := tx.WatchHistory().BulkUpsert(ctx, userID, items); err != nil {
				return fmt.Errorf("restore watch history for %s: %w", userID, err)
			}
		}
		for userID, items := range export.PlaybackProgress {
			if err := tx.PlaybackProgress().BulkUpsert(ctx, userID, items); err != nil {
				return fmt.Errorf("restore playback progress for %s: %w", userID, err)
			}
		}
		for userID, prefs := range export.ContentPreferences {
			for _, pref := range prefs {
				p := pref
				if err := tx.ContentPreferences().Upsert(ctx, userID, &p); err != nil {
					return fmt.Errorf("restore content preference for %s: %w", userID, err)
				}
			}
		}
		for userID, prefs := range export.SeriesOrdering {
			for _, pref := range prefs {
				p := pref
				if err := tx.SeriesOrdering().Upsert(ctx, userID, &p); err != nil {
					return fmt.Errorf("restore series ordering for %s: %w", userID, err)
				}
			}
		}
		for i := range export.NotificationChannels {
			channel := export.NotificationChannels[i]
			if err := tx.Notifications().CreateChannel(ctx, &channel); err != nil {
				return fmt.Errorf("restore notification channel %s: %w", channel.ID, err)
			}
		}
		for i := range export.NotificationObservations {
			observation := export.NotificationObservations[i]
			if err := tx.Notifications().UpsertObservation(ctx, &observation); err != nil {
				return fmt.Errorf("restore notification observation %s/%s: %w", observation.ProfileID, observation.ItemKey, err)
			}
		}
		for i := range export.LocalMediaLibraries {
			library := export.LocalMediaLibraries[i]
			if err := tx.LocalMedia().CreateLibrary(ctx, &library); err != nil {
				return fmt.Errorf("restore local media library %s: %w", library.ID, err)
			}
		}
		for i := range export.LocalMediaItems {
			item := export.LocalMediaItems[i]
			restored := models.LocalMediaItem{
				ID: item.ID, LibraryID: item.LibraryID, RelativePath: item.RelativePath,
				FilePath: item.FilePath, FileName: item.FileName, LibraryType: item.LibraryType,
				DetectedTitle: item.DetectedTitle, DetectedYear: item.DetectedYear,
				SeasonNumber: item.SeasonNumber, EpisodeNumber: item.EpisodeNumber,
				Confidence: item.Confidence, MatchStatus: item.MatchStatus,
				MatchedTitleID: item.MatchedTitleID, MatchedMediaType: item.MatchedMediaType,
				MatchedName: item.MatchedName, MatchedYear: item.MatchedYear,
				IsMissing: item.IsMissing, MissingSince: item.MissingSince,
				Metadata: item.Metadata, Probe: item.Probe, SizeBytes: item.SizeBytes,
				ModifiedAt: item.ModifiedAt, LastScannedAt: item.LastScannedAt,
				LastSeenScanID: item.LastSeenScanID, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
			}
			if err := tx.LocalMedia().UpsertItem(ctx, &restored); err != nil {
				return fmt.Errorf("restore local media item %s: %w", restored.ID, err)
			}
		}
		for i := range export.RemoteMediaLibraries {
			library := export.RemoteMediaLibraries[i]
			if err := tx.RemoteMedia().CreateLibrary(ctx, &library); err != nil {
				return fmt.Errorf("restore remote media library %s: %w", library.ID, err)
			}
		}
		for i := range export.RemoteMediaItems {
			item := export.RemoteMediaItems[i]
			if err := tx.RemoteMedia().UpsertItem(ctx, &item); err != nil {
				return fmt.Errorf("restore remote media item %s: %w", item.ID, err)
			}
		}
		for libraryID, policy := range export.LibraryAccessPolicies {
			policy.LibraryID = libraryID
			if err := tx.LibraryAccess().Set(ctx, policy); err != nil {
				return fmt.Errorf("restore library access policy %s: %w", libraryID, err)
			}
		}
		for i := range export.Recordings {
			recording := export.Recordings[i]
			if err := tx.Recordings().Create(ctx, &recording); err != nil {
				return fmt.Errorf("restore recording %s: %w", recording.ID, err)
			}
		}
		for _, preset := range export.QualityPresets {
			if preset == nil {
				continue
			}
			if err := tx.QualityPresets().SavePreset(ctx, preset); err != nil {
				return fmt.Errorf("restore quality preset %s: %w", preset.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("database restore transaction: %w", err)
	}

	log.Printf("[backup] Database restored: %d accounts, %d users, %d sessions, %d notification channels, %d local libraries, %d remote libraries",
		len(export.Accounts), len(export.Users), len(export.Sessions),
		len(export.NotificationChannels), len(export.LocalMediaLibraries), len(export.RemoteMediaLibraries))
	return nil
}

// clearNonCascadedBackupTables removes durable rows that are not FK-cascaded from
// users/accounts so restore can re-insert backup data without conflicts.
func clearNonCascadedBackupTables(ctx context.Context, tx *datastore.Tx) error {
	presets, err := tx.QualityPresets().ListPresets(ctx)
	if err != nil {
		return fmt.Errorf("list quality presets for clear: %w", err)
	}
	for _, p := range presets {
		if err := tx.QualityPresets().DeletePreset(ctx, p.ID); err != nil {
			return fmt.Errorf("clear quality preset %s: %w", p.ID, err)
		}
	}
	if err := tx.ShareLinks().DeleteAll(ctx); err != nil {
		return fmt.Errorf("clear share links: %w", err)
	}
	if err := tx.SeriesOrdering().DeleteAll(ctx); err != nil {
		return fmt.Errorf("clear series ordering: %w", err)
	}

	// Library access grants reference accounts/profiles; clear policies first.
	policies, err := tx.LibraryAccess().List(ctx)
	if err != nil {
		return fmt.Errorf("list library access policies for clear: %w", err)
	}
	for libraryID := range policies {
		if err := tx.LibraryAccess().Delete(ctx, libraryID); err != nil {
			return fmt.Errorf("clear library access policy %s: %w", libraryID, err)
		}
	}

	localLibraries, err := tx.LocalMedia().ListLibraries(ctx)
	if err != nil {
		return fmt.Errorf("list local media libraries for clear: %w", err)
	}
	for _, library := range localLibraries {
		if err := tx.LocalMedia().DeleteLibrary(ctx, library.ID); err != nil {
			return fmt.Errorf("clear local media library %s: %w", library.ID, err)
		}
	}

	remoteLibraries, err := tx.RemoteMedia().ListLibraries(ctx)
	if err != nil {
		return fmt.Errorf("list remote media libraries for clear: %w", err)
	}
	for _, library := range remoteLibraries {
		if err := tx.RemoteMedia().DeleteLibrary(ctx, library.ID); err != nil {
			return fmt.Errorf("clear remote media library %s: %w", library.ID, err)
		}
	}
	return nil
}

// exportDatabaseToZip queries all tables and writes database.json into the ZIP.
func (s *Service) exportDatabaseToZip(zipWriter *zip.Writer) (string, error) {
	data, err := s.exportDatabaseBytes()
	if err != nil {
		return "", err
	}

	hasher := sha256.New()
	hasher.Write(data)
	checksum := hex.EncodeToString(hasher.Sum(nil))

	writer, err := zipWriter.Create("database.json")
	if err != nil {
		return "", fmt.Errorf("create database.json in zip: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		return "", fmt.Errorf("write database.json: %w", err)
	}
	return checksum, nil
}

// importDatabaseFromZip reads database.json from the ZIP and restores all tables.
func (s *Service) importDatabaseFromZip(file *zip.File) (string, error) {
	rc, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("open database.json: %w", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("read database.json: %w", err)
	}

	hasher := sha256.New()
	hasher.Write(data)
	checksum := hex.EncodeToString(hasher.Sum(nil))

	if err := s.importDatabaseBytes(data); err != nil {
		return "", err
	}
	return checksum, nil
}
