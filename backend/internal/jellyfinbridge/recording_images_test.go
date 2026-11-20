package jellyfinbridge

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

func resetFrameCache() {
	frameCache.mu.Lock()
	frameCache.entries = map[string]recordingFrame{}
	frameCache.mu.Unlock()
}

// A recording's picture is its programme's guide image, else a still from the
// file; the channel logo is only the channel badge.
func TestRecordingPictures(t *testing.T) {
	h, f, tok := dvrHarness(t)
	resetFrameCache()
	var frames atomic.Int32
	h.srv.deps.RecordingFrame = func(_ context.Context, id string, width int) ([]byte, error) {
		frames.Add(1)
		if id != "aaaaaaaa-0000-0000-0000-0000000000c2" || width != recordingFrameWidth {
			t.Errorf("still of %s at width %d", id, width)
		}
		return []byte("JPEG"), nil
	}
	start := time.Now().Add(-2 * time.Hour).UTC()
	end := start.Add(time.Hour)
	f.recs = []models.Recording{
		{ID: "aaaaaaaa-0000-0000-0000-0000000000c1", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "With Image", Guide: &models.RecordingGuide{Image: "https://img.example/show.jpg"}, StartAt: start, EndAt: end, ActualStartAt: &start, ActualEndAt: &end, OutputPath: "/rec/a.ts", OutputSizeBytes: 1},
		{ID: "aaaaaaaa-0000-0000-0000-0000000000c2", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1", ChannelName: "Channel One",
			Title: "Without Image", StartAt: start, EndAt: end, ActualStartAt: &start, ActualEndAt: &end, OutputPath: "/rec/b.ts", OutputSizeBytes: 1},
	}
	list := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
	items := map[string]dto.BaseItemDto{}
	for _, item := range list.Items {
		items[item.Name] = item
	}
	logoTag := imageTag("https://img.example/1.png")
	for name, item := range items {
		if item.ImageTags[dto.ImagePrimary] == "" || item.ImageTags[dto.ImagePrimary] == logoTag || item.ImageTags[dto.ImageThumb] != item.ImageTags[dto.ImagePrimary] {
			t.Fatalf("%s: image tags %v (logo %s)", name, item.ImageTags, logoTag)
		}
		if item.PrimaryImageAspectRatio == nil || *item.PrimaryImageAspectRatio < 1.7 || item.ChannelPrimaryImageTag != logoTag {
			t.Fatalf("%s: aspect %v, channel badge %q", name, item.PrimaryImageAspectRatio, item.ChannelPrimaryImageTag)
		}
	}
	if got := items["With Image"].ImageTags[dto.ImagePrimary]; got != imageTag("https://img.example/show.jpg") {
		t.Fatalf("guide image tag = %q", got)
	}
	// The still is extracted once and served from the cache after that.
	still := items["Without Image"]
	for i := 0; i < 2; i++ {
		rec := h.do(http.MethodGet, "/Items/"+still.ID+"/Images/Primary?tag="+still.ImageTags[dto.ImagePrimary], "", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "JPEG" || rec.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("still: %d %q", rec.Code, rec.Body.String())
		}
	}
	if frames.Load() != 1 {
		t.Fatalf("stills extracted %d times", frames.Load())
	}

	// Without stills the recording keeps the channel logo.
	h.srv.deps.RecordingFrame = nil
	list = decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
	for _, item := range list.Items {
		if item.Name == "Without Image" && (item.ImageTags[dto.ImagePrimary] != logoTag || item.PrimaryImageAspectRatio != nil) {
			t.Fatalf("logo fallback: %v %v", item.ImageTags, item.PrimaryImageAspectRatio)
		}
	}
}

