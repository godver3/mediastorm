package models

// Basic metadata structures for titles and images.

type Image struct {
	URL    string `json:"url"`
	Type   string `json:"type"` // poster, backdrop, logo
	Width  int    `json:"width"`
	Height int    `json:"height"`
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
}

// Rating represents a single rating from a source
type Rating struct {
	Source string  `json:"source"` // imdb, tmdb, trakt, letterboxd, tomatoes, audience, metacritic
	Value  float64 `json:"value"`  // Rating value (scale varies by source)
	Max    float64 `json:"max"`    // Maximum possible value (e.g., 10 for IMDB, 100 for RT)
}

type Title struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	OriginalName    string    `json:"originalName,omitempty"`
	AlternateTitles []string  `json:"alternateTitles,omitempty"`
	Overview        string    `json:"overview"`
	Year            int       `json:"year"`
	Language        string    `json:"language"`
	Poster          *Image    `json:"poster,omitempty"`
	Backdrop        *Image    `json:"backdrop,omitempty"`
	MediaType       string    `json:"mediaType"` // series | movie
	TVDBID          int64     `json:"tvdbId,omitempty"`
	IMDBID          string    `json:"imdbId,omitempty"`
	TMDBID          int64     `json:"tmdbId,omitempty"`
	Popularity      float64   `json:"popularity,omitempty"`
	Network         string    `json:"network,omitempty"`
	Status          string    `json:"status,omitempty"` // For series: Continuing, Ended, Upcoming, etc.
	PrimaryTrailer  *Trailer  `json:"primaryTrailer,omitempty"`
	Trailers        []Trailer `json:"trailers,omitempty"`
	Releases        []Release `json:"releases,omitempty"`
	Theatrical      *Release  `json:"theatricalRelease,omitempty"`
	HomeRelease     *Release  `json:"homeRelease,omitempty"`
	Ratings         []Rating  `json:"ratings,omitempty"` // Aggregated ratings from MDBList
	Credits         *Credits  `json:"credits,omitempty"` // Top billed cast
}

type TrendingItem struct {
	Rank  int   `json:"rank"`
	Title Title `json:"title"`
}

type SearchResult struct {
	Title Title `json:"title"`
	Score int   `json:"score"`
}

type SeriesEpisode struct {
	ID            string `json:"id"`
	TVDBID        int64  `json:"tvdbId,omitempty"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	AiredDate     string `json:"airedDate,omitempty"`
	Runtime       int    `json:"runtimeMinutes,omitempty"`
	Image         *Image `json:"image,omitempty"`
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
	Title   Title          `json:"title"`
	Seasons []SeriesSeason `json:"seasons"`
}

type SeriesDetailsQuery struct {
	TitleID string
	Name    string
	Year    int
	TVDBID  int64
	TMDBID  int64
}

type TrailerQuery struct {
	MediaType string
	TitleID   string
	Name      string
	Year      int
	IMDBID    string
	TMDBID    int64
	TVDBID    int64
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

// BatchSeriesDetailsRequest represents a batch request for multiple series
type BatchSeriesDetailsRequest struct {
	Queries []SeriesDetailsQuery `json:"queries"`
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
	Theatrical  *Release                `json:"theatricalRelease,omitempty"`
	HomeRelease *Release                `json:"homeRelease,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

// BatchMovieReleasesResponse represents the response for a batch releases request
type BatchMovieReleasesResponse struct {
	Results []BatchMovieReleasesItem `json:"results"`
}
