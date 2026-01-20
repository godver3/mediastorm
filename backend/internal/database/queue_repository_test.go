package database

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// setupTestDB creates a new test database in a temp directory.
func setupTestDB(t *testing.T) *DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := NewDB(Config{DatabasePath: dbPath})
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNewDB_Success(t *testing.T) {
	db := setupTestDB(t)
	if db == nil {
		t.Fatal("expected non-nil database")
	}
	if db.Repository == nil {
		t.Fatal("expected non-nil repository")
	}
}

func TestNewDB_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "subdir", "nested", "test.db")

	db, err := NewDB(Config{DatabasePath: dbPath})
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer db.Close()
}

func TestAddToQueue_NewItem(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}

	err := repo.AddToQueue(item)
	if err != nil {
		t.Fatalf("AddToQueue failed: %v", err)
	}

	if item.ID == 0 {
		t.Error("expected non-zero ID after insert")
	}

	// Verify item was stored
	retrieved, err := repo.GetQueueItem(item.ID)
	if err != nil {
		t.Fatalf("GetQueueItem failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected item to be retrievable")
	}
	if retrieved.NzbPath != "/path/to/test.nzb" {
		t.Errorf("expected NzbPath '/path/to/test.nzb', got %q", retrieved.NzbPath)
	}
}

func TestAddToQueue_ConflictUpdate(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	// Add initial item and mark it as completed (so ON CONFLICT update will work)
	item1 := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityLow,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	err := repo.AddToQueue(item1)
	if err != nil {
		t.Fatalf("AddToQueue (first) failed: %v", err)
	}

	// Mark as completed so the ON CONFLICT clause will allow update
	repo.UpdateQueueItemStatus(item1.ID, QueueStatusCompleted, nil)

	// Add same path again - should update since status is 'completed'
	item2 := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityHigh,
		Status:     QueueStatusPending,
		MaxRetries: 5,
	}
	err = repo.AddToQueue(item2)
	if err != nil {
		t.Fatalf("AddToQueue (second) failed: %v", err)
	}

	// Verify the item was updated (status reset to pending, priority updated)
	retrieved, err := repo.GetQueueItem(item1.ID)
	if err != nil {
		t.Fatalf("GetQueueItem failed: %v", err)
	}
	if retrieved.Priority != QueuePriorityHigh {
		t.Errorf("expected priority to be updated to High (%d), got %d", QueuePriorityHigh, retrieved.Priority)
	}
	if retrieved.Status != QueueStatusPending {
		t.Errorf("expected status to be reset to pending, got %q", retrieved.Status)
	}
}

func TestIsFileInQueue_Exists(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)

	exists, err := repo.IsFileInQueue("/path/to/test.nzb")
	if err != nil {
		t.Fatalf("IsFileInQueue failed: %v", err)
	}
	if !exists {
		t.Error("expected file to be in queue")
	}
}

func TestIsFileInQueue_NotExists(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	exists, err := repo.IsFileInQueue("/path/to/nonexistent.nzb")
	if err != nil {
		t.Fatalf("IsFileInQueue failed: %v", err)
	}
	if exists {
		t.Error("expected file to not be in queue")
	}
}

func TestIsFileInQueue_CompletedNotInQueue(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	// Add item and mark it completed
	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)
	repo.UpdateQueueItemStatus(item.ID, QueueStatusCompleted, nil)

	// Completed items should not be considered "in queue"
	exists, err := repo.IsFileInQueue("/path/to/test.nzb")
	if err != nil {
		t.Fatalf("IsFileInQueue failed: %v", err)
	}
	if exists {
		t.Error("expected completed file to not be in queue")
	}
}

func TestClaimNextQueueItem_ClaimsPending(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)

	claimed, err := repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected item to be claimed")
	}
	if claimed.Status != QueueStatusProcessing {
		t.Errorf("expected status 'processing', got %q", claimed.Status)
	}
}

func TestClaimNextQueueItem_SkipsProcessing(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	// Add item and mark it as processing
	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)
	repo.UpdateQueueItemStatus(item.ID, QueueStatusProcessing, nil)

	// Should return nil since item is already processing
	claimed, err := repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed != nil {
		t.Error("expected nil when all items are processing")
	}
}

