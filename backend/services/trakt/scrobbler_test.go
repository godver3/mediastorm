package trakt

import "testing"

func TestShowSyncIDs(t *testing.T) {
	tests := []struct {
		name       string
		tvdbID     int
		externalID map[string]string
		want       SyncIDs
	}{
		{
			name:       "explicit tvdb wins and other IDs are preserved",
			tvdbID:     121361,
			externalID: map[string]string{"tvdb": "999999", "tmdb": "1399", "imdb": "tt0944947", "trakt": "353"},
			want:       SyncIDs{TVDB: 121361, TMDB: 1399, IMDB: "tt0944947", Trakt: 353},
		},
		{
			name:       "falls back to external IDs without tvdb param",
			externalID: map[string]string{"tvdb": "401003", "tmdb": "124364", "imdb": "tt9813792", "trakt": "164767"},
			want:       SyncIDs{TVDB: 401003, TMDB: 124364, IMDB: "tt9813792", Trakt: 164767},
		},
		{
			name:       "tmdb imdb and trakt work without tvdb",
			externalID: map[string]string{"tmdb": "124364", "imdb": "tt9813792", "trakt": "164767"},
			want:       SyncIDs{TMDB: 124364, IMDB: "tt9813792", Trakt: 164767},
		},
		{
			name: "empty map returns zero value",
			want: SyncIDs{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShowSyncIDs(tt.tvdbID, tt.externalID); got != tt.want {
				t.Fatalf("ShowSyncIDs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
