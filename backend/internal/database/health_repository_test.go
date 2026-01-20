package database

import (
	"path/filepath"
	"testing"
	"time"
)

// setupTestHealthRepo creates a test database and health repository.
func setupTestHealthRepo(t *testing.T) (*DB, *HealthRepository) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := NewDB(Config{DatabasePath: dbPath})
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := NewHealthRepository(db.Connection())
	return db, repo
}

func TestUpdateFileHealth_Insert(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.UpdateFileHealth("/path/to/file.mkv", HealthStatusPending, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateFileHealth failed: %v", err)
	}

	// Verify record was created
	health, err := repo.GetFileHealth("/path/to/file.mkv")
	if err != nil {
		t.Fatalf("GetFileHealth failed: %v", err)
	}
	if health == nil {
		t.Fatal("expected health record to exist")
	}
	if health.FilePath != "/path/to/file.mkv" {
		t.Errorf("expected file path '/path/to/file.mkv', got %q", health.FilePath)
	}
	if health.Status != HealthStatusPending {
		t.Errorf("expected status 'pending', got %q", health.Status)
	}
}

func TestUpdateFileHealth_Update(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Insert initial record
	err := repo.UpdateFileHealth("/path/to/file.mkv", HealthStatusPending, nil, nil, nil)
	if err != nil {
		t.Fatalf("initial UpdateFileHealth failed: %v", err)
	}

	// Update to new status
	errorMsg := "test error"
	err = repo.UpdateFileHealth("/path/to/file.mkv", HealthStatusCorrupted, &errorMsg, nil, nil)
	if err != nil {
		t.Fatalf("UpdateFileHealth update failed: %v", err)
	}

	// Verify record was updated
	health, _ := repo.GetFileHealth("/path/to/file.mkv")
	if health.Status != HealthStatusCorrupted {
		t.Errorf("expected status 'corrupted', got %q", health.Status)
	}
	if health.LastError == nil || *health.LastError != "test error" {
		t.Errorf("expected last error 'test error', got %v", health.LastError)
	}
}

func TestUpdateFileHealth_WithAllFields(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	errorMsg := "check failed"
	sourceNzb := "/path/to/source.nzb"
	errorDetails := `{"segments": [1, 2, 3]}`

	err := repo.UpdateFileHealth("/path/to/file.mkv", HealthStatusPartial, &errorMsg, &sourceNzb, &errorDetails)
	if err != nil {
		t.Fatalf("UpdateFileHealth failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/path/to/file.mkv")
	if health.SourceNzbPath == nil || *health.SourceNzbPath != sourceNzb {
		t.Errorf("expected source nzb path %q, got %v", sourceNzb, health.SourceNzbPath)
	}
	if health.ErrorDetails == nil || *health.ErrorDetails != errorDetails {
		t.Errorf("expected error details %q, got %v", errorDetails, health.ErrorDetails)
	}
}

func TestGetFileHealth_Found(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.UpdateFileHealth("/test/file.mkv", HealthStatusHealthy, nil, nil, nil)

	health, err := repo.GetFileHealth("/test/file.mkv")
	if err != nil {
		t.Fatalf("GetFileHealth failed: %v", err)
	}
	if health == nil {
		t.Fatal("expected health record to be found")
	}
	if health.FilePath != "/test/file.mkv" {
		t.Errorf("expected file path '/test/file.mkv', got %q", health.FilePath)
	}
}

func TestGetFileHealth_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	health, err := repo.GetFileHealth("/nonexistent/file.mkv")
	if err != nil {
		t.Fatalf("GetFileHealth failed: %v", err)
	}
	if health != nil {
		t.Error("expected nil for non-existent file")
	}
}

func TestGetFileHealthByID_Found(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.UpdateFileHealth("/test/file.mkv", HealthStatusPending, nil, nil, nil)

	// Get the file to find its ID
	health, _ := repo.GetFileHealth("/test/file.mkv")

	// Get by ID
	retrieved, err := repo.GetFileHealthByID(health.ID)
	if err != nil {
		t.Fatalf("GetFileHealthByID failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected health record to be found by ID")
	}
	if retrieved.FilePath != "/test/file.mkv" {
		t.Errorf("expected file path '/test/file.mkv', got %q", retrieved.FilePath)
	}
}