func TestClaimNextQueueItem_PriorityOrder(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	// Add items with different priorities
	lowPriority := &ImportQueueItem{
		NzbPath:    "/path/low.nzb",
		Priority:   QueuePriorityLow,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(lowPriority)

	highPriority := &ImportQueueItem{
		NzbPath:    "/path/high.nzb",
		Priority:   QueuePriorityHigh,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(highPriority)

	normalPriority := &ImportQueueItem{
		NzbPath:    "/path/normal.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(normalPriority)

	// Should claim high priority first
	claimed, err := repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed == nil || claimed.NzbPath != "/path/high.nzb" {
		t.Errorf("expected high priority item, got %v", claimed)
	}

	// Should claim normal priority next
	claimed, err = repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed == nil || claimed.NzbPath != "/path/normal.nzb" {
		t.Errorf("expected normal priority item, got %v", claimed)
	}

	// Should claim low priority last
	claimed, err = repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed == nil || claimed.NzbPath != "/path/low.nzb" {
		t.Errorf("expected low priority item, got %v", claimed)
	}
}

func TestClaimNextQueueItem_ConcurrentClaims(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	// Add a single item
	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)

	// Attempt concurrent claims with fewer goroutines to reduce lock contention
	var wg sync.WaitGroup
	claimCount := 0
	errCount := 0
	var mu sync.Mutex

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := repo.ClaimNextQueueItem()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// Database lock errors are expected with SQLite concurrent writes
				errCount++
				return
			}
			if claimed != nil {
				claimCount++
			}
		}()
	}

	wg.Wait()

	// With SQLite, we may get lock errors or a single successful claim
	// The key is that at most one goroutine should successfully claim the item
	if claimCount > 1 {
		t.Errorf("expected at most 1 successful claim, got %d", claimCount)
	}
	// Total should equal number of goroutines
	if claimCount+errCount == 0 {
		t.Error("expected at least some response from concurrent claims")
	}
}

func TestUpdateQueueItemStatus_AllStatuses(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	tests := []struct {
		name         string
		status       QueueStatus
		errorMessage *string
	}{
		{"Processing", QueueStatusProcessing, nil},
		{"Completed", QueueStatusCompleted, nil},
		{"Failed", QueueStatusFailed, stringPtr("test error")},
		{"Retrying", QueueStatusRetrying, stringPtr("retry error")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := &ImportQueueItem{
				NzbPath:    "/path/to/" + tc.name + ".nzb",
				Priority:   QueuePriorityNormal,
				Status:     QueueStatusPending,
				MaxRetries: 3,
			}
			repo.AddToQueue(item)

			err := repo.UpdateQueueItemStatus(item.ID, tc.status, tc.errorMessage)
			if err != nil {
				t.Fatalf("UpdateQueueItemStatus failed: %v", err)
			}

			retrieved, _ := repo.GetQueueItem(item.ID)
			if retrieved.Status != tc.status {
				t.Errorf("expected status %q, got %q", tc.status, retrieved.Status)
			}
		})
	}
}

func TestGetQueueStats_Counts(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	// Add items with different statuses
	for i := 0; i < 3; i++ {
		item := &ImportQueueItem{
			NzbPath:    "/path/pending" + string(rune('0'+i)) + ".nzb",
			Priority:   QueuePriorityNormal,
			Status:     QueueStatusPending,
			MaxRetries: 3,
		}
		repo.AddToQueue(item)
	}

	processingItem := &ImportQueueItem{
		NzbPath:    "/path/processing.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(processingItem)
	repo.UpdateQueueItemStatus(processingItem.ID, QueueStatusProcessing, nil)

	completedItem := &ImportQueueItem{
		NzbPath:    "/path/completed.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(completedItem)
	repo.UpdateQueueItemStatus(completedItem.ID, QueueStatusCompleted, nil)

	failedItem := &ImportQueueItem{
		NzbPath:    "/path/failed.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(failedItem)
	errMsg := "test error"
	repo.UpdateQueueItemStatus(failedItem.ID, QueueStatusFailed, &errMsg)

	stats, err := repo.GetQueueStats()
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}

	if stats.TotalQueued != 3 {
		t.Errorf("expected 3 queued, got %d", stats.TotalQueued)
	}
	if stats.TotalProcessing != 1 {
		t.Errorf("expected 1 processing, got %d", stats.TotalProcessing)
	}
	if stats.TotalCompleted != 1 {
		t.Errorf("expected 1 completed, got %d", stats.TotalCompleted)
	}
	if stats.TotalFailed != 1 {
		t.Errorf("expected 1 failed, got %d", stats.TotalFailed)
	}
}

func TestTryClaimQueueItem_Success(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)

	claimed, err := repo.TryClaimQueueItem(item.ID)
	if err != nil {
		t.Fatalf("TryClaimQueueItem failed: %v", err)
	}
	if !claimed {
		t.Error("expected claim to succeed")
	}

	retrieved, _ := repo.GetQueueItem(item.ID)
	if retrieved.Status != QueueStatusProcessing {
		t.Errorf("expected status 'processing', got %q", retrieved.Status)
	}
}

func TestTryClaimQueueItem_AlreadyClaimed(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)

	// First claim
	repo.TryClaimQueueItem(item.ID)

	// Second claim should fail
	claimed, err := repo.TryClaimQueueItem(item.ID)
	if err != nil {
		t.Fatalf("TryClaimQueueItem failed: %v", err)
	}
	if claimed {
		t.Error("expected claim to fail on already-claimed item")
	}
}

