// Package dto holds the Jellyfin wire types served by the bridge.
//
// Field names, casing and enum strings must match the Jellyfin server exactly:
// the Kotlin SDK used by Android TV decodes strictly (missing non-nullable
// fields or unknown enum values fail the whole response). The reference is
// MediaBrowser.Model/Dto in the Jellyfin server repository. Nullable fields use
// omitempty; fields the SDK treats as required never do.
package dto

import (
	"encoding/json"
	"time"
)

// Jellyfin enum strings used by the bridge.
const (
	KindMovie            = "Movie"
	KindSeries           = "Series"
	KindSeason           = "Season"
	KindEpisode          = "Episode"
	KindCollectionFolder = "CollectionFolder"
	KindUserView         = "UserView"
	KindFolder           = "Folder"
	KindBoxSet           = "BoxSet"
	KindTvChannel        = "TvChannel"
	KindProgram          = "Program"
	KindVideo            = "Video"

	CollectionMovies  = "movies"
	CollectionTVShows = "tvshows"
	CollectionLiveTV  = "livetv"
	CollectionBoxSets = "boxsets"
	CollectionFolders = "folders"

	MediaTypeVideo   = "Video"
	MediaTypeUnknown = "Unknown"

	LocationFileSystem = "FileSystem"
	LocationVirtual    = "Virtual"

	ImagePrimary  = "Primary"
	ImageBackdrop = "Backdrop"
	ImageThumb    = "Thumb"
	ImageLogo     = "Logo"
)