func TestGetFileHealthByID_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	health, err := repo.GetFileHealthByID(99999)
	if err != nil {
		t.Fatalf("GetFileHealthByID failed: %v", err)
	}
	if health != nil {
		t.Error("expected nil for non-existent ID")
	}
}

func TestGetUnhealthyFiles_Limit(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create multiple pending files
	for i := 0; i < 5; i++ {
		repo.AddFileToHealthCheck("/file"+string(rune('A'+i))+".mkv", 5, nil)
	}

	files, err := repo.GetUnhealthyFiles(3)
	if err != nil {
		t.Fatalf("GetUnhealthyFiles failed: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d", len(files))
	}
}

func TestGetUnhealthyFiles_SkipsRepairTriggered(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create a pending file
	repo.AddFileToHealthCheck("/pending.mkv", 5, nil)

	// Create a file and set it to repair_triggered
	repo.AddFileToHealthCheck("/repair.mkv", 5, nil)
	repo.SetRepairTriggered("/repair.mkv", nil)

	files, err := repo.GetUnhealthyFiles(10)
	if err != nil {
		t.Fatalf("GetUnhealthyFiles failed: %v", err)
	}

	// Should only return the pending file
	if len(files) != 1 {
		t.Errorf("expected 1 file (repair_triggered should be skipped), got %d", len(files))
	}
	if files[0].FilePath != "/pending.mkv" {
		t.Errorf("expected '/pending.mkv', got %q", files[0].FilePath)
	}
}

func TestGetUnhealthyFiles_RespectsMaxRetries(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create file with 0 max retries (should not be picked up)
	repo.AddFileToHealthCheck("/maxed.mkv", 0, nil)

	// Create file with retries remaining
	repo.AddFileToHealthCheck("/pending.mkv", 5, nil)

	files, err := repo.GetUnhealthyFiles(10)
	if err != nil {
		t.Fatalf("GetUnhealthyFiles failed: %v", err)
	}

	// Should only return file with retries remaining
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

func TestGetFilesForRepairNotification_ReturnsRepairTriggered(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create a pending file
	repo.AddFileToHealthCheck("/pending.mkv", 5, nil)

	// Create a repair_triggered file
	repo.AddFileToHealthCheck("/repair.mkv", 5, nil)
	repo.SetRepairTriggered("/repair.mkv", nil)

	files, err := repo.GetFilesForRepairNotification(10)
	if err != nil {
		t.Fatalf("GetFilesForRepairNotification failed: %v", err)
	}

	// Should only return repair_triggered files
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
	if files[0].FilePath != "/repair.mkv" {
		t.Errorf("expected '/repair.mkv', got %q", files[0].FilePath)
	}
}

func TestIncrementRetryCount_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)

	errorMsg := "connection failed"
	err := repo.IncrementRetryCount("/test.mkv", &errorMsg)
	if err != nil {
		t.Fatalf("IncrementRetryCount failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/test.mkv")
	if health.RetryCount != 1 {
		t.Errorf("expected retry count 1, got %d", health.RetryCount)
	}
	if health.LastError == nil || *health.LastError != "connection failed" {
		t.Errorf("expected last error 'connection failed', got %v", health.LastError)
	}
	if health.NextRetryAt == nil {
		t.Error("expected next_retry_at to be set")
	}
}

func TestIncrementRetryCount_ExponentialBackoff(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 10, nil)

	// Increment multiple times
	for i := 0; i < 3; i++ {
		err := repo.IncrementRetryCount("/test.mkv", nil)
		if err != nil {
			t.Fatalf("IncrementRetryCount iteration %d failed: %v", i, err)
		}
	}

	health, _ := repo.GetFileHealth("/test.mkv")
	if health.RetryCount != 3 {
		t.Errorf("expected retry count 3, got %d", health.RetryCount)
	}
}

func TestSetRepairTriggered_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)

	errorMsg := "repair needed"
	err := repo.SetRepairTriggered("/test.mkv", &errorMsg)
	if err != nil {
		t.Fatalf("SetRepairTriggered failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/test.mkv")
	if health.Status != HealthStatusRepairTriggered {
		t.Errorf("expected status 'repair_triggered', got %q", health.Status)
	}
	if health.LastError == nil || *health.LastError != "repair needed" {
		t.Errorf("expected last error 'repair needed', got %v", health.LastError)
	}
}