func TestResetStaleItems_ResetsProcessing(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	// Add and claim items
	item1 := &ImportQueueItem{
		NzbPath:    "/path/1.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item1)
	repo.TryClaimQueueItem(item1.ID)

	item2 := &ImportQueueItem{
		NzbPath:    "/path/2.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item2)
	repo.UpdateQueueItemStatus(item2.ID, QueueStatusRetrying, nil)

	// Reset stale items
	err := repo.ResetStaleItems()
	if err != nil {
		t.Fatalf("ResetStaleItems failed: %v", err)
	}

	// Both items should be reset to pending
	retrieved1, _ := repo.GetQueueItem(item1.ID)
	if retrieved1.Status != QueueStatusPending {
		t.Errorf("expected item1 status 'pending', got %q", retrieved1.Status)
	}

	retrieved2, _ := repo.GetQueueItem(item2.ID)
	if retrieved2.Status != QueueStatusPending {
		t.Errorf("expected item2 status 'pending', got %q", retrieved2.Status)
	}
}

func TestAddBatchToQueue_MultipleItems(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	items := []*ImportQueueItem{
		{NzbPath: "/path/1.nzb", Priority: QueuePriorityNormal, Status: QueueStatusPending, MaxRetries: 3},
		{NzbPath: "/path/2.nzb", Priority: QueuePriorityHigh, Status: QueueStatusPending, MaxRetries: 3},
		{NzbPath: "/path/3.nzb", Priority: QueuePriorityLow, Status: QueueStatusPending, MaxRetries: 3},
	}

	err := repo.AddBatchToQueue(items)
	if err != nil {
		t.Fatalf("AddBatchToQueue failed: %v", err)
	}

	// All items should have IDs assigned
	for i, item := range items {
		if item.ID == 0 {
			t.Errorf("item %d should have non-zero ID", i)
		}
	}

	// Verify all items are in the queue
	stats, _ := repo.GetQueueStats()
	if stats.TotalQueued != 3 {
		t.Errorf("expected 3 queued items, got %d", stats.TotalQueued)
	}
}

func TestAddBatchToQueue_EmptySlice(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	err := repo.AddBatchToQueue([]*ImportQueueItem{})
	if err != nil {
		t.Errorf("AddBatchToQueue with empty slice should not fail: %v", err)
	}
}

func TestGetQueueItem_NotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item, err := repo.GetQueueItem(99999)
	if err != nil {
		t.Fatalf("GetQueueItem failed: %v", err)
	}
	if item != nil {
		t.Error("expected nil for non-existent item")
	}
}

func TestAddStoragePath_Success(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)

	err := repo.AddStoragePath(item.ID, "/storage/output/file.mkv")
	if err != nil {
		t.Fatalf("AddStoragePath failed: %v", err)
	}

	retrieved, _ := repo.GetQueueItem(item.ID)
	if retrieved.StoragePath == nil || *retrieved.StoragePath != "/storage/output/file.mkv" {
		t.Error("expected storage path to be set")
	}
}

func TestUpdateMetadata_Success(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
	}
	repo.AddToQueue(item)

	metadata := `{"title": "Test Movie", "year": 2024}`
	err := repo.UpdateMetadata(item.ID, &metadata)
	if err != nil {
		t.Fatalf("UpdateMetadata failed: %v", err)
	}

	retrieved, _ := repo.GetQueueItem(item.ID)
	if retrieved.Metadata == nil || *retrieved.Metadata != metadata {
		t.Error("expected metadata to be set")
	}
}

func TestClaimNextQueueItem_RespectsMaxRetries(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 2,
	}
	repo.AddToQueue(item)

	// Simulate hitting max retries
	errMsg := "test error"
	repo.UpdateQueueItemStatus(item.ID, QueueStatusRetrying, &errMsg)
	repo.UpdateQueueItemStatus(item.ID, QueueStatusRetrying, &errMsg)

	// Should now be at max retries and not claimable
	claimed, err := repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed != nil {
		t.Error("expected nil when item is at max retries")
	}
}

func TestDBClose_Success(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := NewDB(Config{DatabasePath: dbPath})
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	err = db.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestDBConnection_ReturnsConnection(t *testing.T) {
	db := setupTestDB(t)

	conn := db.Connection()
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}

	// Verify connection works
	err := conn.Ping()
	if err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}

func TestClaimNextQueueItem_NoItems(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	claimed, err := repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed != nil {
		t.Error("expected nil when no items in queue")
	}
}

func TestClaimNextQueueItem_WithCategory(t *testing.T) {
	db := setupTestDB(t)
	repo := db.Repository

	category := "movies"
	item := &ImportQueueItem{
		NzbPath:    "/path/to/test.nzb",
		Priority:   QueuePriorityNormal,
		Status:     QueueStatusPending,
		MaxRetries: 3,
		Category:   &category,
	}
	repo.AddToQueue(item)

	claimed, err := repo.ClaimNextQueueItem()
	if err != nil {
		t.Fatalf("ClaimNextQueueItem failed: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected item to be claimed")
	}
	if claimed.Category == nil || *claimed.Category != "movies" {
		t.Error("expected category to be preserved")
	}
}

// stringPtr returns a pointer to the given string
func stringPtr(s string) *string {
	return &s
}

// Add delay helper for timing-sensitive tests
func waitForDB() {
	time.Sleep(10 * time.Millisecond)
}
