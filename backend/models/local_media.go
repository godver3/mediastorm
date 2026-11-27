package models

import "time"

const (
	LibraryAccessModeAll        = "all"
	LibraryAccessModeRestricted = "restricted"
)

// LibraryAccessPolicy controls which mediastorm households and profiles may
// discover and play a configured local, Plex, or Jellyfin library.
type LibraryAccessPolicy struct {
	LibraryID         string   `json:"libraryId"`
	AccessMode        string   `json:"accessMode"`
	AllowedAccountIDs []string `json:"allowedAccountIds"`
	AllowedProfileIDs []string `json:"allowedProfileIds"`
}

type LocalMediaLibraryType string

const (
	LocalMediaLibraryTypeMovie LocalMediaLibraryType = "movie"
	LocalMediaLibraryTypeShow  LocalMediaLibraryType = "show"
	LocalMediaLibraryTypeOther LocalMediaLibraryType = "other"
)

type LocalMediaMatchStatus string

const (
	LocalMediaMatchStatusMatched       LocalMediaMatchStatus = "matched"
	LocalMediaMatchStatusLowConfidence LocalMediaMatchStatus = "low_confidence"
	LocalMediaMatchStatusUnmatched     LocalMediaMatchStatus = "unmatched"
	LocalMediaMatchStatusManual        LocalMediaMatchStatus = "manual"
)

type LocalMediaScanStatus string

const (
	LocalMediaScanStatusIdle     LocalMediaScanStatus = "idle"
	LocalMediaScanStatusScanning LocalMediaScanStatus = "scanning"
	LocalMediaScanStatusComplete LocalMediaScanStatus = "complete"
	LocalMediaScanStatusFailed   LocalMediaScanStatus = "failed"
)

type LocalMediaLibrary struct {
	ID                 string                `json:"id"`
	Name               string                `json:"name"`
	Type               LocalMediaLibraryType `json:"type"`
	RootPath           string                `json:"rootPath"`
	RootPaths          []string              `json:"rootPaths"`
	FilterOutTerms     []string              `json:"filterOutTerms,omitempty"`
	MinFileSizeBytes   int64                 `json:"minFileSizeBytes,omitempty"`
	CreatedAt          time.Time             `json:"createdAt"`
	UpdatedAt          time.Time             `json:"updatedAt"`
	LastScanStartedAt  *time.Time            `json:"lastScanStartedAt,omitempty"`
	LastScanFinishedAt *time.Time            `json:"lastScanFinishedAt,omitempty"`
	LastScanStatus     LocalMediaScanStatus  `json:"lastScanStatus"`
	LastScanError      string                `json:"lastScanError,omitempty"`
	LastScanDiscovered int                   `json:"lastScanDiscovered"`
	LastScanTotal      int                   `json:"lastScanTotal"`
	LastScanMatched    int                   `json:"lastScanMatched"`
	LastScanLowConf    int                   `json:"lastScanLowConfidence"`
	SourceType         string                `json:"sourceType"`
	SourceName         string                `json:"sourceName"`
	SourceServerName   string                `json:"sourceServerName,omitempty"`
	Access             *LibraryAccessPolicy  `json:"access,omitempty"`
}

type LocalMediaProbe struct {
	FormatName      string   `json:"formatName,omitempty"`
	DurationSeconds float64  `json:"durationSeconds,omitempty"`
	SizeBytes       int64    `json:"sizeBytes,omitempty"`
	VideoCodec      string   `json:"videoCodec,omitempty"`
	Width           int      `json:"width,omitempty"`
	Height          int      `json:"height,omitempty"`
	HDRFormat       string   `json:"hdrFormat,omitempty"`
	AudioCodecs     []string `json:"audioCodecs,omitempty"`
	SubtitleCodecs  []string `json:"subtitleCodecs,omitempty"`
	AudioStreams    int      `json:"audioStreams,omitempty"`
	SubtitleStreams int      `json:"subtitleStreams,omitempty"`
	Interlaced      bool     `json:"interlaced,omitempty"` // interlaced video (1080i, 576i)
	// Version is the probe generation (localmedia.localMediaProbeVersion): an older probe of an
	// unchanged file is redone on the next scan to fill fields added since.
	Version int `json:"version,omitempty"`
}

type LocalMediaExternalIDs struct {
	IMDB string `json:"imdb,omitempty"`
	TMDB string `json:"tmdb,omitempty"`
	TVDB string `json:"tvdb,omitempty"`
}