func TestSetRepairTriggered_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.SetRepairTriggered("/nonexistent.mkv", nil)
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestSetCorrupted_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)

	errorMsg := "permanently corrupted"
	err := repo.SetCorrupted("/test.mkv", &errorMsg)
	if err != nil {
		t.Fatalf("SetCorrupted failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/test.mkv")
	if health.Status != HealthStatusCorrupted {
		t.Errorf("expected status 'corrupted', got %q", health.Status)
	}
}

func TestSetCorrupted_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.SetCorrupted("/nonexistent.mkv", nil)
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestIncrementRepairRetryCount_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)
	repo.SetRepairTriggered("/test.mkv", nil)

	err := repo.IncrementRepairRetryCount("/test.mkv", nil)
	if err != nil {
		t.Fatalf("IncrementRepairRetryCount failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/test.mkv")
	if health.RepairRetryCount != 1 {
		t.Errorf("expected repair retry count 1, got %d", health.RepairRetryCount)
	}
}

func TestMarkAsCorrupted_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)

	finalError := "all retries exhausted"
	err := repo.MarkAsCorrupted("/test.mkv", &finalError)
	if err != nil {
		t.Fatalf("MarkAsCorrupted failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/test.mkv")
	if health.Status != HealthStatusCorrupted {
		t.Errorf("expected status 'corrupted', got %q", health.Status)
	}
}

