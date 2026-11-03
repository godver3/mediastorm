package scheduler

import (
	"fmt"
	"time"

	"novastream/config"
)

// Failed and preview runs update LastRunAt for scheduling, but must not become
// incremental cursors. Replaying the library also retries partial exports.
func historySyncLastRun(task config.ScheduledTask) *time.Time {
	if task.Config["fullSync"] == "true" || task.Config["fullExport"] == "true" ||
		task.LastStatus == config.ScheduledTaskStatusError || task.DryRunDetails != nil {
		return nil
	}
	return task.LastRunAt
}

func (s *Service) recordFullHistorySync(key string) {
	s.lastFullSyncTimesMu.Lock()
	defer s.lastFullSyncTimesMu.Unlock()
	if s.lastFullSyncTimes == nil {
		s.lastFullSyncTimes = make(map[string]time.Time)
	}
	s.lastFullSyncTimes[key] = time.Now().UTC()
}

func combineHistorySyncResults(in, out SyncResult) SyncResult {
	combined := SyncResult{
		Count: in.Count + out.Count, DryRun: in.DryRun || out.DryRun,
		ToAdd: append(in.ToAdd, out.ToAdd...), ToRemove: append(in.ToRemove, out.ToRemove...),
	}
	if len(in.Config)+len(out.Config) > 0 {
		combined.Config = make(map[string]string)
		for key, value := range in.Config {
			combined.Config[key] = value
		}
		for key, value := range out.Config {
			combined.Config[key] = value
		}
	}
	return combined
}

// prepareFullSync overrides only the execution copy. Saved task settings and
// cursors remain intact until the run records its result.
func prepareFullSync(task config.ScheduledTask) (config.ScheduledTask, error) {
	switch task.Type {
	case config.ScheduledTaskTypePlexWatchlistSync,
		config.ScheduledTaskTypeTraktListSync,
		config.ScheduledTaskTypeTraktHistorySync,
		config.ScheduledTaskTypeSimklHistorySync,
		config.ScheduledTaskTypeSimklWatchlistSync,
		config.ScheduledTaskTypeScrobHistorySync,
		config.ScheduledTaskTypePlexHistorySync,
		config.ScheduledTaskTypeJellyfinFavoritesSync,
		config.ScheduledTaskTypeJellyfinHistorySync,
		config.ScheduledTaskTypeMDBListWatchlistSync,
		config.ScheduledTaskTypeMDBListHistorySync:
	default:
		return task, fmt.Errorf("full sync is only available for sync tasks")
	}
	task.LastRunAt = nil
	copied := make(map[string]string, len(task.Config)+2)
	for key, value := range task.Config {
		copied[key] = value
	}
	task.Config = copied
	task.Config["fullSync"] = "true"
	task.Config["fullExport"] = "true"
	delete(task.Config, "lastSimklActivityAt")
	return task, nil
}
