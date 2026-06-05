package playback_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
	"novastream/internal/database"
	"novastream/internal/integration"
	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/pool"
	"novastream/services/playback"
)

type stubMetadataService struct {
	files     map[string][]string
	subdirs   map[string][]string
	fileSizes map[string]int64
}

func newStubMetadataService() *stubMetadataService {
	return &stubMetadataService{
		files:     make(map[string][]string),
		subdirs:   make(map[string][]string),
		fileSizes: make(map[string]int64),
	}
}

func (s *stubMetadataService) ListDirectory(virtualPath string) ([]string, error) {
	return s.files[virtualPath], nil
}

func (s *stubMetadataService) ListSubdirectories(virtualPath string) ([]string, error) {
	return s.subdirs[virtualPath], nil
}

func (s *stubMetadataService) GetFileMetadata(virtualPath string) (*metapb.FileMetadata, error) {
	size, ok := s.fileSizes[virtualPath]
	if !ok {
		return nil, nil
	}
	return &metapb.FileMetadata{FileSize: size}, nil
}

func setupPlaybackService(t *testing.T) (*playback.Service, *integration.NzbSystem, *stubMetadataService) {
	t.Helper()

	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, "settings.json")
	cfg := config.NewManager(settingsPath)
	if err := cfg.Save(config.DefaultSettings()); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	adapter := config.NewConfigAdapter(cfg)
	poolManager := pool.NewManager()
	nzbCfg := integration.NzbConfig{
		QueueDatabasePath:   filepath.Join(tempDir, "queue.db"),
		MetadataRootPath:    filepath.Join(tempDir, "metadata"),
		Password:            "",
		Salt:                "",
		MaxProcessorWorkers: 1,
		MaxDownloadWorkers:  1,
	}

	nzbSystem, err := integration.NewNzbSystem(nzbCfg, poolManager, adapter.GetConfigGetter())
	if err != nil {
		t.Fatalf("new nzb system: %v", err)
	}
	t.Cleanup(func() {
		_ = nzbSystem.Close()
	})

	if err := nzbSystem.StopService(context.Background()); err != nil {
		t.Fatalf("stop nzb service: %v", err)
	}

	metadataSvc := newStubMetadataService()
	service := playback.NewService(cfg, nil, nzbSystem, metadataSvc)

	return service, nzbSystem, metadataSvc
}

func addCompletedPlaybackItem(t *testing.T, nzbSystem *integration.NzbSystem, nzbPath, sourceNzbPath, storagePath string, fileSize int64) int64 {
	t.Helper()

	importerSvc := nzbSystem.ImporterService()
	item := &database.ImportQueueItem{
		NzbPath:    nzbPath,
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := fmt.Sprintf(`{"sourceNzbPath":%q,"preflightHealth":"healthy"}`, sourceNzbPath)
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, storagePath); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	return item.ID
}

func TestQueueStatusQueued(t *testing.T) {
	service, nzbSystem, _ := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	fileSize := int64(1024)
	item := &database.ImportQueueItem{
		NzbPath:    "queued-item.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"queued-item.nzb"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}

	if status.QueueID != item.ID {
		t.Fatalf("expected queueID %d, got %d", item.ID, status.QueueID)
	}
	if status.HealthStatus != "queued" {
		t.Fatalf("expected healthStatus queued, got %q", status.HealthStatus)
	}
	if status.WebDAVPath != "" {
		t.Fatalf("expected empty webdav path, got %q", status.WebDAVPath)
	}
	if status.SourceNZBPath != "queued-item.nzb" {
		t.Fatalf("expected sourceNzbPath queued-item.nzb, got %q", status.SourceNZBPath)
	}
	if status.FileSize != fileSize {
		t.Fatalf("expected fileSize %d, got %d", fileSize, status.FileSize)
	}
}

func TestQueueStatusCompleted(t *testing.T) {
	service, nzbSystem, _ := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	fileSize := int64(2048)
	item := &database.ImportQueueItem{
		NzbPath:    "completed-item.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Series.S01E01.mkv","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	storagePath := "/virtual/Series.S01E01.mkv"
	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, storagePath); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}

	if status.WebDAVPath == "" {
		t.Fatalf("expected non-empty webdav path")
	}
	if status.HealthStatus != "healthy" {
		t.Fatalf("expected healthStatus healthy, got %q", status.HealthStatus)
	}
	if status.SourceNZBPath != "Series.S01E01.mkv" {
		t.Fatalf("expected sourceNzbPath Series.S01E01.mkv, got %q", status.SourceNZBPath)
	}
	if status.FileSize != fileSize {
		t.Fatalf("expected fileSize %d, got %d", fileSize, status.FileSize)
	}
}