func TestMarkAsCorrupted_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.MarkAsCorrupted("/nonexistent.mkv", nil)
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestGetHealthStats_EmptyDatabase(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	stats, err := repo.GetHealthStats()
	if err != nil {
		t.Fatalf("GetHealthStats failed: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("expected empty stats, got %v", stats)
	}
}

func TestGetHealthStats_ReturnsCounts(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create files with different statuses
	repo.AddFileToHealthCheck("/pending1.mkv", 5, nil)
	repo.AddFileToHealthCheck("/pending2.mkv", 5, nil)

	repo.AddFileToHealthCheck("/healthy.mkv", 5, nil)
	repo.UpdateFileHealth("/healthy.mkv", HealthStatusHealthy, nil, nil, nil)

	repo.AddFileToHealthCheck("/corrupted.mkv", 5, nil)
	repo.SetCorrupted("/corrupted.mkv", nil)

	stats, err := repo.GetHealthStats()
	if err != nil {
		t.Fatalf("GetHealthStats failed: %v", err)
	}

	if stats[HealthStatusPending] != 2 {
		t.Errorf("expected 2 pending, got %d", stats[HealthStatusPending])
	}
	if stats[HealthStatusHealthy] != 1 {
		t.Errorf("expected 1 healthy, got %d", stats[HealthStatusHealthy])
	}
	if stats[HealthStatusCorrupted] != 1 {
		t.Errorf("expected 1 corrupted, got %d", stats[HealthStatusCorrupted])
	}
}

func TestListHealthItems_Pagination(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create multiple files
	for i := 0; i < 10; i++ {
		repo.AddFileToHealthCheck("/file"+string(rune('A'+i))+".mkv", 5, nil)
	}

	// Get first page
	files, err := repo.ListHealthItems(nil, 3, 0, nil, "", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems page 1 failed: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 files on page 1, got %d", len(files))
	}

	// Get second page
	files, err = repo.ListHealthItems(nil, 3, 3, nil, "", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems page 2 failed: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 files on page 2, got %d", len(files))
	}

	// Get last page
	files, err = repo.ListHealthItems(nil, 3, 9, nil, "", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems last page failed: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file on last page, got %d", len(files))
	}
}

func TestListHealthItems_FilterByStatus(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create files with different statuses
	repo.AddFileToHealthCheck("/pending.mkv", 5, nil)
	repo.AddFileToHealthCheck("/healthy.mkv", 5, nil)
	repo.UpdateFileHealth("/healthy.mkv", HealthStatusHealthy, nil, nil, nil)

	// Filter by pending
	status := HealthStatusPending
	files, err := repo.ListHealthItems(&status, 10, 0, nil, "", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems failed: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 pending file, got %d", len(files))
	}

	// Filter by healthy
	status = HealthStatusHealthy
	files, err = repo.ListHealthItems(&status, 10, 0, nil, "", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems failed: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 healthy file, got %d", len(files))
	}
}

func TestListHealthItems_Sorting(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create files with predictable names
	repo.AddFileToHealthCheck("/aaa.mkv", 5, nil)
	repo.AddFileToHealthCheck("/zzz.mkv", 5, nil)

	// Sort by file_path ascending
	files, err := repo.ListHealthItems(nil, 10, 0, nil, "", "file_path", "asc")
	if err != nil {
		t.Fatalf("ListHealthItems failed: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("expected at least 2 files, got %d", len(files))
	}
	if files[0].FilePath != "/aaa.mkv" {
		t.Errorf("expected first file '/aaa.mkv', got %q", files[0].FilePath)
	}

	// Sort by file_path descending
	files, err = repo.ListHealthItems(nil, 10, 0, nil, "", "file_path", "desc")
	if err != nil {
		t.Fatalf("ListHealthItems failed: %v", err)
	}
	if files[0].FilePath != "/zzz.mkv" {
		t.Errorf("expected first file '/zzz.mkv', got %q", files[0].FilePath)
	}
}

func TestListHealthItems_Search(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create files
	repo.AddFileToHealthCheck("/movies/action/terminator.mkv", 5, nil)
	repo.AddFileToHealthCheck("/movies/comedy/funny.mkv", 5, nil)

	// Search for terminator
	files, err := repo.ListHealthItems(nil, 10, 0, nil, "terminator", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems search failed: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file matching 'terminator', got %d", len(files))
	}
}

func TestListHealthItems_SinceFilter(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create a file
	repo.AddFileToHealthCheck("/recent.mkv", 5, nil)

	// Filter for files created since yesterday (should return the file)
	yesterday := time.Now().Add(-24 * time.Hour)
	files, err := repo.ListHealthItems(nil, 10, 0, &yesterday, "", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems failed: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file since yesterday, got %d", len(files))
	}

	// Filter for files created since tomorrow (should return none)
	tomorrow := time.Now().Add(24 * time.Hour)
	files, err = repo.ListHealthItems(nil, 10, 0, &tomorrow, "", "", "")
	if err != nil {
		t.Fatalf("ListHealthItems failed: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files since tomorrow, got %d", len(files))
	}
}

func TestCountHealthItems_ReturnsCorrectCount(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Initially empty
	count, err := repo.CountHealthItems(nil, nil, "")
	if err != nil {
		t.Fatalf("CountHealthItems failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Add files
	repo.AddFileToHealthCheck("/file1.mkv", 5, nil)
	repo.AddFileToHealthCheck("/file2.mkv", 5, nil)
	repo.AddFileToHealthCheck("/file3.mkv", 5, nil)

	count, err = repo.CountHealthItems(nil, nil, "")
	if err != nil {
		t.Fatalf("CountHealthItems failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestCountHealthItems_WithStatusFilter(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create files with different statuses
	repo.AddFileToHealthCheck("/pending.mkv", 5, nil)
	repo.AddFileToHealthCheck("/healthy.mkv", 5, nil)
	repo.UpdateFileHealth("/healthy.mkv", HealthStatusHealthy, nil, nil, nil)

	status := HealthStatusPending
	count, err := repo.CountHealthItems(&status, nil, "")
	if err != nil {
		t.Fatalf("CountHealthItems failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 pending, got %d", count)
	}
}

func TestDeleteHealthRecord_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/delete.mkv", 5, nil)

	err := repo.DeleteHealthRecord("/delete.mkv")
	if err != nil {
		t.Fatalf("DeleteHealthRecord failed: %v", err)
	}

	// Verify deleted
	health, _ := repo.GetFileHealth("/delete.mkv")
	if health != nil {
		t.Error("expected file to be deleted")
	}
}

func TestDeleteHealthRecord_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.DeleteHealthRecord("/nonexistent.mkv")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestDeleteHealthRecordByID_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/delete.mkv", 5, nil)
	health, _ := repo.GetFileHealth("/delete.mkv")

	err := repo.DeleteHealthRecordByID(health.ID)
	if err != nil {
		t.Fatalf("DeleteHealthRecordByID failed: %v", err)
	}

	// Verify deleted
	retrieved, _ := repo.GetFileHealth("/delete.mkv")
	if retrieved != nil {
		t.Error("expected file to be deleted")
	}
}

func TestDeleteHealthRecordByID_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.DeleteHealthRecordByID(99999)
	if err == nil {
		t.Error("expected error for non-existent ID")
	}
}

func TestAddFileToHealthCheck_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	sourceNzb := "/path/to/source.nzb"
	err := repo.AddFileToHealthCheck("/new/file.mkv", 5, &sourceNzb)
	if err != nil {
		t.Fatalf("AddFileToHealthCheck failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/new/file.mkv")
	if health == nil {
		t.Fatal("expected health record to exist")
	}
	if health.Status != HealthStatusPending {
		t.Errorf("expected status 'pending', got %q", health.Status)
	}
	if health.MaxRetries != 5 {
		t.Errorf("expected max retries 5, got %d", health.MaxRetries)
	}
	if health.SourceNzbPath == nil || *health.SourceNzbPath != sourceNzb {
		t.Errorf("expected source nzb path %q, got %v", sourceNzb, health.SourceNzbPath)
	}
}

func TestAddFileToHealthCheck_UpdatesExisting(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Add file initially
	repo.AddFileToHealthCheck("/file.mkv", 3, nil)

	// Add again with different max retries
	repo.AddFileToHealthCheck("/file.mkv", 10, nil)

	health, _ := repo.GetFileHealth("/file.mkv")
	if health.MaxRetries != 10 {
		t.Errorf("expected max retries to be updated to 10, got %d", health.MaxRetries)
	}
}

func TestCleanupHealthRecords_RemovesNonExistent(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Add multiple files
	repo.AddFileToHealthCheck("/keep1.mkv", 5, nil)
	repo.AddFileToHealthCheck("/keep2.mkv", 5, nil)
	repo.AddFileToHealthCheck("/remove1.mkv", 5, nil)
	repo.AddFileToHealthCheck("/remove2.mkv", 5, nil)

	// Cleanup - only keep files that exist
	existingFiles := []string{"/keep1.mkv", "/keep2.mkv"}
	err := repo.CleanupHealthRecords(existingFiles)
	if err != nil {
		t.Fatalf("CleanupHealthRecords failed: %v", err)
	}

	// Verify kept files still exist
	health, _ := repo.GetFileHealth("/keep1.mkv")
	if health == nil {
		t.Error("expected /keep1.mkv to be kept")
	}

	// Verify removed files are gone
	health, _ = repo.GetFileHealth("/remove1.mkv")
	if health != nil {
		t.Error("expected /remove1.mkv to be removed")
	}
}

func TestCleanupHealthRecords_EmptyList(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/file.mkv", 5, nil)

	// Cleanup with empty list should remove all
	err := repo.CleanupHealthRecords([]string{})
	if err != nil {
		t.Fatalf("CleanupHealthRecords failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/file.mkv")
	if health != nil {
		t.Error("expected all files to be removed")
	}
}

func TestSetFileChecking_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)

	err := repo.SetFileChecking("/test.mkv")
	if err != nil {
		t.Fatalf("SetFileChecking failed: %v", err)
	}

	health, _ := repo.GetFileHealth("/test.mkv")
	if health.Status != HealthStatusChecking {
		t.Errorf("expected status 'checking', got %q", health.Status)
	}
}

func TestSetFileCheckingByID_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)
	health, _ := repo.GetFileHealth("/test.mkv")

	err := repo.SetFileCheckingByID(health.ID)
	if err != nil {
		t.Fatalf("SetFileCheckingByID failed: %v", err)
	}

	updated, _ := repo.GetFileHealthByID(health.ID)
	if updated.Status != HealthStatusChecking {
		t.Errorf("expected status 'checking', got %q", updated.Status)
	}
}