// PublicSystemInfo is GET /System/Info/Public.
type PublicSystemInfo struct {
	LocalAddress           string `json:"LocalAddress,omitempty"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	OperatingSystem        string `json:"OperatingSystem,omitempty"`
	ID                     string `json:"Id"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// SystemInfo is GET /System/Info (authenticated).
type SystemInfo struct {
	PublicSystemInfo
	OperatingSystemDisplayName string `json:"OperatingSystemDisplayName"`
	PackageName                string `json:"PackageName"`
	HasPendingRestart          bool   `json:"HasPendingRestart"`
	IsShuttingDown             bool   `json:"IsShuttingDown"`
	SupportsLibraryMonitor     bool   `json:"SupportsLibraryMonitor"`
	WebSocketPortNumber        int    `json:"WebSocketPortNumber"`
	CompletedInstallations     []any  `json:"CompletedInstallations"`
	CanSelfRestart             bool   `json:"CanSelfRestart"`
	CanLaunchWebBrowser        bool   `json:"CanLaunchWebBrowser"`
	ProgramDataPath            string `json:"ProgramDataPath"`
	WebPath                    string `json:"WebPath"`
	ItemsByNamePath            string `json:"ItemsByNamePath"`
	CachePath                  string `json:"CachePath"`
	LogPath                    string `json:"LogPath"`
	InternalMetadataPath       string `json:"InternalMetadataPath"`
	TranscodingTempPath        string `json:"TranscodingTempPath"`
	CastReceiverApplications   []any  `json:"CastReceiverApplications"`
	HasUpdateAvailable         bool   `json:"HasUpdateAvailable"`
	EncoderLocation            string `json:"EncoderLocation"`
	SystemArchitecture         string `json:"SystemArchitecture"`
}

// BrandingOptions is GET /Branding/Configuration.
type BrandingOptions struct {
	LoginDisclaimer     *string `json:"LoginDisclaimer"`
	CustomCss           *string `json:"CustomCss"`
	SplashscreenEnabled bool    `json:"SplashscreenEnabled"`
}

// UserPolicy mirrors Jellyfin's UserPolicy. Every field is required by the SDK.
type UserPolicy struct {
	IsAdministrator                  bool     `json:"IsAdministrator"`
	IsHidden                         bool     `json:"IsHidden"`
	EnableCollectionManagement       bool     `json:"EnableCollectionManagement"`
	EnableSubtitleManagement         bool     `json:"EnableSubtitleManagement"`
	EnableLyricManagement            bool     `json:"EnableLyricManagement"`
	IsDisabled                       bool     `json:"IsDisabled"`
	BlockedTags                      []string `json:"BlockedTags"`
	AllowedTags                      []string `json:"AllowedTags"`
	EnableUserPreferenceAccess       bool     `json:"EnableUserPreferenceAccess"`
	AccessSchedules                  []any    `json:"AccessSchedules"`
	BlockUnratedItems                []string `json:"BlockUnratedItems"`
	EnableRemoteControlOfOtherUsers  bool     `json:"EnableRemoteControlOfOtherUsers"`
	EnableSharedDeviceControl        bool     `json:"EnableSharedDeviceControl"`
	EnableRemoteAccess               bool     `json:"EnableRemoteAccess"`
	EnableLiveTvManagement           bool     `json:"EnableLiveTvManagement"`
	EnableLiveTvAccess               bool     `json:"EnableLiveTvAccess"`
	EnableMediaPlayback              bool     `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding   bool     `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding   bool     `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing           bool     `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding     bool     `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeletion            bool     `json:"EnableContentDeletion"`
	EnableContentDeletionFromFolders []string `json:"EnableContentDeletionFromFolders"`
	EnableContentDownloading         bool     `json:"EnableContentDownloading"`
	EnableSyncTranscoding            bool     `json:"EnableSyncTranscoding"`
	EnableMediaConversion            bool     `json:"EnableMediaConversion"`
	EnabledDevices                   []string `json:"EnabledDevices"`
	EnableAllDevices                 bool     `json:"EnableAllDevices"`
	EnabledChannels                  []string `json:"EnabledChannels"`
	EnableAllChannels                bool     `json:"EnableAllChannels"`
	EnabledFolders                   []string `json:"EnabledFolders"`
	EnableAllFolders                 bool     `json:"EnableAllFolders"`
	InvalidLoginAttemptCount         int      `json:"InvalidLoginAttemptCount"`
	LoginAttemptsBeforeLockout       int      `json:"LoginAttemptsBeforeLockout"`
	MaxActiveSessions                int      `json:"MaxActiveSessions"`
	EnablePublicSharing              bool     `json:"EnablePublicSharing"`
	BlockedMediaFolders              []string `json:"BlockedMediaFolders"`
	BlockedChannels                  []string `json:"BlockedChannels"`
	RemoteClientBitrateLimit         int      `json:"RemoteClientBitrateLimit"`
	AuthenticationProviderID         string   `json:"AuthenticationProviderId"`
	PasswordResetProviderID          string   `json:"PasswordResetProviderId"`
	SyncPlayAccess                   string   `json:"SyncPlayAccess"`
}

// UserConfiguration mirrors Jellyfin's UserConfiguration.
type UserConfiguration struct {
	AudioLanguagePreference    string   `json:"AudioLanguagePreference,omitempty"`
	PlayDefaultAudioTrack      bool     `json:"PlayDefaultAudioTrack"`
	SubtitleLanguagePreference string   `json:"SubtitleLanguagePreference,omitempty"`
	DisplayMissingEpisodes     bool     `json:"DisplayMissingEpisodes"`
	GroupedFolders             []string `json:"GroupedFolders"`
	SubtitleMode               string   `json:"SubtitleMode"`
	DisplayCollectionsView     bool     `json:"DisplayCollectionsView"`
	EnableLocalPassword        bool     `json:"EnableLocalPassword"`
	OrderedViews               []string `json:"OrderedViews"`
	LatestItemsExcludes        []string `json:"LatestItemsExcludes"`
	MyMediaExcludes            []string `json:"MyMediaExcludes"`
	HidePlayedInLatest         bool     `json:"HidePlayedInLatest"`
	RememberAudioSelections    bool     `json:"RememberAudioSelections"`
	RememberSubtitleSelections bool     `json:"RememberSubtitleSelections"`
	EnableNextEpisodeAutoPlay  bool     `json:"EnableNextEpisodeAutoPlay"`
	CastReceiverID             string   `json:"CastReceiverId,omitempty"`
}

// UserDto is a Jellyfin user (a mediastorm profile).
type UserDto struct {
	Name                      string             `json:"Name"`
	ServerID                  string             `json:"ServerId"`
	ServerName                string             `json:"ServerName,omitempty"`
	ID                        string             `json:"Id"`
	PrimaryImageTag           string             `json:"PrimaryImageTag,omitempty"`
	HasPassword               bool               `json:"HasPassword"`
	HasConfiguredPassword     bool               `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool               `json:"HasConfiguredEasyPassword"`
	EnableAutoLogin           bool               `json:"EnableAutoLogin"`
	LastLoginDate             *time.Time         `json:"LastLoginDate,omitempty"`
	LastActivityDate          *time.Time         `json:"LastActivityDate,omitempty"`
	Configuration             *UserConfiguration `json:"Configuration,omitempty"`
	Policy                    *UserPolicy        `json:"Policy,omitempty"`
	PrimaryImageAspectRatio   *float64           `json:"PrimaryImageAspectRatio,omitempty"`
}

// QuickConnectResult is a Quick Connect request as seen by the device.
type QuickConnectResult struct {
	Authenticated bool      `json:"Authenticated"`
	Secret        string    `json:"Secret"`
	Code          string    `json:"Code"`
	DeviceID      string    `json:"DeviceId"`
	DeviceName    string    `json:"DeviceName"`
	AppName       string    `json:"AppName"`
	AppVersion    string    `json:"AppVersion"`
	DateAdded     time.Time `json:"DateAdded"`
}

// AuthenticationResult is POST /Users/AuthenticateByName (and AuthenticateWithQuickConnect).
type AuthenticationResult struct {
	User        UserDto `json:"User"`
	AccessToken string  `json:"AccessToken"`
	ServerID    string  `json:"ServerId"`
}

// UserItemData is a BaseItemDto's UserData. All non-pointer fields are required.
type UserItemData struct {
	PlayedPercentage      *float64   `json:"PlayedPercentage,omitempty"`
	UnplayedItemCount     *int       `json:"UnplayedItemCount,omitempty"`
	PlaybackPositionTicks int64      `json:"PlaybackPositionTicks"`
	PlayCount             int        `json:"PlayCount"`
	IsFavorite            bool       `json:"IsFavorite"`
	LastPlayedDate        *time.Time `json:"LastPlayedDate,omitempty"`
	Played                bool       `json:"Played"`
	Key                   string     `json:"Key"`
	ItemID                string     `json:"ItemId"`
}

// NameGuidPair is used for Studios/GenreItems.
type NameGuidPair struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

// Person is a cast/crew entry.
type Person struct {
	Name            string `json:"Name"`
	ID              string `json:"Id"`
	Role            string `json:"Role,omitempty"`
	Type            string `json:"Type"`
	PrimaryImageTag string `json:"PrimaryImageTag,omitempty"`
}

// BaseItemDto is the universal Jellyfin item.
// MarshalJSON always sends ImageTags and BackdropImageTags, empty when the
// item has no images, as Jellyfin does: the Android TV app calls
// ImageTags.containsKey without a null check and crashes on cards without it.
func (b BaseItemDto) MarshalJSON() ([]byte, error) {
	type plain BaseItemDto
	if b.ImageTags == nil {
		b.ImageTags = map[string]string{}
	}
	if b.BackdropImageTags == nil {
		b.BackdropImageTags = []string{}
	}
	return json.Marshal(plain(b))
}

type BaseItemDto struct {
	Name                    string                              `json:"Name,omitempty"`
	OriginalTitle           string                              `json:"OriginalTitle,omitempty"`
	ServerID                string                              `json:"ServerId,omitempty"`
	ID                      string                              `json:"Id"`
	Etag                    string                              `json:"Etag,omitempty"`
	DateCreated             *time.Time                          `json:"DateCreated,omitempty"`
	CanDelete               *bool                               `json:"CanDelete,omitempty"`
	PlaylistItemID          string                              `json:"PlaylistItemId,omitempty"`
	CanDownload             *bool                               `json:"CanDownload,omitempty"`
	SortName                string                              `json:"SortName,omitempty"`
	PremiereDate            *time.Time                          `json:"PremiereDate,omitempty"`
	OfficialRating          string                              `json:"OfficialRating,omitempty"`
	Overview                string                              `json:"Overview,omitempty"`
	Genres                  []string                            `json:"Genres,omitempty"`
	CommunityRating         *float64                            `json:"CommunityRating,omitempty"`
	RunTimeTicks            *int64                              `json:"RunTimeTicks,omitempty"`
	PlayAccess              string                              `json:"PlayAccess,omitempty"`
	ProductionYear          *int                                `json:"ProductionYear,omitempty"`
	IndexNumber             *int                                `json:"IndexNumber,omitempty"`
	ParentIndexNumber       *int                                `json:"ParentIndexNumber,omitempty"`
	ProviderIDs             map[string]string                   `json:"ProviderIds,omitempty"`
	IsFolder                *bool                               `json:"IsFolder,omitempty"`
	ParentID                string                              `json:"ParentId,omitempty"`
	Type                    string                              `json:"Type"`
	People                  []Person                            `json:"People,omitempty"`
	Studios                 []NameGuidPair                      `json:"Studios,omitempty"`
	GenreItems              []NameGuidPair                      `json:"GenreItems,omitempty"`
	ProductionLocations     []string                            `json:"ProductionLocations,omitempty"`
	ParentBackdropItemID    string                              `json:"ParentBackdropItemId,omitempty"`
	ParentBackdropImageTags []string                            `json:"ParentBackdropImageTags,omitempty"`
	UserData                *UserItemData                       `json:"UserData,omitempty"`
	ChildCount              *int                                `json:"ChildCount,omitempty"`
	RecursiveItemCount      *int                                `json:"RecursiveItemCount,omitempty"`
	SeriesName              string                              `json:"SeriesName,omitempty"`
	SeriesID                string                              `json:"SeriesId,omitempty"`
	SeasonID                string                              `json:"SeasonId,omitempty"`
	SeasonName              string                              `json:"SeasonName,omitempty"`
	DisplayPreferencesID    string                              `json:"DisplayPreferencesId,omitempty"`
	Status                  string                              `json:"Status,omitempty"`
	PrimaryImageAspectRatio *float64                            `json:"PrimaryImageAspectRatio,omitempty"`
	SeriesPrimaryImageTag   string                              `json:"SeriesPrimaryImageTag,omitempty"`
	CollectionType          string                              `json:"CollectionType,omitempty"`
	ImageTags               map[string]string                   `json:"ImageTags"`
	BackdropImageTags       []string                            `json:"BackdropImageTags"`
	ParentLogoItemID        string                              `json:"ParentLogoItemId,omitempty"`
	ParentLogoImageTag      string                              `json:"ParentLogoImageTag,omitempty"`
	ParentThumbItemID       string                              `json:"ParentThumbItemId,omitempty"`
	ParentThumbImageTag     string                              `json:"ParentThumbImageTag,omitempty"`
	ImageBlurHashes         map[string]any                      `json:"ImageBlurHashes,omitempty"`
	LocationType            string                              `json:"LocationType,omitempty"`
	MediaType               string                              `json:"MediaType,omitempty"`
	VideoType               string                              `json:"VideoType,omitempty"`
	Container               string                              `json:"Container,omitempty"`
	HasSubtitles            *bool                               `json:"HasSubtitles,omitempty"`
	MediaSources            []MediaSourceInfo                   `json:"MediaSources,omitempty"`
	MediaStreams            []MediaStream                       `json:"MediaStreams,omitempty"`
	Chapters                []any                               `json:"Chapters,omitempty"`
	LocalTrailerCount       *int                                `json:"LocalTrailerCount,omitempty"`
	RemoteTrailers          []MediaURL                          `json:"RemoteTrailers,omitempty"`
	Trickplay               map[string]map[string]TrickplayInfo `json:"Trickplay,omitempty"`
	ExternalURLs            []MediaURL                          `json:"ExternalUrls,omitempty"`
	Taglines                []string                            `json:"Taglines,omitempty"`

	// Live TV
	ChannelID              string       `json:"ChannelId,omitempty"`
	ChannelName            string       `json:"ChannelName,omitempty"`
	ChannelNumber          string       `json:"ChannelNumber,omitempty"`
	Number                 string       `json:"Number,omitempty"`
	ChannelPrimaryImageTag string       `json:"ChannelPrimaryImageTag,omitempty"`
	ChannelType            string       `json:"ChannelType,omitempty"`
	StartDate              *time.Time   `json:"StartDate,omitempty"`
	EndDate                *time.Time   `json:"EndDate,omitempty"`
	EpisodeTitle           string       `json:"EpisodeTitle,omitempty"`
	CurrentProgram         *BaseItemDto `json:"CurrentProgram,omitempty"`
	IsMovie                *bool        `json:"IsMovie,omitempty"`
	IsSeries               *bool        `json:"IsSeries,omitempty"`
	IsLive                 *bool        `json:"IsLive,omitempty"`
	IsNews                 *bool        `json:"IsNews,omitempty"`
	IsKids                 *bool        `json:"IsKids,omitempty"`
	IsSports               *bool        `json:"IsSports,omitempty"`
}

// MediaURL is a name/url pair (trailers, external links).
type MediaURL struct {
	Name string `json:"Name,omitempty"`
	URL  string `json:"Url"`
}

// QueryResult wraps item lists. All three fields are required by the SDK.
type QueryResult[T any] struct {
	Items            []T `json:"Items"`
	TotalRecordCount int `json:"TotalRecordCount"`
	StartIndex       int `json:"StartIndex"`
}

// NewQueryResult builds a QueryResult, never emitting a null Items array.
func NewQueryResult[T any](items []T, total, start int) QueryResult[T] {
	if items == nil {
		items = []T{}
	}
	return QueryResult[T]{Items: items, TotalRecordCount: total, StartIndex: start}
}

// VirtualFolderInfo is one library in /Library/VirtualFolders.
type VirtualFolderInfo struct {
	Name               string         `json:"Name"`
	Locations          []string       `json:"Locations"`
	CollectionType     string         `json:"CollectionType,omitempty"`
	LibraryOptions     map[string]any `json:"LibraryOptions"`
	ItemID             string         `json:"ItemId"`
	PrimaryImageItemID string         `json:"PrimaryImageItemId,omitempty"`
	RefreshProgress    *float64       `json:"RefreshProgress"`
	RefreshStatus      string         `json:"RefreshStatus"`
}

// MediaStream is one stream inside a MediaSource.
type MediaStream struct {
	Codec                  string   `json:"Codec,omitempty"`
	Language               string   `json:"Language,omitempty"`
	Title                  string   `json:"Title,omitempty"`
	DisplayTitle           string   `json:"DisplayTitle,omitempty"`
	VideoRange             string   `json:"VideoRange,omitempty"`
	VideoRangeType         string   `json:"VideoRangeType,omitempty"`
	IsInterlaced           bool     `json:"IsInterlaced"`
	BitRate                *int     `json:"BitRate,omitempty"`
	Channels               *int     `json:"Channels,omitempty"`
	ChannelLayout          string   `json:"ChannelLayout,omitempty"`
	Height                 *int     `json:"Height,omitempty"`
	Width                  *int     `json:"Width,omitempty"`
	AverageFrameRate       *float64 `json:"AverageFrameRate,omitempty"`
	RealFrameRate          *float64 `json:"RealFrameRate,omitempty"`
	Profile                string   `json:"Profile,omitempty"`
	Level                  *float64 `json:"Level,omitempty"`
	BitDepth               *int     `json:"BitDepth,omitempty"`
	PixelFormat            string   `json:"PixelFormat,omitempty"`
	ColorSpace             string   `json:"ColorSpace,omitempty"`
	ColorPrimaries         string   `json:"ColorPrimaries,omitempty"`
	ColorTransfer          string   `json:"ColorTransfer,omitempty"`
	SampleRate             *int     `json:"SampleRate,omitempty"`
	Type                   string   `json:"Type"` // Audio | Video | Subtitle
	AspectRatio            string   `json:"AspectRatio,omitempty"`
	Index                  int      `json:"Index"`
	IsDefault              bool     `json:"IsDefault"`
	IsForced               bool     `json:"IsForced"`
	IsHearingImpaired      bool     `json:"IsHearingImpaired"`
	IsExternal             bool     `json:"IsExternal"`
	IsTextSubtitleStream   bool     `json:"IsTextSubtitleStream"`
	SupportsExternalStream bool     `json:"SupportsExternalStream"`
	DeliveryMethod         string   `json:"DeliveryMethod,omitempty"` // Encode | Embed | External | Hls
	DeliveryURL            string   `json:"DeliveryUrl,omitempty"`
	Path                   string   `json:"Path,omitempty"`
}

// MediaSourceInfo describes one playable version of an item.
// MarshalJSON never sends an empty TranscodingSubProtocol: it is an enum
// (http | hls) in the Kotlin SDK, and an empty value fails decoding of the
// whole item in the Android TV app. Jellyfin's default is http.
func (m MediaSourceInfo) MarshalJSON() ([]byte, error) {
	type plain MediaSourceInfo
	if m.TranscodingSubProtocol == "" {
		m.TranscodingSubProtocol = "http"
	}
	return json.Marshal(plain(m))
}

type MediaSourceInfo struct {
	Protocol                     string            `json:"Protocol"` // File | Http
	ID                           string            `json:"Id"`
	Path                         string            `json:"Path,omitempty"`
	Type                         string            `json:"Type"` // Default
	Container                    string            `json:"Container,omitempty"`
	Size                         *int64            `json:"Size,omitempty"`
	Name                         string            `json:"Name,omitempty"`
	IsRemote                     bool              `json:"IsRemote"`
	ETag                         string            `json:"ETag,omitempty"`
	RunTimeTicks                 *int64            `json:"RunTimeTicks,omitempty"`
	ReadAtNativeFramerate        bool              `json:"ReadAtNativeFramerate"`
	IgnoreDts                    bool              `json:"IgnoreDts"`
	IgnoreIndex                  bool              `json:"IgnoreIndex"`
	GenPtsInput                  bool              `json:"GenPtsInput"`
	SupportsTranscoding          bool              `json:"SupportsTranscoding"`
	SupportsDirectStream         bool              `json:"SupportsDirectStream"`
	SupportsDirectPlay           bool              `json:"SupportsDirectPlay"`
	IsInfiniteStream             bool              `json:"IsInfiniteStream"`
	UseMostCompatibleTranscoding bool              `json:"UseMostCompatibleTranscodingProfile"`
	RequiresOpening              bool              `json:"RequiresOpening"`
	RequiresClosing              bool              `json:"RequiresClosing"`
	LiveStreamID                 string            `json:"LiveStreamId,omitempty"`
	RequiresLooping              bool              `json:"RequiresLooping"`
	SupportsProbing              bool              `json:"SupportsProbing"`
	VideoType                    string            `json:"VideoType,omitempty"`
	MediaStreams                 []MediaStream     `json:"MediaStreams"`
	MediaAttachments             []any             `json:"MediaAttachments"`
	Formats                      []string          `json:"Formats"`
	Bitrate                      *int              `json:"Bitrate,omitempty"`
	RequiredHTTPHeaders          map[string]string `json:"RequiredHttpHeaders"`
	TranscodingURL               string            `json:"TranscodingUrl,omitempty"`
	TranscodingSubProtocol       string            `json:"TranscodingSubProtocol"` // http | hls
	TranscodingContainer         string            `json:"TranscodingContainer,omitempty"`
	DefaultAudioStreamIndex      *int              `json:"DefaultAudioStreamIndex,omitempty"`
	DefaultSubtitleStreamIndex   *int              `json:"DefaultSubtitleStreamIndex,omitempty"`
	HasSegments                  bool              `json:"HasSegments"`
}

// PlaybackInfoResponse is POST /Items/{id}/PlaybackInfo.
type PlaybackInfoResponse struct {
	MediaSources  []MediaSourceInfo `json:"MediaSources"`
	PlaySessionID string            `json:"PlaySessionId,omitempty"`
	ErrorCode     string            `json:"ErrorCode,omitempty"`
}

// LiveTvInfo is GET /LiveTv/Info.
type LiveTvInfo struct {
	Services     []any    `json:"Services"`
	IsEnabled    bool     `json:"IsEnabled"`
	EnabledUsers []string `json:"EnabledUsers"`
}

// GuideInfo is GET /LiveTv/GuideInfo.
type GuideInfo struct {
	StartDate time.Time `json:"StartDate"`
	EndDate   time.Time `json:"EndDate"`
}

// SessionInfoDto is a minimal entry of GET /Sessions.
type SessionInfoDto struct {
	ID                       string    `json:"Id"`
	UserID                   string    `json:"UserId"`
	UserName                 string    `json:"UserName,omitempty"`
	Client                   string    `json:"Client,omitempty"`
	DeviceID                 string    `json:"DeviceId,omitempty"`
	DeviceName               string    `json:"DeviceName,omitempty"`
	ApplicationVersion       string    `json:"ApplicationVersion,omitempty"`
	ServerID                 string    `json:"ServerId"`
	LastActivityDate         time.Time `json:"LastActivityDate"`
	IsActive                 bool      `json:"IsActive"`
	SupportsMediaControl     bool      `json:"SupportsMediaControl"`
	SupportsRemoteControl    bool      `json:"SupportsRemoteControl"`
	HasCustomDeviceName      bool      `json:"HasCustomDeviceName"`
	PlayableMediaTypes       []string  `json:"PlayableMediaTypes"`
	SupportedCommands        []string  `json:"SupportedCommands"`
	AdditionalUsers          []any     `json:"AdditionalUsers"`
	NowPlayingQueue          []any     `json:"NowPlayingQueue"`
	NowPlayingQueueFullItems []any     `json:"NowPlayingQueueFullItems"`

	PlayState       PlayerStateInfo  `json:"PlayState"`
	NowPlayingItem  *BaseItemDto     `json:"NowPlayingItem,omitempty"`
	TranscodingInfo *TranscodingInfo `json:"TranscodingInfo,omitempty"`
}

// TrickplayInfo describes one trickplay resolution (BaseItemDto.Trickplay).
// ChapterInfo is a chapter of an item (Jellyfin's ChapterInfo).
type ChapterInfo struct {
	StartPositionTicks int64  `json:"StartPositionTicks"`
	Name               string `json:"Name"`
	ImageDateModified  string `json:"ImageDateModified"`
	ImageTag           string `json:"ImageTag,omitempty"`
}

type TrickplayInfo struct {
	Width          int `json:"Width"`
	Height         int `json:"Height"`
	TileWidth      int `json:"TileWidth"`
	TileHeight     int `json:"TileHeight"`
	ThumbnailCount int `json:"ThumbnailCount"`
	Interval       int `json:"Interval"` // milliseconds
	Bandwidth      int `json:"Bandwidth"`
}

// MediaSegmentDto is one entry of GET /MediaSegments/{itemId}.
type MediaSegmentDto struct {
	ID         string `json:"Id"`
	ItemID     string `json:"ItemId"`
	Type       string `json:"Type"` // Unknown | Commercial | Preview | Recap | Outro | Intro
	StartTicks int64  `json:"StartTicks"`
	EndTicks   int64  `json:"EndTicks"`
}

// PlayerStateInfo is SessionInfoDto.PlayState.
type PlayerStateInfo struct {
	PositionTicks       *int64 `json:"PositionTicks,omitempty"`
	CanSeek             bool   `json:"CanSeek"`
	IsPaused            bool   `json:"IsPaused"`
	IsMuted             bool   `json:"IsMuted"`
	AudioStreamIndex    *int   `json:"AudioStreamIndex,omitempty"`
	SubtitleStreamIndex *int   `json:"SubtitleStreamIndex,omitempty"`
	MediaSourceID       string `json:"MediaSourceId,omitempty"`
	PlayMethod          string `json:"PlayMethod,omitempty"` // Transcode | DirectStream | DirectPlay
	RepeatMode          string `json:"RepeatMode"`
	PlaybackOrder       string `json:"PlaybackOrder"`
}

// TranscodingInfo describes how the server prepares the stream; clients show
// it in their playback info ("Remuxing", "Direct streaming", "Transcoding").
type TranscodingInfo struct {
	AudioCodec               string   `json:"AudioCodec,omitempty"`
	VideoCodec               string   `json:"VideoCodec,omitempty"`
	Container                string   `json:"Container,omitempty"`
	IsVideoDirect            bool     `json:"IsVideoDirect"`
	IsAudioDirect            bool     `json:"IsAudioDirect"`
	AudioChannels            *int     `json:"AudioChannels,omitempty"`
	CompletionPercentage     *float64 `json:"CompletionPercentage,omitempty"`
	Framerate                *float64 `json:"Framerate,omitempty"`
	HardwareAccelerationType string   `json:"HardwareAccelerationType,omitempty"`
	TranscodeReasons         []string `json:"TranscodeReasons"`
}