func TestQueueStatusCompleted_RejectsDirectSampleFile(t *testing.T) {
	service, nzbSystem, _ := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	fileSize := int64(1024)
	item := &database.ImportQueueItem{
		NzbPath:    "Show.S01E01.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Show.S01E01.sample.mkv","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	storagePath := "/virtual/Show.S01E01.sample.mkv"
	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, storagePath); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	_, err := service.QueueStatus(context.Background(), item.ID)
	if err == nil {
		t.Fatal("expected direct sample file to be rejected")
	}
	if !strings.Contains(err.Error(), "sample/extras") {
		t.Fatalf("expected sample/extras error, got: %v", err)
	}
}

func TestQueueStatusCompleted_RejectsSuspiciouslySmallEpisodeSelection(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	releaseSize := int64(1800 * 1024 * 1024)
	selectedSize := int64(71 * 1024 * 1024)
	storagePath := "/virtual/Show.S02E01.1080p.Release/344f5f48b1214acfb7fd7ed0498166c4.mkv"
	metadataSvc.fileSizes[storagePath] = selectedSize

	item := &database.ImportQueueItem{
		NzbPath:    "Show.S02E01.1080p.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &releaseSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Show.S02E01.1080p.nzb","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, storagePath); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	_, err := service.QueueStatus(context.Background(), item.ID)
	if err == nil {
		t.Fatal("expected suspiciously small selected file to be rejected")
	}
	if !strings.Contains(err.Error(), "short sample") {
		t.Fatalf("expected short sample error, got: %v", err)
	}
}

func TestQueueStatusCompleted_DoesNotCompareSeasonPackTotalToSmallEpisode(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	releaseSize := int64(9000 * 1024 * 1024)
	selectedSize := int64(80 * 1024 * 1024)
	storagePath := "/virtual/Show.S02.1080p.Release/Show.S02E01.Small.Encode.mkv"
	metadataSvc.fileSizes[storagePath] = selectedSize

	item := &database.ImportQueueItem{
		NzbPath:    "Show.S02.1080p.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &releaseSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Show.S02.1080p.nzb","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, storagePath); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}
	if !strings.HasSuffix(status.WebDAVPath, "/Show.S02E01.Small.Encode.mkv") {
		t.Fatalf("expected selected file to pass, got %q", status.WebDAVPath)
	}
}

func TestQueueStatusCompleted_SuspiciousSizePolicyBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		nzbPath       string
		sourceNzbPath string
		storagePath   string
		releaseSize   int64
		selectedSize  int64
		wantErr       bool
	}{
		{
			name:          "legitimate small per episode release passes",
			nzbPath:       "Show.S02E01.Small.Encode.nzb",
			sourceNzbPath: "Show.S02E01.Small.Encode.nzb",
			storagePath:   "/virtual/Show.S02E01.Small.Encode.mkv",
			releaseSize:   90 * 1024 * 1024,
			selectedSize:  90 * 1024 * 1024,
		},
		{
			name:          "selected file at max size boundary passes",
			nzbPath:       "Show.S02E01.1080p.nzb",
			sourceNzbPath: "Show.S02E01.1080p.nzb",
			storagePath:   "/virtual/Show.S02E01.1080p/Show.S02E01.100MB.mkv",
			releaseSize:   1800 * 1024 * 1024,
			selectedSize:  100 * 1024 * 1024,
		},
		{
			name:          "selected file at ratio boundary passes",
			nzbPath:       "Show.S02E01.1080p.nzb",
			sourceNzbPath: "Show.S02E01.1080p.nzb",
			storagePath:   "/virtual/Show.S02E01.1080p/Show.S02E01.100MBish.mkv",
			releaseSize:   396 * 1024 * 1024,
			selectedSize:  99 * 1024 * 1024,
		},
		{
			name:          "advertised size below threshold passes",
			nzbPath:       "Show.S02E01.SD.nzb",
			sourceNzbPath: "Show.S02E01.SD.nzb",
			storagePath:   "/virtual/Show.S02E01.SD/Show.S02E01.70MB.mkv",
			releaseSize:   299 * 1024 * 1024,
			selectedSize:  70 * 1024 * 1024,
		},
		{
			name:          "non episode movie title does not compare advertised size",
			nzbPath:       "Movie.2024.1080p.nzb",
			sourceNzbPath: "Movie.2024.1080p.nzb",
			storagePath:   "/virtual/Movie.2024.1080p/Movie.2024.Tiny.mkv",
			releaseSize:   1500 * 1024 * 1024,
			selectedSize:  80 * 1024 * 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, nzbSystem, metadataSvc := setupPlaybackService(t)
			metadataSvc.fileSizes[tt.storagePath] = tt.selectedSize

			queueID := addCompletedPlaybackItem(t, nzbSystem, tt.nzbPath, tt.sourceNzbPath, tt.storagePath, tt.releaseSize)

			status, err := service.QueueStatus(context.Background(), queueID)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected suspicious size error, got nil")
				}
				if !strings.Contains(err.Error(), "short sample") {
					t.Fatalf("expected short sample error, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("QueueStatus returned error: %v", err)
			}
			if status.WebDAVPath == "" {
				t.Fatal("expected webdav path")
			}
		})
	}
}