func TestSetRepairTriggeredByID_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	repo.AddFileToHealthCheck("/test.mkv", 5, nil)
	health, _ := repo.GetFileHealth("/test.mkv")

	errorMsg := "repair needed"
	err := repo.SetRepairTriggeredByID(health.ID, &errorMsg)
	if err != nil {
		t.Fatalf("SetRepairTriggeredByID failed: %v", err)
	}

	updated, _ := repo.GetFileHealthByID(health.ID)
	if updated.Status != HealthStatusRepairTriggered {
		t.Errorf("expected status 'repair_triggered', got %q", updated.Status)
	}
}

func TestSetRepairTriggeredByID_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.SetRepairTriggeredByID(99999, nil)
	if err == nil {
		t.Error("expected error for non-existent ID")
	}
}

func TestResetFileAllChecking_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Create files and set them to checking
	repo.AddFileToHealthCheck("/file1.mkv", 5, nil)
	repo.AddFileToHealthCheck("/file2.mkv", 5, nil)
	repo.SetFileChecking("/file1.mkv")
	repo.SetFileChecking("/file2.mkv")

	err := repo.ResetFileAllChecking()
	if err != nil {
		t.Fatalf("ResetFileAllChecking failed: %v", err)
	}

	// Verify all are back to pending
	health1, _ := repo.GetFileHealth("/file1.mkv")
	health2, _ := repo.GetFileHealth("/file2.mkv")

	if health1.Status != HealthStatusPending {
		t.Errorf("expected file1 status 'pending', got %q", health1.Status)
	}
	if health2.Status != HealthStatusPending {
		t.Errorf("expected file2 status 'pending', got %q", health2.Status)
	}
}

