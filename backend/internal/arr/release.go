package arr

import "time"

// ReleaseType is Sonarr's release type.
type ReleaseType int

const (
	ReleaseTypeUnknown       ReleaseType = 0
	ReleaseTypeSingleEpisode ReleaseType = 1
	ReleaseTypeMultiEpisode  ReleaseType = 2
	ReleaseTypeSeasonPack    ReleaseType = 3
)

func (t ReleaseType) String() string {
	switch t {
	case ReleaseTypeSingleEpisode:
		return "singleEpisode"
	case ReleaseTypeMultiEpisode:
		return "multiEpisode"
	case ReleaseTypeSeasonPack:
		return "seasonPack"
	}
	return "unknown"
}

// Protocol is the download protocol of a release.
type Protocol string

const (
	ProtocolUnknown Protocol = ""
	ProtocolUsenet  Protocol = "usenet"
	ProtocolTorrent Protocol = "torrent"
)

var sonarrIndexerFlags = []EnumOption{
	{1, "Freeleech"}, {2, "Halfleech"}, {4, "DoubleUpload"}, {8, "Internal"}, {16, "Scene"},
	{32, "Freeleech75"}, {64, "Freeleech25"}, {128, "Nuked"}, {256, "Subtitles"},
}

var radarrIndexerFlags = []EnumOption{
	{1, "G_Freeleech"}, {2, "G_Halfleech"}, {4, "G_DoubleUpload"}, {8, "PTP_Golden"}, {16, "PTP_Approved"},
	{32, "G_Internal"}, {64, "AHD_Internal"}, {128, "G_Scene"}, {256, "G_Freeleech75"}, {512, "G_Freeleech25"},
	{1024, "AHD_UserRelease"}, {2048, "Nuked"},
}

// IndexerFlagOptions lists the indexer flags of an app.
func IndexerFlagOptions(app App) []EnumOption {
	if app == AppRadarr {
		return append([]EnumOption(nil), radarrIndexerFlags...)
	}
	return append([]EnumOption(nil), sonarrIndexerFlags...)
}

func validIndexerFlag(app App, v int) bool {
	for _, o := range IndexerFlagOptions(app) {
		if o.Value == v {
			return true
		}
	}
	return false
}

// IndexerFlagName returns the name of an indexer flag.
func IndexerFlagName(app App, v int) string {
	for _, o := range IndexerFlagOptions(app) {
		if o.Value == v {
			return o.Name
		}
	}
	return "unknown"
}

// Release holds everything custom formats and profiles evaluate. It is built
// once per search result (see Parse and BuildRelease) and then reused for
// every format and comparison.
type Release struct {
	App              App         `json:"app"`
	Title            string      `json:"title"`
	Filename         string      `json:"filename,omitempty"`
	ReleaseGroup     string      `json:"releaseGroup,omitempty"`
	Edition          string      `json:"edition,omitempty"`
	Year             int         `json:"year,omitempty"`
	Quality          Quality     `json:"quality"`
	Revision         Revision    `json:"revision"`
	Languages        []Language  `json:"languages"`
	OriginalLanguage Language    `json:"originalLanguage,omitempty"`
	ReleaseType      ReleaseType `json:"releaseType"`
	FullSeason       bool        `json:"fullSeason,omitempty"`
	EpisodeCount     int         `json:"episodeCount,omitempty"`
	Size             int64       `json:"size"`
	IndexerFlags     int         `json:"indexerFlags,omitempty"`
	Protocol         Protocol    `json:"protocol,omitempty"`
	Seeders          *int        `json:"seeders,omitempty"`
	Peers            *int        `json:"peers,omitempty"`
	PublishDate      time.Time   `json:"publishDate,omitempty"`
	RuntimeMinutes   int         `json:"runtimeMinutes,omitempty"`
	IsAnime          bool        `json:"isAnime,omitempty"`
}
