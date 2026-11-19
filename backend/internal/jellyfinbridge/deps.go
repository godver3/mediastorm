package jellyfinbridge

import (
	"context"
	"net/http"
	"time"

	"novastream/config"
	"novastream/models"
)

// The bridge depends on narrow slices of mediastorm's services so tests can
// substitute fakes. main.go passes the real services.

type AccountService interface {
	Get(id string) (models.Account, bool)
	Authenticate(username, password string) (models.Account, error)
	IsExpired(id string) bool
}

type UserService interface {
	ListAll() []models.User
	Get(id string) (models.User, bool)
	BelongsToAccount(profileID, accountID string) bool
	HasPin(id string) bool
	VerifyPin(id, pin string) error
}

type SessionService interface {
	ValidateScoped(token, scope string) (models.Session, error)
	CreateScopedWithResource(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration, scope string, scopeResource string) (models.Session, error)
	Revoke(token string) error
}

type MetadataService interface {
	Search(ctx context.Context, query string, mediaType string) ([]models.SearchResult, error)
	Trending(ctx context.Context, mediaType string) ([]models.TrendingItem, error)
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error)
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	EnrichSearchCertifications(ctx context.Context, results []models.SearchResult)
	EnrichTrendingCertifications(ctx context.Context, items []models.TrendingItem)
	EnrichTitleCertifications(ctx context.Context, titles []models.Title)
	Similar(ctx context.Context, mediaType string, tmdbID int64) ([]models.Title, error)
	PersonDetails(ctx context.Context, personID int64) (*models.PersonDetails, error)
}

// CalendarService is the per-profile release calendar (upcoming episodes).
type CalendarService interface {
	GetForHomeShelf(userID string, loc *time.Location, daysBack, daysForward, limit int) []models.CalendarItem
}

type HistoryService interface {
	ListContinueWatching(userID string) ([]models.SeriesWatchState, error)
	ListPlaybackProgress(userID string) ([]models.PlaybackProgress, error)
	ListWatchHistory(userID string) ([]models.WatchHistoryItem, error)
	UpdateWatchHistory(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error)
	UpdatePlaybackProgressContext(ctx context.Context, userID string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error)
}

type WatchlistService interface {
	List(userID string) ([]models.WatchlistItem, error)
	AddOrUpdate(userID string, input models.WatchlistUpsert) (models.WatchlistItem, error)
	Remove(userID, mediaType, id string) (bool, error)
}

type CustomListService interface {
	ListLists(userID string) ([]models.CustomList, error)
	ListItems(userID, listID string) ([]models.WatchlistItem, error)
}

type EPGService interface {
	GetScheduleMultiple(channelIDs []string, start, end time.Time) map[string][]models.EPGProgram
}

// Handlers are mediastorm HTTP handlers the bridge invokes in-process. Calling
// them (rather than re-implementing their logic) keeps access checks, stream
// limits, track selection and HLS behaviour identical to the main app.
type Handlers struct {
	Prequeue       http.HandlerFunc // POST /api/playback/prequeue
	PrequeueStatus http.HandlerFunc // GET  /api/playback/prequeue/{prequeueID}

	HLSStart          http.HandlerFunc // GET /api/video/hls/start
	LiveHLSStart      http.HandlerFunc // GET /api/live/hls/start
	HLSMaster         http.HandlerFunc // {sessionID}
	HLSPlaylist       http.HandlerFunc // {sessionID}
	HLSSegment        http.HandlerFunc // {sessionID} {segment}
	HLSSubPlaylist    http.HandlerFunc // {sessionID} {track}
	HLSSubTrack       http.HandlerFunc // {sessionID} {track}
	HLSKeepAlive      http.HandlerFunc // {sessionID}
	HLSStop           http.HandlerFunc // {sessionID}
	Stream            http.HandlerFunc // GET /api/video/stream?path=
	LiveStream        http.HandlerFunc // GET /api/live/stream?url=
	SubtitleStart     http.HandlerFunc // GET /api/video/subtitles/start
	SubtitleServe     http.HandlerFunc // {sessionID}
	LiveChannels      http.HandlerFunc // GET /api/live/channels
	ImageProxy        http.HandlerFunc // GET /api/images/proxy?url=&w=
	LocalItemPlayback http.HandlerFunc // GET /api/library/items/{itemID}/playback
	LocalLibraries    http.HandlerFunc // GET /api/library/libraries
	LocalGroups       http.HandlerFunc // GET /api/library/libraries/{libraryID}/groups
	LibraryArtwork    http.HandlerFunc // GET /api/library/items/{itemID}/artwork/{kind}
	ProbeVideo        http.HandlerFunc // GET /api/video/metadata?path= (cached ffprobe summary)
	SkipSegments      http.HandlerFunc // POST /api/video/segments (intro/recap/credits)
	ThumbnailsStart   http.HandlerFunc // POST /api/video/thumbnails/start?path=
	IndexerSearch     http.HandlerFunc // GET /api/indexers/search (ranked releases)
	PlaybackResolve   http.HandlerFunc // POST /api/playback/resolve
	PlaybackQueue     http.HandlerFunc // GET /api/playback/queue/{queueID}
	// Recordings (Live TV DVR, see dvr.go).
	RecordingsList      http.HandlerFunc // GET    /api/live/recordings?profileId=
	RecordingCreateEPG  http.HandlerFunc // POST   /api/live/recordings/epg
	RecordingCancel     http.HandlerFunc // POST   /api/live/recordings/{recordingID}/cancel
	RecordingDelete     http.HandlerFunc // DELETE /api/live/recordings/{recordingID}
	RecordingRules      http.HandlerFunc // GET    /api/live/recording-rules?profileId=
	RecordingRuleCreate http.HandlerFunc // POST   /api/live/recording-rules
	RecordingRuleDelete http.HandlerFunc // DELETE /api/live/recording-rules/{ruleID}
	RecordingRuleUpdate http.HandlerFunc // PUT    /api/live/recording-rules/{ruleID}
	RecordingSettings   http.HandlerFunc // GET    /api/live/recording-settings?profileId=
}

