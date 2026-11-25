package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

// remoteMediaSyncer syncs the Plex and Jellyfin libraries.
type remoteMediaSyncer interface {
	ListLibraries(ctx context.Context) ([]models.RemoteMediaLibrary, error)
	Sync(ctx context.Context, id string) (int, error)
}

// SetRemoteMediaService wires Plex/Jellyfin libraries into library scan tasks.
func (s *Service) SetRemoteMediaService(remote remoteMediaSyncer) {
	s.remoteMediaService = remote
}

// libraryScanTarget is one library a scan task can refresh, whatever its source.
type libraryScanTarget struct {
	id, name, source string
	run              func(ctx context.Context) (models.LocalMediaScanSummary, error)
}

func (s *Service) libraryScanTargets(ctx context.Context) ([]libraryScanTarget, error) {
	var targets []libraryScanTarget
	if s.localMediaService != nil {
		libraries, err := s.localMediaService.ListLibraries(ctx)
		if err != nil {
			return nil, fmt.Errorf("list local media libraries: %w", err)
		}
		for _, library := range libraries {
			targets = append(targets, libraryScanTarget{
				id: library.ID, name: library.Name, source: models.MediaSourceLocal,
				run: func(ctx context.Context) (models.LocalMediaScanSummary, error) {
					return s.localMediaService.RunScan(ctx, library.ID)
				},
			})
		}
	}
	if s.remoteMediaService != nil {
		libraries, err := s.remoteMediaService.ListLibraries(ctx)
		if err != nil {
			return nil, fmt.Errorf("list remote media libraries: %w", err)
		}
		for _, library := range libraries {
			targets = append(targets, libraryScanTarget{
				id: library.ID, name: library.Name, source: library.Provider,
				run: func(ctx context.Context) (models.LocalMediaScanSummary, error) {
					// Remote items arrive already identified by their server.
					count, err := s.remoteMediaService.Sync(ctx, library.ID)
					return models.LocalMediaScanSummary{Discovered: count, Matched: count}, err
				},
			})
		}
	}
	return targets, nil
}

// executeLocalMediaScan scans local libraries and syncs Plex/Jellyfin ones. One
// failing library does not stop the others; failures are reported together.
func (s *Service) executeLocalMediaScan(task config.ScheduledTask) (SyncResult, error) {
	libraryID := strings.TrimSpace(task.Config["libraryId"])
	if libraryID == "" {
		return SyncResult{}, errors.New("library scan requires libraryId")
	}
	ctx := context.Background()
	targets, err := s.libraryScanTargets(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	if libraryID != config.ScheduledTaskLocalMediaAllLibraries {
		var selected []libraryScanTarget
		for _, target := range targets {
			if target.id == libraryID {
				selected = append(selected, target)
			}
		}
		if len(selected) == 0 {
			return SyncResult{}, errors.New("the selected library no longer exists")
		}
		targets = selected
	}
	if len(targets) == 0 {
		return SyncResult{}, errors.New("no libraries configured")
	}

	var total models.LocalMediaScanSummary
	var failures []error
	for _, target := range targets {
		started := time.Now()
		summary, err := target.run(ctx)
		if err != nil {
			log.Printf("[scheduler] Library scan failed library=%q source=%s duration=%s err=%v", target.name, target.source, time.Since(started), err)
			failures = append(failures, fmt.Errorf("scan library %q (%s): %w", target.name, target.source, err))
			continue
		}
		log.Printf("[scheduler] Library scan complete library=%q source=%s items=%d duration=%s", target.name, target.source, summary.Discovered, time.Since(started))
		total.Discovered += summary.Discovered
		total.Matched += summary.Matched
		total.LowConfidence += summary.LowConfidence
		total.Unmatched += summary.Unmatched
	}
	return SyncResult{
		Count: total.Discovered,
		Message: fmt.Sprintf("Library scan completed for %d of %d libraries: %d discovered, %d matched, %d low confidence, %d unmatched",
			len(targets)-len(failures), len(targets), total.Discovered, total.Matched, total.LowConfidence, total.Unmatched),
	}, errors.Join(failures...)
}