// A running recording's still is taken again once it has finished.
func TestRecordingStillFollowsTheStatus(t *testing.T) {
	h, f, tok := dvrHarness(t)
	resetFrameCache()
	var frames atomic.Int32
	h.srv.deps.RecordingFrame = func(context.Context, string, int) ([]byte, error) {
		frames.Add(1)
		return []byte("JPEG"), nil
	}
	start := time.Now().Add(-30 * time.Minute).UTC()
	f.recs = []models.Recording{{ID: "aaaaaaaa-0000-0000-0000-0000000000d1", UserID: h.alice.ID, Status: models.RecordingStatusRunning, ChannelID: "ch-1",
		ChannelName: "Channel One", Title: "Growing", StartAt: start, EndAt: start.Add(time.Hour), ActualStartAt: &start, OutputPath: "/rec/g.ts", OutputSizeBytes: 1}}
	fetch := func() string {
		list := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
		item := list.Items[0]
		if rec := h.do(http.MethodGet, "/Items/"+item.ID+"/Images/Primary", "", nil); rec.Code != http.StatusOK {
			t.Fatalf("still: %d", rec.Code)
		}
		return item.ImageTags[dto.ImagePrimary]
	}
	running := fetch()
	f.mu.Lock()
	end := time.Now().UTC()
	f.recs[0].Status, f.recs[0].ActualEndAt = models.RecordingStatusCompleted, &end
	f.mu.Unlock()
	if done := fetch(); done == running || frames.Load() != 2 {
		t.Fatalf("tag %q -> %q, stills %d", running, done, frames.Load())
	}
}

// A recording shows what the guide said about its programme: episode numbers
// and title in its name (Video items get no episode line in Jellyfin
// clients), year, genres, and its file's size as a media source.
func TestRecordingGuideDetails(t *testing.T) {
	h, f, tok := dvrHarness(t)
	start := time.Now().Add(-2 * time.Hour).UTC()
	end := start.Add(time.Hour)
	f.recs = []models.Recording{{ID: "aaaaaaaa-0000-0000-0000-0000000000e1", UserID: h.alice.ID, Status: models.RecordingStatusCompleted, ChannelID: "ch-1",
		ChannelName: "Channel One", Title: "Death in Paradise", StartAt: start, EndAt: end, ActualStartAt: &start, ActualEndAt: &end, OutputPath: "/rec/d.ts", OutputSizeBytes: 1,
		Guide: &models.RecordingGuide{EpisodeTitle: "Mord im Paradies", Episode: "S12 E2/8", Year: 2023, Categories: []string{"Krimiserie"}, Rating: "12"}}}
	list := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Recordings", tok, nil))
	if len(list.Items) != 1 {
		t.Fatalf("recordings = %+v", list.Items)
	}
	item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+list.Items[0].ID, tok, nil))
	if item.Name != "Death in Paradise – S12:E2 – Mord im Paradies" || item.EpisodeTitle != "" || item.ParentIndexNumber == nil || *item.ParentIndexNumber != 12 ||
		item.IndexNumber == nil || *item.IndexNumber != 2 || item.ProductionYear == nil || *item.ProductionYear != 2023 ||
		len(item.Genres) != 1 || item.OfficialRating != "12" || item.SortName != "death in paradise 0012 00002" {
		t.Fatalf("item = %+v", item)
	}
	if len(item.MediaSources) != 1 || item.MediaSources[0].Container != "ts" || item.MediaSources[0].Size == nil || *item.MediaSources[0].Size != 1 {
		t.Fatalf("media sources = %+v", item.MediaSources)
	}
	// Without episode numbers the episode title still shows.
	f.mu.Lock()
	f.recs[0].Guide = &models.RecordingGuide{EpisodeTitle: "Schein und Sein"}
	f.mu.Unlock()
	if item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+list.Items[0].ID, tok, nil)); item.Name != "Death in Paradise – Schein und Sein" {
		t.Fatalf("name = %q", item.Name)
	}
}

func TestEpisodeNumbers(t *testing.T) {
	for raw, want := range map[string][2]int{
		"S12 E2/8": {12, 2}, "S01E05": {1, 5}, "E5": {0, 5}, "S3": {3, 0}, "S2 E3 P1/2": {2, 3}, "": {0, 0}, "Folge 4": {0, 0},
	} {
		if s, e := episodeNumbers(raw); s != want[0] || e != want[1] {
			t.Errorf("episodeNumbers(%q) = %d, %d, want %v", raw, s, e, want)
		}
	}
}
