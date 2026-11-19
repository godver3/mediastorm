package jellyfinbridge

import (
	"net/http"
	"testing"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// jellyfin-web puts the red recording dot on every item with a TimerId, so
// only a recording that is still recording carries one.
func TestOnlyRunningRecordingsHaveATimer(t *testing.T) {
	h, f, tok := dvrHarness(t)
	start := time.Now().Add(-10 * time.Minute).UTC()
	end := start.Add(5 * time.Minute)
	f.recs = []models.Recording{
		{ID: "aaaaaaaa-0000-0000-0000-0000000000d1", UserID: h.alice.ID, Status: models.RecordingStatusRunning, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Live Now", StartAt: start, EndAt: start.Add(time.Hour), ActualStartAt: &start, OutputPath: "/rec/live.ts", OutputSizeBytes: 1},
		{ID: "aaaaaaaa-0000-0000-0000-0000000000d2", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Done", StartAt: start, EndAt: end, ActualStartAt: &start, ActualEndAt: &end, OutputPath: "/rec/done.ts", OutputSizeBytes: 1},
	}
	list := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
	timers := map[string]string{}
	for _, item := range list.Items {
		timers[item.Name] = item.TimerID
	}
	if len(timers) != 2 || timers["Live Now"] == "" || timers["Done"] != "" {
		t.Fatalf("timer ids = %v, want one for the running recording only", timers)
	}
}
