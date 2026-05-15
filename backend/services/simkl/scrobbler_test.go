package simkl

import (
	"testing"
	"time"

	"novastream/models"
)

func testEpisodeUpdate() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "tvdb:series:153021:s01e03",
		SeriesID:      "tvdb:series:153021",
		SeriesName:    "The Walking Dead",
		SeasonNumber:  1,
		EpisodeNumber: 3,
		ExternalIDs: map[string]string{
			"tvdb": "153021",
			"imdb": "tt1520211",
		},
	}
}

func TestRecentStopSuppressesImmediateWatchedSync(t *testing.T) {
	scrobbler := NewScrobbler(NewClient(), nil)
	update := testEpisodeUpdate()
	scrobbler.noteRecentStop("user-1", update)

	if !scrobbler.wasRecentlyStopped("user-1", "episode", 0, 153021, "", 1, 3) {
		t.Fatal("expected recent stop to suppress matching watched sync")
	}
	if scrobbler.wasRecentlyStopped("user-1", "episode", 0, 153021, "", 1, 4) {
		t.Fatal("different episode should not be suppressed")
	}
}

func TestRecentStopExpires(t *testing.T) {
	scrobbler := NewScrobbler(NewClient(), nil)
	scrobbler.recentStops[recentStopKey("user-1", "movie", "tmdb", "27205", 0, 0)] = time.Now().Add(-10 * time.Minute)

	if scrobbler.wasRecentlyStopped("user-1", "movie", 27205, 0, "", 0, 0) {
		t.Fatal("expired recent stop should not suppress watched sync")
	}
}