// Deps wires the bridge to mediastorm.
type Deps struct {
	CacheDir    string
	Accounts    AccountService
	Users       UserService
	Sessions    SessionService
	Metadata    MetadataService
	History     HistoryService
	Watchlist   WatchlistService
	CustomLists CustomListService
	WatchRooms  WatchRoomService // backs SyncPlay (nil: no SyncPlay)
	EPG         EPGService
	Calendar    CalendarService
	Handlers    Handlers
	// EPGTimeOffset returns the guide time correction for a profile (global
	// Live.EPG.TimeOffsetMinutes with the per-profile override applied).
	EPGTimeOffset func(profileID string) time.Duration
	// ProfilePinsEnforced reports the security.enforceProfilePins setting;
	// when on, PIN-locked profiles need their PIN to sign in.
	ProfilePinsEnforced func() bool
	// SubtitleDone reports whether a subtitle extraction session has finished.
	SubtitleDone func(sessionID string) bool
	// Settings returns the current bridge settings, so changes in the admin
	// UI apply without a restart (listener address and port excepted).
	Settings func() (config.JellyfinBridgeSettings, error)
	// StreamPlan reports what a mediastorm HLS session copies or encodes
	// (false until its FFmpeg run is planned).
	StreamPlan func(hlsSessionID string) (StreamPlan, bool)
	// ThumbnailsEnabled reports playback.thumbnails.enabled; ThumbnailFrames
	// lists the seek thumbnails generated so far for a stream path and whether
	// generation finished. Both back trickplay.
	ThumbnailsEnabled func() bool
	ThumbnailFrames   func(streamPath string) ([]ThumbnailFrame, bool)
	// StopRequested reports (and consumes) a stop the Active Streams
	// dashboard requested for a profile's playback of an item.
	StopRequested func(profileID string, update models.PlaybackProgressUpdate) bool
	// RankingGeneration changes whenever mediastorm's release ranking changes
	// (ranking settings); cached picks and play sessions from an
	// older generation are not reused.
	RankingGeneration func() uint64
}

// StreamPlan is the output plan of a mediastorm HLS session, as shown in the
// clients' playback info.
type StreamPlan struct {
	Live             bool
	SourceVideoCodec string
	SourcePixFmt     string
	SourceHDR        bool
	SourceDV         bool
	SourceAudioCodec string
	VideoCodec       string
	VideoCopied      bool
	Encoder          string // "copy", "libx264" or a hardware kind (nvenc, qsv, vaapi, videotoolbox)
	HardwareEncode   bool
	AudioCodec       string
	AudioCopied      bool
	AudioChannels    int
	// CompletionPercentage is how far the output reaches into the title.
	CompletionPercentage float64
	// EncodeFPS is the current run's output frames per second.
	EncodeFPS float64
	// Container is ts or mp4; VideoRange SDR, PQ or HLG as delivered.
	Container  string
	VideoRange string
}
