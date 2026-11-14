package models

import (
	"strings"
	"time"
)

// Basic metadata structures for titles and images.

// LanguageAlias is a language-tagged alternate title (e.g. from TVDB aliases).
type LanguageAlias struct {
	Name     string
	Language string // ISO 639-2/B code, e.g. "eng", "jpn"
}

type Image struct {
	URL                string `json:"url"`
	Type               string `json:"type"` // poster, backdrop, logo
	Width              int    `json:"width"`
	Height             int    `json:"height"`
	IsDark             bool   `json:"is_dark,omitempty"`
	IsTextless         bool   `json:"is_textless,omitempty"`
	Language           string `json:"language,omitempty"`
	IsFallbackLanguage bool   `json:"is_fallback_language,omitempty"`
}

type Trailer struct {
	Name            string `json:"name"`
	Site            string `json:"site,omitempty"`
	Type            string `json:"type,omitempty"`
	URL             string `json:"url"`
	EmbedURL        string `json:"embedUrl,omitempty"`
	ThumbnailURL    string `json:"thumbnailUrl,omitempty"`
	Language        string `json:"language,omitempty"`
	Country         string `json:"country,omitempty"`
	Key             string `json:"key,omitempty"`
	Official        bool   `json:"official,omitempty"`
	PublishedAt     string `json:"publishedAt,omitempty"`
	Resolution      int    `json:"resolution,omitempty"`
	Source          string `json:"source,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	SeasonNumber    int    `json:"seasonNumber,omitempty"` // 0 = series-level trailer
}

// Rating represents a single rating from a source
type Rating struct {
	Source string  `json:"source"` // imdb, tmdb, trakt, letterboxd, tomatoes, audience, metacritic
	Value  float64 `json:"value"`  // Rating value (scale varies by source)
	Max    float64 `json:"max"`    // Maximum possible value (e.g., 10 for IMDB, 100 for RT)
}

type Title struct {
	SourceAvailability *SourceAvailability `json:"sourceAvailability,omitempty"`
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	OriginalName       string              `json:"originalName,omitempty"`
	AlternateTitles    []string            `json:"alternateTitles,omitempty"`
	Overview           string              `json:"overview"`
	Year               int                 `json:"year"`
	Language           string              `json:"language"`
	CountryCode        string              `json:"countryCode,omitempty"` // Original production country (ISO 3166 where available)
	Poster             *Image              `json:"poster,omitempty"`
	TextPoster         *Image              `json:"textPoster,omitempty"` // Original poster with text (preserved when Poster is overridden with textless)
	Posters            []Image             `json:"posters,omitempty"`    // Additional localized/textless poster options beyond the primary
	Backdrop           *Image              `json:"backdrop,omitempty"`
	TextBackdrop       *Image              `json:"textBackdrop,omitempty"` // Original backdrop with text (preserved when Backdrop is overridden with textless)
	Backdrops          []Image             `json:"backdrops,omitempty"`    // Additional backdrop options beyond the primary
	Logo               *Image              `json:"logo,omitempty"`
	MediaType          string              `json:"mediaType"` // series | movie | person (search cards only)
	TVDBID             int64               `json:"tvdbId,omitempty"`
	IMDBID             string              `json:"imdbId,omitempty"`
	TMDBID             int64               `json:"tmdbId,omitempty"`
	Popularity         float64             `json:"popularity,omitempty"`
	VoteCount          int                 `json:"voteCount,omitempty"`
	Network            string              `json:"network,omitempty"`
	AirsTime           string              `json:"airsTime,omitempty"`        // e.g. "21:00" — local air time from TVDB
	AirsTimezone       string              `json:"airsTimezone,omitempty"`    // IANA timezone inferred from network/country
	Status             string              `json:"status,omitempty"`          // Release availability: movies released/theatrical/upcoming/unknown; series released/unreleased.
	LifecycleStatus    string              `json:"lifecycleStatus,omitempty"` // Series lifecycle from provider (Continuing, Ended, Upcoming, etc.).
	IsDaily            bool                `json:"isDaily,omitempty"`         // True for shows whose releases commonly use date-based episode naming
	Certification      string              `json:"certification,omitempty"`   // MPAA/TV content rating (G, PG, PG-13, R, TV-Y, TV-G, TV-PG, TV-14, TV-MA)
	PrimaryTrailer     *Trailer            `json:"primaryTrailer,omitempty"`
	Trailers           []Trailer           `json:"trailers,omitempty"`
	Releases           []Release           `json:"releases,omitempty"`
	Theatrical         *Release            `json:"theatricalRelease,omitempty"`
	HomeRelease        *Release            `json:"homeRelease,omitempty"`
	ReleaseDate        string              `json:"releaseDate,omitempty"`       // YYYY-MM-DD the title first became/becomes watchable (movie: earliest theatrical/home window, else primary release date; series: premiere/first air date)
	Availability       string              `json:"availability,omitempty"`      // Derived at encode time: released | theatrical | coming_soon | unreleased (see ReleaseAvailability)
	Ratings            []Rating            `json:"ratings,omitempty"`           // Aggregated ratings from MDBList
	Credits            *Credits            `json:"credits,omitempty"`           // Top billed cast
	RuntimeMinutes     int                 `json:"runtimeMinutes,omitempty"`    // Runtime in minutes (movies only)
	Collection         *Collection         `json:"collection,omitempty"`        // Movie collection (movies only)
	Genres             []string            `json:"genres,omitempty"`            // Genre names from TMDB
	Adult              bool                `json:"adult,omitempty"`             // True when the metadata provider marks this title as adult content
	WatchState         string              `json:"watchState,omitempty"`        // "none" | "partial" | "complete"
	UnwatchedCount     *int                `json:"unwatchedCount,omitempty"`    // series only: total - watched
	CardSubtitle       string              `json:"cardSubtitle,omitempty"`      // Optional shelf-specific context rendered above the title
	CardImage          *Image              `json:"cardImage,omitempty"`         // Optional shelf-specific landscape image
	ForceTitleOverlay  bool                `json:"forceTitleOverlay,omitempty"` // Keep title/subtitle visible on clean-poster layouts
}

type TrendingItem struct {
	Rank  int   `json:"rank"`
	Title Title `json:"title"`
}

type SearchResult struct {
	Title Title `json:"title"`
	Score int   `json:"score"`
}

type YouTubeVideoSearchResult struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	Channel      string `json:"channel,omitempty"`
	Uploader     string `json:"uploader,omitempty"`
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	ViewCount    int64  `json:"viewCount,omitempty"`
}

type SeriesEpisode struct {
	Numbering             *EpisodeNumbering `json:"numbering,omitempty"`
	ID                    string            `json:"id"`
	TMDBID                int64             `json:"tmdbId,omitempty"`
	TMDBSeasonNumber      int               `json:"tmdbSeasonNumber,omitempty"`
	TMDBEpisodeNumber     int               `json:"tmdbEpisodeNumber,omitempty"`
	TVDBID                int64             `json:"tvdbId,omitempty"`
	Name                  string            `json:"name"`
	Overview              string            `json:"overview"`
	SeasonNumber          int               `json:"seasonNumber"`
	EpisodeNumber         int               `json:"episodeNumber"`
	AbsoluteEpisodeNumber int               `json:"absoluteEpisodeNumber,omitempty"` // Release absolute number; excludes season-zero specials.
	AiredDate             string            `json:"airedDate,omitempty"`
	AiredDateTimeUTC      string            `json:"airedDateTimeUTC,omitempty"`
	Runtime               int               `json:"runtimeMinutes,omitempty"`
	Image                 *Image            `json:"image,omitempty"`

	// Per-user fields populated only on details-bundle responses (never cached
	// metadata). ItemID is the canonical progress/watch item ID for this episode;
	// Watched reports watch history; Resume is the server resume decision.
	ItemID  string       `json:"itemId,omitempty"`
	Watched bool         `json:"watched,omitempty"`
	Resume  *ResumeState `json:"resume,omitempty"`
}

type SeriesSeason struct {
	ID           string          `json:"id"`
	TVDBID       int64           `json:"tvdbId,omitempty"`
	Name         string          `json:"name"`
	Number       int             `json:"number"`
	Overview     string          `json:"overview"`
	Type         string          `json:"type,omitempty"`
	Image        *Image          `json:"image,omitempty"`
	EpisodeCount int             `json:"episodeCount"`
	Episodes     []SeriesEpisode `json:"episodes"`
}

type SeriesDetails struct {
	Numbering           *EpisodeNumbering `json:"numbering,omitempty"`
	Title               Title             `json:"title"`
	Seasons             []SeriesSeason    `json:"seasons"`
	PreferredSeason     *int              `json:"preferredSeason,omitempty"`
	EpisodeTMDBEnriched bool              `json:"episodeTmdbEnriched,omitempty"`
	// AvailableOrderings lists every TVDB season-ordering for this series
	// (official, dvd, absolute, alternate, regional, …). Populated only when
	// more than one ordering exists so the client can offer a switcher.
	AvailableOrderings []SeriesOrdering `json:"availableOrderings,omitempty"`
	// ActiveOrdering is the lowercase season-type the returned seasons/episodes
	// are numbered under (e.g. "official", "dvd", "absolute").
	ActiveOrdering string `json:"activeOrdering,omitempty"`
}

// SeriesOrdering describes one selectable TVDB episode ordering.
type SeriesOrdering struct {
	Type        string `json:"type"`        // canonical lowercase season-type key (official, dvd, absolute, alternate, regional, …)
	Name        string `json:"name"`        // human-readable label, falls back to Type
	SeasonCount int    `json:"seasonCount"` // number of seasons under this ordering
	IsOfficial  bool   `json:"isOfficial"`  // true for the aired/official ordering (sync-safe)
}

type SeriesDetailsQuery struct {
	TitleID string
	Name    string
	Year    int
	TVDBID  int64
	TMDBID  int64
	IMDBID  string
	// SeasonType, when set, overrides the auto-detected primary ordering with a
	// specific TVDB season-type (lowercase, e.g. "dvd", "absolute"). Empty means
	// auto-detect the official/primary ordering.
	SeasonType string
}

type TrailerQuery struct {
	MediaType    string
	TitleID      string
	Name         string
	Year         int
	IMDBID       string
	TMDBID       int64
	TVDBID       int64
	SeasonNumber int // 0 = show-level trailers, >0 = season-specific trailers
}

type TrailerResponse struct {
	PrimaryTrailer *Trailer  `json:"primaryTrailer,omitempty"`
	Trailers       []Trailer `json:"trailers"`
}

type MovieDetailsQuery struct {
	TitleID string
	Name    string
	Year    int
	IMDBID  string
	TMDBID  int64
	TVDBID  int64
}

type Release struct {
	Type     string `json:"type"`               // theatrical | theatricalLimited | digital | physical | premiere | tv
	Date     string `json:"date"`               // ISO 8601
	Country  string `json:"country,omitempty"`  // ISO 3166-1 alpha-2
	Note     string `json:"note,omitempty"`     // limited, IMAX, etc.
	Source   string `json:"source"`             // tmdb
	Primary  bool   `json:"primary,omitempty"`  // best pick within type bucket
	Released bool   `json:"released,omitempty"` // true when date <= today
}

const (
	MovieReleaseStatusReleased    = "released"
	MovieReleaseStatusTheatrical  = "theatrical"
	MovieReleaseStatusUpcoming    = "upcoming"
	MovieReleaseStatusUnknown     = "unknown"
	SeriesReleaseStatusReleased   = "released"
	SeriesReleaseStatusUnreleased = "unreleased"
)

// MovieReleaseStatus normalizes movie availability into a stable status string.
// Home releases mean available. Older theatrical releases are also treated as
// available even when TMDB has no digital/physical date.
func MovieReleaseStatus(title Title) string {
	return MovieReleaseStatusAt(title, time.Now())
}

// MovieReleaseStatusAt is MovieReleaseStatus evaluated at now.
func MovieReleaseStatusAt(title Title, now time.Time) string {
	if !strings.EqualFold(strings.TrimSpace(title.MediaType), "movie") {
		return strings.TrimSpace(title.Status)
	}
	// Treat any completed home window as released. The primary HomeRelease
	// pointer is a display choice and must not hide an already-available
	// physical/TV release merely because a higher-priority digital date is
	// still in the future.
	for i := range title.Releases {
		switch strings.ToLower(strings.TrimSpace(title.Releases[i].Type)) {
		case "digital", "physical", "tv":
			if releaseIsReleasedAt(&title.Releases[i], now) {
				return MovieReleaseStatusReleased
			}
		}
	}
	status := MovieReleaseStatusFromWindowsAt(title.Theatrical, title.HomeRelease, now)
	if status != MovieReleaseStatusUnknown {
		return status
	}
	if dated := MovieReleaseStatusFromReleaseDateAt(title.ReleaseDate, now); dated != MovieReleaseStatusUnknown {
		return dated
	}
	if title.Year > 0 && title.Year < now.Year() {
		return MovieReleaseStatusReleased
	}
	if title.Year >= now.Year() {
		return MovieReleaseStatusUpcoming
	}
	return status
}

func MovieReleaseStatusFromWindows(theatrical, home *Release) string {
	return MovieReleaseStatusFromWindowsAt(theatrical, home, time.Now())
}

// MovieReleaseStatusFromWindowsAt is MovieReleaseStatusFromWindows evaluated
// at now.
func MovieReleaseStatusFromWindowsAt(theatrical, home *Release, now time.Time) string {
	if releaseIsReleasedAt(home, now) {
		return MovieReleaseStatusReleased
	}
	if releaseIsReleasedAt(theatrical, now) {
		if releaseIsOlderThan(theatrical, 12, now) {
			return MovieReleaseStatusReleased
		}
		return MovieReleaseStatusTheatrical
	}
	if releaseHasDate(theatrical) || releaseHasDate(home) {
		return MovieReleaseStatusUpcoming
	}
	return MovieReleaseStatusUnknown
}

func MovieReleaseStatusFromReleaseDate(releaseDate string) string {
	return MovieReleaseStatusFromReleaseDateAt(releaseDate, time.Now())
}

// MovieReleaseStatusFromReleaseDateAt is MovieReleaseStatusFromReleaseDate
// evaluated at now, for callers that work with an explicit clock.
func MovieReleaseStatusFromReleaseDateAt(releaseDate string, now time.Time) string {
	release := &Release{Type: "theatrical", Date: releaseDate}
	if !releaseHasDate(release) {
		return MovieReleaseStatusUnknown
	}
	if releaseIsReleasedAt(release, now) {
		if releaseIsOlderThan(release, 12, now) {
			return MovieReleaseStatusReleased
		}
		return MovieReleaseStatusTheatrical
	}
	return MovieReleaseStatusUpcoming
}

func SeriesReleaseStatusFromSeasons(seasons []SeriesSeason) string {
	now := time.Now()
	for _, season := range seasons {
		for _, episode := range season.Episodes {
			if SeriesEpisodeHasAired(episode, now) {
				return SeriesReleaseStatusReleased
			}
		}
	}
	return SeriesReleaseStatusUnreleased
}

func SeriesReleaseStatusFromDate(airDate string) string {
	ts, ok := parseReleaseDate(airDate)
	if !ok {
		return SeriesReleaseStatusUnreleased
	}
	if ts.After(time.Now()) {
		return SeriesReleaseStatusUnreleased
	}
	return SeriesReleaseStatusReleased
}

func SeriesEpisodeHasAired(episode SeriesEpisode, now time.Time) bool {
	if ts, ok := parseReleaseDate(episode.AiredDateTimeUTC); ok {
		return !ts.After(now)
	}
	if ts, ok := parseReleaseDate(episode.AiredDate); ok {
		return !ts.After(now)
	}
	return false
}

func releaseIsReleased(release *Release) bool {
	return releaseIsReleasedAt(release, time.Now())
}

func releaseIsReleasedAt(release *Release, now time.Time) bool {
	if release == nil {
		return false
	}
	if release.Released {
		return true
	}
	if ts, ok := parseReleaseDate(release.Date); ok {
		return !ts.After(now)
	}
	return false
}

func releaseHasDate(release *Release) bool {
	if release == nil {
		return false
	}
	_, ok := parseReleaseDate(release.Date)
	return ok
}

func releaseIsOlderThan(release *Release, months int, now time.Time) bool {
	if release == nil {
		return false
	}
	ts, ok := parseReleaseDate(release.Date)
	return ok && !ts.After(now.AddDate(0, -months, 0))
}

func parseReleaseDate(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return ts, true
	}
	if len(trimmed) >= len("2006-01-02") {
		if ts, err := time.Parse("2006-01-02", trimmed[:len("2006-01-02")]); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

// CastMember represents an actor in a movie or series
type CastMember struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	Order       int    `json:"order"`
	ProfilePath string `json:"profilePath,omitempty"`
	ProfileURL  string `json:"profileUrl,omitempty"`
}

// Credits contains cast information for a title
type Credits struct {
	Cast []CastMember `json:"cast"`
}

// Collection represents a movie collection (e.g., "The Matrix Collection")
type Collection struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Poster   *Image `json:"poster,omitempty"`
	Backdrop *Image `json:"backdrop,omitempty"`
}

// CollectionDetails contains full collection info including all movies
type CollectionDetails struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Overview string  `json:"overview,omitempty"`
	Poster   *Image  `json:"poster,omitempty"`
	Backdrop *Image  `json:"backdrop,omitempty"`
	Movies   []Title `json:"movies"`
}

// Person represents an actor/crew member with detailed info
type Person struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Biography    string `json:"biography,omitempty"`
	Birthday     string `json:"birthday,omitempty"`
	Deathday     string `json:"deathday,omitempty"`
	PlaceOfBirth string `json:"placeOfBirth,omitempty"`
	ProfileURL   string `json:"profileUrl,omitempty"`
	KnownFor     string `json:"knownFor,omitempty"` // "Acting", "Directing", etc.
}

// PersonDetails contains person info + filmography
type PersonDetails struct {
	Person      Person  `json:"person"`
	Filmography []Title `json:"filmography"`
}

// BatchSeriesDetailsRequest represents a batch request for multiple series
type BatchSeriesDetailsRequest struct {
	Queries []SeriesDetailsQuery `json:"queries"`
	Fields  []string             `json:"fields,omitempty"`
}

// BatchSeriesDetailsItem represents a single result in a batch response
type BatchSeriesDetailsItem struct {
	Query   SeriesDetailsQuery `json:"query"`
	Details *SeriesDetails     `json:"details,omitempty"`
	Error   string             `json:"error,omitempty"`
}

// BatchSeriesDetailsResponse represents the response for a batch request
type BatchSeriesDetailsResponse struct {
	Results []BatchSeriesDetailsItem `json:"results"`
}

// BatchMovieTitleFieldsRequest represents a batch request for compact movie title fields.
type BatchMovieTitleFieldsRequest struct {
	Queries []MovieDetailsQuery `json:"queries"`
	Fields  []string            `json:"fields,omitempty"`
}

// BatchMovieTitleFieldsItem represents a single compact movie title result.
type BatchMovieTitleFieldsItem struct {
	Query MovieDetailsQuery `json:"query"`
	Title *Title            `json:"title,omitempty"`
	Error string            `json:"error,omitempty"`
}

// BatchMovieTitleFieldsResponse represents the response for compact movie title fields.
type BatchMovieTitleFieldsResponse struct {
	Results []BatchMovieTitleFieldsItem `json:"results"`
}

// BatchMovieReleasesQuery represents a query for movie release data
type BatchMovieReleasesQuery struct {
	TitleID string `json:"titleId,omitempty"`
	TMDBID  int64  `json:"tmdbId,omitempty"`
	IMDBID  string `json:"imdbId,omitempty"`
}

// BatchMovieReleasesRequest represents a batch request for movie releases
type BatchMovieReleasesRequest struct {
	Queries []BatchMovieReleasesQuery `json:"queries"`
}

// BatchMovieReleasesItem represents a single result in a batch response
type BatchMovieReleasesItem struct {
	Query       BatchMovieReleasesQuery `json:"query"`
	Status      string                  `json:"status,omitempty"`
	Theatrical  *Release                `json:"theatricalRelease,omitempty"`
	HomeRelease *Release                `json:"homeRelease,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

// BatchMovieReleasesResponse represents the response for a batch releases request
type BatchMovieReleasesResponse struct {
	Results []BatchMovieReleasesItem `json:"results"`
}