type LocalMediaItem struct {
	ID               string                 `json:"id"`
	LibraryID        string                 `json:"libraryId"`
	RelativePath     string                 `json:"relativePath"`
	FilePath         string                 `json:"-"`
	FileName         string                 `json:"fileName"`
	LibraryType      LocalMediaLibraryType  `json:"libraryType"`
	DetectedTitle    string                 `json:"detectedTitle,omitempty"`
	DetectedYear     int                    `json:"detectedYear,omitempty"`
	SeasonNumber     int                    `json:"seasonNumber,omitempty"`
	EpisodeNumber    int                    `json:"episodeNumber,omitempty"`
	Confidence       float64                `json:"confidence"`
	MatchStatus      LocalMediaMatchStatus  `json:"matchStatus"`
	MatchedTitleID   string                 `json:"matchedTitleId,omitempty"`
	MatchedMediaType string                 `json:"matchedMediaType,omitempty"`
	MatchedName      string                 `json:"matchedName,omitempty"`
	MatchedYear      int                    `json:"matchedYear,omitempty"`
	IsMissing        bool                   `json:"isMissing,omitempty"`
	MissingSince     *time.Time             `json:"missingSince,omitempty"`
	ExternalIDs      *LocalMediaExternalIDs `json:"externalIds,omitempty"`
	Metadata         *Title                 `json:"metadata,omitempty"`
	EpisodeTitle     string                 `json:"episodeTitle,omitempty"`
	EpisodeOverview  string                 `json:"episodeOverview,omitempty"`
	EpisodeImage     *Image                 `json:"episodeImage,omitempty"`
	Probe            *LocalMediaProbe       `json:"probe,omitempty"`
	SizeBytes        int64                  `json:"sizeBytes"`
	ModifiedAt       *time.Time             `json:"modifiedAt,omitempty"`
	LastScannedAt    *time.Time             `json:"lastScannedAt,omitempty"`
	LastSeenScanID   string                 `json:"-"`
	CreatedAt        time.Time              `json:"createdAt"`
	UpdatedAt        time.Time              `json:"updatedAt"`
	SourceType       string                 `json:"sourceType"`
	SourceName       string                 `json:"sourceName"`
	SourceServerName string                 `json:"sourceServerName,omitempty"`
	VersionLabel     string                 `json:"versionLabel,omitempty"`
}

type LocalMediaItemListQuery struct {
	Filter         string `json:"filter"`
	Sort           string `json:"sort"`
	Dir            string `json:"dir"`
	Query          string `json:"query"`
	MediaType      string `json:"mediaType"`
	Alphabet       string `json:"alphabet"`
	Limit          int    `json:"limit"`
	Offset         int    `json:"offset"`
	IncludeMissing bool   `json:"includeMissing"`
	IncludeCards   bool   `json:"includeCards"`
	// Kids profile rating caps. When set, groups whose certification exceeds
	// the cap for their media type (or have no known certification) are removed.
	MaxMovieRating string `json:"-"`
	MaxTVRating    string `json:"-"`
}