func TestDeleteHealthRecordsBulk_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Add multiple files
	repo.AddFileToHealthCheck("/file1.mkv", 5, nil)
	repo.AddFileToHealthCheck("/file2.mkv", 5, nil)
	repo.AddFileToHealthCheck("/file3.mkv", 5, nil)

	// Delete bulk
	err := repo.DeleteHealthRecordsBulk([]string{"/file1.mkv", "/file2.mkv"})
	if err != nil {
		t.Fatalf("DeleteHealthRecordsBulk failed: %v", err)
	}

	// Verify deleted
	health, _ := repo.GetFileHealth("/file1.mkv")
	if health != nil {
		t.Error("expected /file1.mkv to be deleted")
	}
	health, _ = repo.GetFileHealth("/file2.mkv")
	if health != nil {
		t.Error("expected /file2.mkv to be deleted")
	}

	// Verify not deleted
	health, _ = repo.GetFileHealth("/file3.mkv")
	if health == nil {
		t.Error("expected /file3.mkv to still exist")
	}
}

func TestDeleteHealthRecordsBulk_EmptyList(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.DeleteHealthRecordsBulk([]string{})
	if err != nil {
		t.Fatalf("DeleteHealthRecordsBulk with empty list should not error: %v", err)
	}
}

func TestDeleteHealthRecordsBulk_NotFound(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	err := repo.DeleteHealthRecordsBulk([]string{"/nonexistent.mkv"})
	if err == nil {
		t.Error("expected error for non-existent files")
	}
}

func TestResetHealthChecksBulk_Success(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	// Add files and modify them
	repo.AddFileToHealthCheck("/file1.mkv", 5, nil)
	repo.AddFileToHealthCheck("/file2.mkv", 5, nil)
	repo.SetCorrupted("/file1.mkv", nil)
	repo.IncrementRetryCount("/file2.mkv", nil)

	// Reset bulk
	count, err := repo.ResetHealthChecksBulk([]string{"/file1.mkv", "/file2.mkv"})
	if err != nil {
		t.Fatalf("ResetHealthChecksBulk failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 records reset, got %d", count)
	}

	// Verify reset
	health1, _ := repo.GetFileHealth("/file1.mkv")
	health2, _ := repo.GetFileHealth("/file2.mkv")

	if health1.Status != HealthStatusPending {
		t.Errorf("expected file1 status 'pending', got %q", health1.Status)
	}
	if health1.RetryCount != 0 {
		t.Errorf("expected file1 retry count 0, got %d", health1.RetryCount)
	}
	if health2.Status != HealthStatusPending {
		t.Errorf("expected file2 status 'pending', got %q", health2.Status)
	}
	if health2.RetryCount != 0 {
		t.Errorf("expected file2 retry count 0, got %d", health2.RetryCount)
	}
}

func TestResetHealthChecksBulk_EmptyList(t *testing.T) {
	_, repo := setupTestHealthRepo(t)

	count, err := repo.ResetHealthChecksBulk([]string{})
	if err != nil {
		t.Fatalf("ResetHealthChecksBulk with empty list should not error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}