func TestQueueStatusCompleted_SelectsEpisodeMatch(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	releaseDir := "/virtual/Another.Show.Release"
	metadataSvc.files[releaseDir] = []string{
		"Another.Show.S01E01.mkv",
		"Another.Show.S01E02.mkv",
	}

	fileSize := int64(4096)
	item := &database.ImportQueueItem{
		NzbPath:    "Another.Show.S01E02.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Another.Show.S01E02.2160p.WEB-DL.mkv","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, releaseDir); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}

	if !strings.HasSuffix(status.WebDAVPath, "/Another.Show.S01E02.mkv") {
		t.Fatalf("expected webdav path to end with S01E02 file, got %q", status.WebDAVPath)
	}
}

func TestQueueStatusCompleted_PrefersTitleSimilarity(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	releaseDir := "/virtual/Movie.Collection"
	metadataSvc.files[releaseDir] = []string{
		"Bonus.Featurette.mkv",
		"Movie.Title.2023.2160p.BluRay.x265.mkv",
	}

	fileSize := int64(8192)
	item := &database.ImportQueueItem{
		NzbPath:    "Movie.Title.2023.2160p.BluRay.x265-GROUP.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Movie.Title.2023.2160p.BluRay.x265-GROUP.nzb","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, releaseDir); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}

	if !strings.HasSuffix(status.WebDAVPath, "/Movie.Title.2023.2160p.BluRay.x265.mkv") {
		t.Fatalf("expected movie file to be selected, got %q", status.WebDAVPath)
	}
}

func TestQueueStatusCompleted_SkipsSampleFile(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	releaseDir := "/virtual/Show.S01E01.Release"
	metadataSvc.files[releaseDir] = []string{
		"Show.S01E01.sample.mkv",
		"Show.S01E01.2160p.WEB-DL.mkv",
	}

	fileSize := int64(4096)
	item := &database.ImportQueueItem{
		NzbPath:    "Show.S01E01.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Show.S01E01.2160p.WEB-DL.mkv","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, releaseDir); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}

	// Should select the main file, not the sample
	if strings.Contains(strings.ToLower(status.WebDAVPath), "sample") {
		t.Fatalf("expected sample file to be skipped, got %q", status.WebDAVPath)
	}
	if !strings.HasSuffix(status.WebDAVPath, "/Show.S01E01.2160p.WEB-DL.mkv") {
		t.Fatalf("expected main file to be selected, got %q", status.WebDAVPath)
	}
}

func TestQueueStatusCompleted_SkipsSampleFileOnly(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	// Scenario: only a sample file exists - should return error
	releaseDir := "/virtual/Show.S01E02.Release"
	metadataSvc.files[releaseDir] = []string{
		"Show.S01E02.sample.mkv",
	}

	fileSize := int64(4096)
	item := &database.ImportQueueItem{
		NzbPath:    "Show.S01E02.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Show.S01E02.sample.mkv","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, releaseDir); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	// When only sample files exist, QueueStatus should return an error
	// because no valid playable media was found
	_, err := service.QueueStatus(context.Background(), item.ID)
	if err == nil {
		t.Fatalf("expected error when only sample file exists, got nil")
	}
	if !strings.Contains(err.Error(), "no playable media") {
		t.Fatalf("expected 'no playable media' error, got: %v", err)
	}
}

func TestQueueStatusCompleted_SkipsExtrasFile(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	releaseDir := "/virtual/Movie.2023.Release"
	metadataSvc.files[releaseDir] = []string{
		"Movie.2023.Extras.Behind.The.Scenes.mkv",
		"Movie.2023.2160p.BluRay.mkv",
	}

	fileSize := int64(8192)
	item := &database.ImportQueueItem{
		NzbPath:    "Movie.2023.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Movie.2023.2160p.BluRay.mkv","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, releaseDir); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}

	// Should select the main file, not the extras
	if strings.Contains(strings.ToLower(status.WebDAVPath), "extras") {
		t.Fatalf("expected extras file to be skipped, got %q", status.WebDAVPath)
	}
	if !strings.HasSuffix(status.WebDAVPath, "/Movie.2023.2160p.BluRay.mkv") {
		t.Fatalf("expected main file to be selected, got %q", status.WebDAVPath)
	}
}

func TestQueueStatusCompleted_SkipsSampleDirectory(t *testing.T) {
	service, nzbSystem, metadataSvc := setupPlaybackService(t)
	importerSvc := nzbSystem.ImporterService()

	releaseDir := "/virtual/Show.S01E03.Release"
	sampleDir := "/virtual/Show.S01E03.Release/Sample"

	// Root has no files, but has a Sample subdirectory
	metadataSvc.files[releaseDir] = []string{
		"Show.S01E03.2160p.WEB-DL.mkv",
	}
	metadataSvc.subdirs[releaseDir] = []string{"Sample"}

	// Sample directory has a sample file
	metadataSvc.files[sampleDir] = []string{
		"Show.S01E03.sample.mkv",
	}

	fileSize := int64(4096)
	item := &database.ImportQueueItem{
		NzbPath:    "Show.S01E03.nzb",
		Priority:   database.QueuePriorityNormal,
		Status:     database.QueueStatusPending,
		MaxRetries: 3,
		FileSize:   &fileSize,
	}

	if err := importerSvc.Database().Repository.AddToQueue(item); err != nil {
		t.Fatalf("add to queue: %v", err)
	}

	meta := `{"sourceNzbPath":"Show.S01E03.2160p.WEB-DL.mkv","preflightHealth":"healthy"}`
	if err := importerSvc.Database().Repository.UpdateMetadata(item.ID, &meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}

	if err := importerSvc.Database().Repository.AddStoragePath(item.ID, releaseDir); err != nil {
		t.Fatalf("add storage path: %v", err)
	}

	if err := importerSvc.Database().Repository.UpdateQueueItemStatus(item.ID, database.QueueStatusCompleted, nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	status, err := service.QueueStatus(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("QueueStatus returned error: %v", err)
	}

	// Should select file from root, not from Sample directory
	if strings.Contains(status.WebDAVPath, "/Sample/") {
		t.Fatalf("expected Sample directory to be skipped, got %q", status.WebDAVPath)
	}
	if !strings.HasSuffix(status.WebDAVPath, "/Show.S01E03.2160p.WEB-DL.mkv") {
		t.Fatalf("expected main file to be selected, got %q", status.WebDAVPath)
	}
}