type LocalMediaItemListResult struct {
	Items  []LocalMediaItem `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

type LocalMediaSeasonGroup struct {
	ID               string                   `json:"id"`
	SeasonNumber     int                      `json:"seasonNumber"`
	ItemCount        int                      `json:"itemCount"`
	MissingCount     int                      `json:"missingCount"`
	MatchStatus      LocalMediaMatchStatus    `json:"matchStatus"`
	ConfidenceMin    float64                  `json:"confidenceMin"`
	ConfidenceMax    float64                  `json:"confidenceMax"`
	TotalSizeBytes   int64                    `json:"totalSizeBytes"`
	LatestModifiedAt *time.Time               `json:"latestModifiedAt,omitempty"`
	LatestUpdatedAt  *time.Time               `json:"latestUpdatedAt,omitempty"`
	Episodes         []LocalMediaEpisodeGroup `json:"episodes"`
}

type LocalMediaEpisodeGroup struct {
	ID               string                `json:"id"`
	EpisodeNumber    int                   `json:"episodeNumber"`
	EpisodeTitle     string                `json:"episodeTitle,omitempty"`
	EpisodeOverview  string                `json:"episodeOverview,omitempty"`
	EpisodeImage     *Image                `json:"episodeImage,omitempty"`
	ItemCount        int                   `json:"itemCount"`
	MissingCount     int                   `json:"missingCount"`
	MatchStatus      LocalMediaMatchStatus `json:"matchStatus"`
	ConfidenceMin    float64               `json:"confidenceMin"`
	ConfidenceMax    float64               `json:"confidenceMax"`
	TotalSizeBytes   int64                 `json:"totalSizeBytes"`
	LatestModifiedAt *time.Time            `json:"latestModifiedAt,omitempty"`
	LatestUpdatedAt  *time.Time            `json:"latestUpdatedAt,omitempty"`
	Items            []LocalMediaItem      `json:"items"`
}

type LocalMediaItemGroup struct {
	ID               string                  `json:"id"`
	GroupType        string                  `json:"groupType"`
	LibraryType      LocalMediaLibraryType   `json:"libraryType"`
	Title            string                  `json:"title"`
	Overview         string                  `json:"overview,omitempty"`
	Certification    string                  `json:"certification,omitempty"` // MPAA/TV content rating from matched metadata
	Year             int                     `json:"year,omitempty"`
	TMDBID           int64                   `json:"tmdbId,omitempty"`
	TVDBID           int64                   `json:"tvdbId,omitempty"`
	IMDBID           string                  `json:"imdbId,omitempty"`
	Poster           *Image                  `json:"poster,omitempty"`
	TextPoster       *Image                  `json:"textPoster,omitempty"`
	Backdrop         *Image                  `json:"backdrop,omitempty"`
	TextBackdrop     *Image                  `json:"textBackdrop,omitempty"`
	ItemCount        int                     `json:"itemCount"`
	MissingCount     int                     `json:"missingCount"`
	MatchStatus      LocalMediaMatchStatus   `json:"matchStatus"`
	ConfidenceMin    float64                 `json:"confidenceMin"`
	ConfidenceMax    float64                 `json:"confidenceMax"`
	TotalSizeBytes   int64                   `json:"totalSizeBytes"`
	LatestModifiedAt *time.Time              `json:"latestModifiedAt,omitempty"`
	LatestUpdatedAt  *time.Time              `json:"latestUpdatedAt,omitempty"`
	LatestCreatedAt  *time.Time              `json:"latestCreatedAt,omitempty"`
	Items            []LocalMediaItem        `json:"items,omitempty"`
	Seasons          []LocalMediaSeasonGroup `json:"seasons,omitempty"`
}

type LocalMediaGroupListResult struct {
	Groups          []LocalMediaItemGroup `json:"groups"`
	Total           int                   `json:"total"`
	Limit           int                   `json:"limit"`
	Offset          int                   `json:"offset"`
	AlphabetBuckets []string              `json:"alphabetBuckets,omitempty"`
}

type LocalMediaMatchQuery struct {
	MediaType string `json:"mediaType"`
	TitleID   string `json:"titleId,omitempty"`
	Title     string `json:"title,omitempty"`
	Year      int    `json:"year,omitempty"`
	IMDBID    string `json:"imdbId,omitempty"`
	TMDBID    string `json:"tmdbId,omitempty"`
	TVDBID    string `json:"tvdbId,omitempty"`
}

type LocalMediaMatchedGroup struct {
	LibraryID   string                `json:"libraryId"`
	LibraryName string                `json:"libraryName"`
	LibraryType LocalMediaLibraryType `json:"libraryType"`
	Group       LocalMediaItemGroup   `json:"group"`
}

type LocalMediaScanSummary struct {
	Discovered    int    `json:"discovered"`
	Matched       int    `json:"matched"`
	LowConfidence int    `json:"lowConfidence"`
	Unmatched     int    `json:"unmatched"`
	Status        string `json:"status,omitempty"`
	// Async is true when the scan was accepted and is running in the background.
	// Poll library lastScan* fields for progress and completion.
	Async bool `json:"async,omitempty"`
}

type LocalMediaLibraryCreateInput struct {
	Name             string                `json:"name"`
	Type             LocalMediaLibraryType `json:"type"`
	RootPath         string                `json:"rootPath"`
	RootPaths        []string              `json:"rootPaths"`
	FilterOutTerms   []string              `json:"filterOutTerms"`
	MinFileSizeBytes int64                 `json:"minFileSizeBytes"`
}

type LocalMediaMatchInput struct {
	MatchedTitleID   string                `json:"matchedTitleId"`
	MatchedMediaType string                `json:"matchedMediaType"`
	MatchedName      string                `json:"matchedName"`
	MatchedYear      int                   `json:"matchedYear"`
	Confidence       float64               `json:"confidence"`
	MatchStatus      LocalMediaMatchStatus `json:"matchStatus"`
	Metadata         *Title                `json:"metadata,omitempty"`
}

type LocalMediaDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type LocalMediaDirectoryListing struct {
	CurrentPath string                     `json:"currentPath"`
	ParentPath  string                     `json:"parentPath,omitempty"`
	Entries     []LocalMediaDirectoryEntry `json:"entries"`
}

type LocalMediaPlaybackResponse struct {
	ItemID          string            `json:"itemId"`
	FileName        string            `json:"fileName"`
	DisplayName     string            `json:"displayName"`
	TitleID         string            `json:"titleId,omitempty"`
	Title           string            `json:"title,omitempty"`
	SeriesTitle     string            `json:"seriesTitle,omitempty"`
	EpisodeTitle    string            `json:"episodeTitle,omitempty"`
	Year            int               `json:"year,omitempty"`
	DurationSeconds float64           `json:"durationSeconds,omitempty"`
	PosterURL       string            `json:"posterUrl,omitempty"`
	BackdropURL     string            `json:"backdropUrl,omitempty"`
	EpisodeImage    string            `json:"episodeImageUrl,omitempty"`
	ExternalIDs     map[string]string `json:"externalIds,omitempty"`
	StreamPath      string            `json:"streamPath"`
	StreamURL       string            `json:"streamUrl"`
	HLSStartURL     string            `json:"hlsStartUrl,omitempty"`
	DirectStream    bool              `json:"directStream"`
	HLSAvailable    bool              `json:"hlsAvailable"`
	SourceType      string            `json:"sourceType"`
	SourceName      string            `json:"sourceName"`
}

const (
	MediaSourceLocal    = "local"
	MediaSourcePlex     = "plex"
	MediaSourceJellyfin = "jellyfin"
)

// RemoteMediaLibrary is a configured Plex or Jellyfin library mirrored into
// PostgreSQL. Provider credentials remain in settings and are referenced by ID.
type RemoteMediaLibrary struct {
	ID                 string                `json:"id"`
	Name               string                `json:"name"`
	Type               LocalMediaLibraryType `json:"type"`
	Provider           string                `json:"provider"`
	AccountID          string                `json:"accountId"`
	ServerID           string                `json:"serverId,omitempty"`
	ServerName         string                `json:"serverName,omitempty"`
	ServerURL          string                `json:"serverUrl,omitempty"`
	ExternalLibraryID  string                `json:"externalLibraryId"`
	CreatedAt          time.Time             `json:"createdAt"`
	UpdatedAt          time.Time             `json:"updatedAt"`
	LastSyncStartedAt  *time.Time            `json:"lastSyncStartedAt,omitempty"`
	LastSyncFinishedAt *time.Time            `json:"lastSyncFinishedAt,omitempty"`
	LastSyncStatus     LocalMediaScanStatus  `json:"lastSyncStatus"`
	LastSyncError      string                `json:"lastSyncError,omitempty"`
	LastSyncTotal      int                   `json:"lastSyncTotal"`
}

type RemoteMediaLibraryCreateInput struct {
	Name              string                `json:"name"`
	Type              LocalMediaLibraryType `json:"type"`
	Provider          string                `json:"provider"`
	AccountID         string                `json:"accountId"`
	ServerID          string                `json:"serverId,omitempty"`
	ServerName        string                `json:"serverName,omitempty"`
	ServerURL         string                `json:"serverUrl,omitempty"`
	ExternalLibraryID string                `json:"externalLibraryId"`
}

// RemoteMediaItem represents one playable provider version. Multiple rows may
// share GroupKey (or GroupKey/season/episode) and become version choices.
type RemoteMediaItem struct {
	ID              string                 `json:"id"`
	LibraryID       string                 `json:"libraryId"`
	ExternalItemID  string                 `json:"externalItemId"`
	ExternalMediaID string                 `json:"externalMediaId,omitempty"`
	GroupKey        string                 `json:"groupKey"`
	LibraryType     LocalMediaLibraryType  `json:"libraryType"`
	Title           string                 `json:"title"`
	Year            int                    `json:"year,omitempty"`
	Overview        string                 `json:"overview,omitempty"`
	Certification   string                 `json:"certification,omitempty"`
	SeasonNumber    int                    `json:"seasonNumber,omitempty"`
	EpisodeNumber   int                    `json:"episodeNumber,omitempty"`
	EpisodeTitle    string                 `json:"episodeTitle,omitempty"`
	ExternalIDs     *LocalMediaExternalIDs `json:"externalIds,omitempty"`
	PosterURL       string                 `json:"posterUrl,omitempty"`
	BackdropURL     string                 `json:"backdropUrl,omitempty"`
	EpisodeImageURL string                 `json:"episodeImageUrl,omitempty"`
	FileName        string                 `json:"fileName,omitempty"`
	VersionLabel    string                 `json:"versionLabel,omitempty"`
	Container       string                 `json:"container,omitempty"`
	VideoCodec      string                 `json:"videoCodec,omitempty"`
	AudioCodec      string                 `json:"audioCodec,omitempty"`
	Width           int                    `json:"width,omitempty"`
	Height          int                    `json:"height,omitempty"`
	HDRFormat       string                 `json:"hdrFormat,omitempty"`
	DurationSeconds float64                `json:"durationSeconds,omitempty"`
	SizeBytes       int64                  `json:"sizeBytes,omitempty"`
	StreamPath      string                 `json:"streamPath"`
	// ProviderData holds provider-specific stream keys (e.g. Plex partKey). Must be
	// serialized so backup/restore does not wipe playback paths (json:"-" did that).
	ProviderData   map[string]string `json:"providerData,omitempty"`
	LastSeenSyncID string            `json:"-"`
	IsMissing      bool              `json:"isMissing,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}
