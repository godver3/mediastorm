package models

// UserSettings contains per-user customizable settings.
// These override global defaults when set.
type UserSettings struct {
	Playback    PlaybackSettings    `json:"playback"`
	HomeShelves HomeShelvesSettings `json:"homeShelves"`
	Filtering   FilterSettings      `json:"filtering"`
	LiveTV      LiveTVSettings      `json:"liveTV"`
}

// LiveTVSettings contains per-user Live TV preferences.
type LiveTVSettings struct {
	HiddenChannels     []string `json:"hiddenChannels"`     // Channel IDs that are hidden
	FavoriteChannels   []string `json:"favoriteChannels"`   // Channel IDs that are favorited
	SelectedCategories []string `json:"selectedCategories"` // Selected category filters
}

// PlaybackSettings controls how the client should launch resolved streams.
type PlaybackSettings struct {
	PreferredPlayer           string  `json:"preferredPlayer"`
	PreferredAudioLanguage    string  `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage string  `json:"preferredSubtitleLanguage,omitempty"`
	PreferredSubtitleMode     string  `json:"preferredSubtitleMode,omitempty"`
	UseLoadingScreen          bool    `json:"useLoadingScreen,omitempty"`
	SubtitleSize              float64 `json:"subtitleSize,omitempty"` // Scaling factor for subtitle size (1.0 = default)
}

// ShelfConfig represents a configurable home screen shelf.
type ShelfConfig struct {
	ID      string `json:"id"`      // Unique identifier (e.g., "continue-watching", "watchlist", "trending-movies")
	Name    string `json:"name"`    // Display name
	Enabled bool   `json:"enabled"` // Whether the shelf is visible
	Order   int    `json:"order"`   // Sort order (lower numbers appear first)
}

// TrendingMovieSource determines which source to use for trending movies.
type TrendingMovieSource string

const (
	TrendingMovieSourceAll      TrendingMovieSource = "all"      // TMDB trending (includes unreleased)
	TrendingMovieSourceReleased TrendingMovieSource = "released" // MDBList top movies of the week (released only)
)

// HomeShelvesSettings controls which shelves appear on the home screen and their order.
type HomeShelvesSettings struct {
	Shelves             []ShelfConfig       `json:"shelves"`
	TrendingMovieSource TrendingMovieSource `json:"trendingMovieSource,omitempty"` // "all" (TMDB) or "released" (MDBList)
}

// FilterSettings controls content filtering preferences.
type FilterSettings struct {
	MaxSizeMovieGB   float64  `json:"maxSizeMovieGb"`
	MaxSizeEpisodeGB float64  `json:"maxSizeEpisodeGb"`
	ExcludeHdr       bool     `json:"excludeHdr"`
	PrioritizeHdr    bool     `json:"prioritizeHdr"`  // Prioritize HDR/DV content in search results
	FilterOutTerms   []string `json:"filterOutTerms"` // Terms to filter out from results (exact match in title)
}

// DefaultUserSettings returns the default settings for a new user.
func DefaultUserSettings() UserSettings {
	return UserSettings{
		Playback: PlaybackSettings{
			PreferredPlayer:  "native",
			UseLoadingScreen: false,
			SubtitleSize:     1.0,
		},
		HomeShelves: HomeShelvesSettings{
			Shelves: []ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 1},
				{ID: "trending-movies", Name: "Trending Movies", Enabled: true, Order: 2},
				{ID: "trending-tv", Name: "Trending TV Shows", Enabled: true, Order: 3},
			},
			TrendingMovieSource: TrendingMovieSourceReleased,
		},
		Filtering: FilterSettings{
			MaxSizeMovieGB:   0,
			MaxSizeEpisodeGB: 0,
			ExcludeHdr:       false,
			PrioritizeHdr:    true,
		},
		LiveTV: LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
	}
}
