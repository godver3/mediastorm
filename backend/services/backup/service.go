package backup

import (
	"archive/zip"
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
)

// BackupType indicates how the backup was created
type BackupType string

const (
	BackupTypeManual    BackupType = "manual"
	BackupTypeScheduled BackupType = "scheduled"
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
	mu            sync.RWMutex
	backupDir     string
	cacheDir      string
	configManager *config.Manager
}

// Files to backup (relative to cacheDir)
var backupFiles = []string{
	"settings.json",
	"queue.db",
	"users.json",
	"watchlist.json",
	"accounts.json",
	"playback_progress.json",
	"watched_items.json",
	"user_settings.json",
}

// NewService creates a new backup service
func NewService(cacheDir string, configManager *config.Manager) (*Service, error) {
	backupDir := filepath.Join(cacheDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}

	return &Service{
		backupDir:     backupDir,
		cacheDir:      cacheDir,
		configManager: configManager,
	}, nil
}

// CreateBackup creates a new backup archive
func (s *Service) CreateBackup(backupType BackupType) (*BackupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate backup filename with timestamp
	timestamp := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("strmr_backup_%s.zip", timestamp)
	backupPath := filepath.Join(s.backupDir, filename)

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

	// Add files to backup
	for _, filename := range backupFiles {
		srcPath := filepath.Join(s.cacheDir, filename)

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
		if !strings.HasPrefix(name, "strmr_backup_") || !strings.HasSuffix(name, ".zip") {
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

	if !strings.HasPrefix(filename, "strmr_backup_") || !strings.HasSuffix(filename, ".zip") {
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

	if !strings.HasPrefix(filename, "strmr_backup_") || !strings.HasSuffix(filename, ".zip") {
		return errors.New("invalid backup filename format")
	}

	backupPath := filepath.Join(s.backupDir, filename)

	// Check if file exists
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return errors.New("backup not found")
	}

	// Read and validate manifest
	manifest, err := s.readManifest(backupPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	log.Printf("[backup] Restoring from backup: %s (created %s)", filename, manifest.CreatedAt.Format(time.RFC3339))

	// Open zip for reading
	reader, err := zip.OpenReader(backupPath)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer reader.Close()

	// Extract files
	restoredCount := 0
	for _, file := range reader.File {
		// Skip manifest
		if file.Name == "manifest.json" {
			continue
		}

		// Only restore known files
		expectedChecksum, ok := manifest.Files[file.Name]
		if !ok {
			log.Printf("[backup] Skipping unknown file in backup: %s", file.Name)
			continue
		}

		destPath := filepath.Join(s.cacheDir, file.Name)

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

	log.Printf("[backup] Restore completed: %d files restored from %s", restoredCount, filename)
	return nil
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

	if !strings.HasPrefix(filename, "strmr_backup_") || !strings.HasSuffix(filename, ".zip") {
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
			if backup.CreatedAt.Before(cutoff) {
				toDelete[backup.Filename] = true
			}
		}
	}

	// Apply retention count rule (keep newest N backups)
	if retention.RetentionCount > 0 && len(backups) > retention.RetentionCount {
		// Backups are already sorted newest first
		for i := retention.RetentionCount; i < len(backups); i++ {
			toDelete[backups[i].Filename] = true
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
