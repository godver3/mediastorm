package handlers

import (
	"testing"

	"novastream/config"
)

func TestPlexReconnectionStatusMarksAuthFailures(t *testing.T) {
	tasks := []config.ScheduledTask{
		{
			Type:       config.ScheduledTaskTypePlexWatchlistSync,
			LastStatus: config.ScheduledTaskStatusError,
			LastError:  "fetch plex watchlist: plex watchlist failed: 401 Unauthorized",
			Config: map[string]string{
				"plexAccountId": "plex-1",
			},
		},
		{
			Type:       config.ScheduledTaskTypePlexHistorySync,
			LastStatus: config.ScheduledTaskStatusSuccess,
			LastError:  "401 Unauthorized",
			Config: map[string]string{
				"plexAccountId": "plex-2",
			},
		},
		{
			Type:       config.ScheduledTaskTypeTraktHistorySync,
			LastStatus: config.ScheduledTaskStatusError,
			LastError:  "401 Unauthorized",
			Config: map[string]string{
				"plexAccountId": "plex-3",
			},
		},
	}

	status := plexReconnectionStatus(tasks)
	if !status["plex-1"] {
		t.Fatal("expected plex-1 to require reconnection")
	}
	if status["plex-2"] {
		t.Fatal("did not expect successful task to require reconnection")
	}
	if status["plex-3"] {
		t.Fatal("did not expect non-Plex task to require reconnection")
	}
}
