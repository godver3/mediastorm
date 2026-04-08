package models

import "time"

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
}

type LocalMediaItemListQuery struct {
	Filter         string `json:"filter"`
	Sort           string `json:"sort"`
	Dir            string `json:"dir"`
	Query          string `json:"query"`
	Limit          int    `json:"limit"`
	Offset         int    `json:"offset"`
	IncludeMissing bool   `json:"includeMissing"`
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
	Year             int                     `json:"year,omitempty"`
	Poster           *Image                  `json:"poster,omitempty"`
	TextPoster       *Image                  `json:"textPoster,omitempty"`
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
	Groups []LocalMediaItemGroup `json:"groups"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
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
	Discovered    int `json:"discovered"`
	Matched       int `json:"matched"`
	LowConfidence int `json:"lowConfidence"`
	Unmatched     int `json:"unmatched"`
}

type LocalMediaLibraryCreateInput struct {
	Name             string                `json:"name"`
	Type             LocalMediaLibraryType `json:"type"`
	RootPath         string                `json:"rootPath"`
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
	ItemID       string `json:"itemId"`
	FileName     string `json:"fileName"`
	DisplayName  string `json:"displayName"`
	StreamPath   string `json:"streamPath"`
	StreamURL    string `json:"streamUrl"`
	HLSStartURL  string `json:"hlsStartUrl,omitempty"`
	DirectStream bool   `json:"directStream"`
	HLSAvailable bool   `json:"hlsAvailable"`
}
