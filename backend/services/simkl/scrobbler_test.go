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

func TestShowSyncIDs(t *testing.T) {
	tests := []struct {
		name       string
		tvdbID     int
		externalID map[string]string
		want       IDs
	}{
		{
			name:       "explicit tvdb wins and other IDs are preserved",
			tvdbID:     153021,
			externalID: map[string]string{"tvdb": "999999", "tmdb": "1402", "imdb": "tt1520211", "simkl": "41086"},
			want:       IDs{TVDB: 153021, TMDB: 1402, IMDB: "tt1520211", Simkl: 41086},
		},
		{
			name:       "falls back to external IDs without tvdb param",
			externalID: map[string]string{"tvdb": "401003", "tmdb": "124364", "imdb": "tt9813792", "simkl": "1481305"},
			want:       IDs{TVDB: 401003, TMDB: 124364, IMDB: "tt9813792", Simkl: 1481305},
		},
		{
			name:       "tmdb imdb and simkl work without tvdb",
			externalID: map[string]string{"tmdb": "124364", "imdb": "tt9813792", "simkl": "1481305"},
			want:       IDs{TMDB: 124364, IMDB: "tt9813792", Simkl: 1481305},
		},
		{
			name: "empty map returns zero value",
			want: IDs{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := showSyncIDs(tt.tvdbID, tt.externalID); got != tt.want {
				t.Fatalf("showSyncIDs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
