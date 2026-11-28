package datastore

import (
	"context"
	"time"

	"novastream/models"
)

// AccountRepository manages account persistence.
type AccountRepository interface {
	Get(ctx context.Context, id string) (*models.Account, error)
	GetByUsername(ctx context.Context, username string) (*models.Account, error)
	List(ctx context.Context) ([]models.Account, error)
	Create(ctx context.Context, acct *models.Account) error
	Update(ctx context.Context, acct *models.Account) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// NumbersStationRepository stores account-level puzzle progress.
type NumbersStationRepository interface {
	Get(ctx context.Context, accountID string) (*models.NumbersStationProgress, error)
	List(ctx context.Context) ([]models.NumbersStationProgress, error)
	Upsert(ctx context.Context, progress *models.NumbersStationProgress) error
	Advance(ctx context.Context, accountID string, expectedStage, nextStage int, completed bool, now time.Time) (bool, error)
}

// UserRepository manages user profile persistence.
type UserRepository interface {
	Get(ctx context.Context, id string) (*models.User, error)
	ListByAccount(ctx context.Context, accountID string) ([]models.User, error)
	List(ctx context.Context) ([]models.User, error)
	Create(ctx context.Context, user *models.User) error
	Update(ctx context.Context, user *models.User) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// NotificationRepository manages profile notification destinations and release observations.
type NotificationRepository interface {
	GetChannel(ctx context.Context, id string) (*models.NotificationChannel, error)
	ListChannels(ctx context.Context, profileID string) ([]models.NotificationChannel, error)
	ListAllChannels(ctx context.Context) ([]models.NotificationChannel, error)
	CreateChannel(ctx context.Context, channel *models.NotificationChannel) error
	UpdateChannel(ctx context.Context, channel *models.NotificationChannel) error
	DeleteChannel(ctx context.Context, profileID, id string) error
	GetObservation(ctx context.Context, profileID, itemKey string) (*models.NotificationObservation, error)
	ListObservations(ctx context.Context, profileID string) ([]models.NotificationObservation, error)
	ListAllObservations(ctx context.Context) ([]models.NotificationObservation, error)
	UpsertObservation(ctx context.Context, observation *models.NotificationObservation) error
	GetProgressMessage(ctx context.Context, channelID, playbackKey string) (*models.NotificationProgressMessage, error)
	ListProgressMessages(ctx context.Context) ([]models.NotificationProgressMessage, error)
	ListProgressMessagesByPlayback(ctx context.Context, profileID, playbackKey string) ([]models.NotificationProgressMessage, error)
	UpsertProgressMessage(ctx context.Context, message *models.NotificationProgressMessage) error
	TouchProgressMessages(ctx context.Context, profileID, playbackKey string, updatedAt time.Time) error
	DeleteProgressMessage(ctx context.Context, channelID, playbackKey string) error
}

// SessionRepository manages session persistence.
type SessionRepository interface {
	Get(ctx context.Context, token string) (*models.Session, error)
	List(ctx context.Context) ([]models.Session, error)
	ListByAccount(ctx context.Context, accountID string) ([]models.Session, error)
	Create(ctx context.Context, sess *models.Session) error
	Update(ctx context.Context, sess *models.Session) error
	Delete(ctx context.Context, token string) error
	DeleteByAccount(ctx context.Context, accountID string) error
	DeleteByAccountExcept(ctx context.Context, accountID, keepToken string) error
	DeleteExpired(ctx context.Context) (int64, error)
	Count(ctx context.Context) (int64, error)
}

// HistorySyncSnapshotRepository persists the remote history keys each
// bidirectional history sync last saw, keyed by task/provider/account/profile.
type HistorySyncSnapshotRepository interface {
	Get(ctx context.Context, key string) (*models.HistorySyncSnapshot, error)
	Put(ctx context.Context, snapshot models.HistorySyncSnapshot) error
}

// UserCalendarRepository persists each profile's last built calendar
// (kind "full" or "lite") so restarts can serve it immediately.
type UserCalendarRepository interface {
	List(ctx context.Context) ([]models.UserCalendarSnapshot, error)
	Put(ctx context.Context, snap models.UserCalendarSnapshot) error
	MarkStale(ctx context.Context, userID string) error
}

// ProfileUnlockRepository persists server-issued profile PIN unlock grants.
type ProfileUnlockRepository interface {
	Create(ctx context.Context, unlock *models.ProfileUnlock) error
	List(ctx context.Context) ([]models.ProfileUnlock, error)
	DeleteBySession(ctx context.Context, sessionHash string) error
	DeleteByProfile(ctx context.Context, profileID string) error
	RebindSession(ctx context.Context, oldSessionHash, newSessionHash string, sessionExpiresAt time.Time) error
	DeleteExpired(ctx context.Context) (int64, error)
}

// InvitationRepository manages invitation persistence.
type InvitationRepository interface {
	Get(ctx context.Context, id string) (*models.Invitation, error)
	GetByToken(ctx context.Context, token string) (*models.Invitation, error)
	List(ctx context.Context) ([]models.Invitation, error)
	Create(ctx context.Context, inv *models.Invitation) error
	Update(ctx context.Context, inv *models.Invitation) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// ShareLinkRepository manages shareable playback link persistence.
type ShareLinkRepository interface {
	Create(ctx context.Context, link *models.ShareLink) error
	Get(ctx context.Context, token string) (*models.ShareLink, error)
	ListAll(ctx context.Context) ([]models.ShareLink, error)
	// ConsumeUse atomically records one use of a link, returning the updated
	// record only if the link is active, unexpired, and under its use cap.
	// Returns (nil, nil) when the link is not usable.
	ConsumeUse(ctx context.Context, token string, now time.Time) (*models.ShareLink, error)
	SetActive(ctx context.Context, token string, active bool) error
	Delete(ctx context.Context, token string) error
	DeleteAll(ctx context.Context) error
	DeleteExpired(ctx context.Context, now time.Time) error
}

// WatchRoomRepository manages persistent native watch-together rooms.
type WatchRoomRepository interface {
	Create(ctx context.Context, room *models.WatchRoom, inviteeProfileIDs []string, clientID string, capabilities models.WatchRoomClientCapabilities) error
	Get(ctx context.Context, roomID string) (*models.WatchRoom, error)
	ListInvitations(ctx context.Context, profileID string, now time.Time) ([]models.WatchRoom, error)
	IsInvited(ctx context.Context, roomID, profileID string) (bool, error)
	Join(ctx context.Context, roomID, profileID, clientID string, capabilities models.WatchRoomClientCapabilities, now time.Time) error
	SetReady(ctx context.Context, roomID, profileID string, ready bool, now time.Time) error
	UpdateState(ctx context.Context, roomID, profileID, status string, position, duration float64, expectedRevision *int64, episode *models.WatchRoomEpisode, now time.Time) (bool, error)
	Heartbeat(ctx context.Context, roomID, profileID, clientID string, buffering bool, now time.Time) error
	Leave(ctx context.Context, roomID, profileID string) error
	End(ctx context.Context, roomID, profileID string, now time.Time) (bool, error)
	EndExpired(ctx context.Context, roomID string, now time.Time) error
	Sweep(ctx context.Context, now, disconnectCutoff, deleteBefore time.Time) (ended int64, deleted int64, err error)
	CreateAccountInvite(ctx context.Context, invite *models.WatchRoomAccountInvite) (bool, error)
	ListAccountInvites(ctx context.Context, accountID string, now time.Time) ([]models.WatchRoomAccountInvite, error)
	ListRoomAccountInvites(ctx context.Context, roomID string, now time.Time) ([]models.WatchRoomAccountInvite, error)
	AcceptAccountInvite(ctx context.Context, inviteID, accountID, profileID string, now time.Time) (string, bool, error)
	DeclineAccountInvite(ctx context.Context, inviteID, accountID string, now time.Time) (bool, error)
	RevokeAccountInvite(ctx context.Context, inviteID, roomID, creatorProfileID string, now time.Time) (bool, error)
	ReplaceExternalInvite(ctx context.Context, invite *models.WatchRoomExternalInvite) error
	RevokeExternalInvite(ctx context.Context, roomID, creatorProfileID string) (bool, error)
	GetExternalInviteByTokenHash(ctx context.Context, tokenHash string, now time.Time) (*models.WatchRoomExternalInvite, error)
	GetExternalInviteByCode(ctx context.Context, shortCode string, now time.Time) (*models.WatchRoomExternalInvite, error)
	JoinExternalGuest(ctx context.Context, roomID, guestID, name, clientID string, capabilities models.WatchRoomClientCapabilities, now time.Time) error
	IsExternalGuest(ctx context.Context, roomID, guestID string) (bool, error)
	HeartbeatExternalGuest(ctx context.Context, roomID, guestID, clientID string, buffering bool, now time.Time) error
	LeaveExternalGuest(ctx context.Context, roomID, guestID string) error
	SetExternalGuestReady(ctx context.Context, roomID, guestID string, ready bool, now time.Time) error
	BindExternalSource(ctx context.Context, roomID, creatorProfileID, resource string, params map[string]string, episodeKey string, now time.Time) (bool, error)
	GetExternalSource(ctx context.Context, roomID string) (*models.WatchRoomExternalSource, error)
}

type RemoteAccessInviteRepository interface {
	Get(ctx context.Context, id string) (*models.RemoteAccessInvite, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (*models.RemoteAccessInvite, error)
	List(ctx context.Context) ([]models.RemoteAccessInvite, error)
	Create(ctx context.Context, inv *models.RemoteAccessInvite) error
	ClaimByTokenHash(ctx context.Context, tokenHash string, peerID string, now time.Time) (*models.RemoteAccessInvite, error)
	Update(ctx context.Context, inv *models.RemoteAccessInvite) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

type RemoteAccessPairingRepository interface {
	Get(ctx context.Context, id string) (*models.RemoteAccessPairing, error)
	GetByPeerID(ctx context.Context, peerID string) (*models.RemoteAccessPairing, error)
	List(ctx context.Context) ([]models.RemoteAccessPairing, error)
	Create(ctx context.Context, pairing *models.RemoteAccessPairing) error
	Update(ctx context.Context, pairing *models.RemoteAccessPairing) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// RemoteAccessPairingClaimer is implemented by repositories that can consume an
// invitation, create its durable pairing, and retire the invitation secret in one
// database statement.
type RemoteAccessPairingClaimer interface {
	ClaimInvite(ctx context.Context, tokenHash, peerID, credentialHash, pairingID string, now time.Time) (*models.RemoteAccessInvite, error)
}

// ClientRepository manages client/device persistence.
type ClientRepository interface {
	Get(ctx context.Context, id string) (*models.Client, error)
	ListByUser(ctx context.Context, userID string) ([]models.Client, error)
	List(ctx context.Context) ([]models.Client, error)
	Create(ctx context.Context, client *models.Client) error
	Update(ctx context.Context, client *models.Client) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)

	// Profile associations (person × device sightings)
	ListProfiles(ctx context.Context) ([]models.ClientProfileAssociation, error)
	ListProfilesByClient(ctx context.Context, clientID string) ([]models.ClientProfileAssociation, error)
	UpsertProfile(ctx context.Context, assoc models.ClientProfileAssociation) error
	DeleteProfile(ctx context.Context, clientID, userID string) error
	DeleteProfilesByClient(ctx context.Context, clientID string) error
}

// ClientSettingsRepository manages per-(device, person) filter settings.
// List returns a map keyed by models.ClientSettingsKey(clientID, userID).
type ClientSettingsRepository interface {
	Get(ctx context.Context, clientID, userID string) (*models.ClientFilterSettings, error)
	Upsert(ctx context.Context, clientID, userID string, settings *models.ClientFilterSettings) error
	Delete(ctx context.Context, clientID, userID string) error
	DeleteByClient(ctx context.Context, clientID string) error
	List(ctx context.Context) (map[string]models.ClientFilterSettings, error)
	Count(ctx context.Context) (int64, error)
}

// UserSettingsRepository manages per-user settings.
type UserSettingsRepository interface {
	Get(ctx context.Context, userID string) (*models.UserSettings, error)
	Upsert(ctx context.Context, userID string, settings *models.UserSettings) error
	Delete(ctx context.Context, userID string) error
	List(ctx context.Context) (map[string]models.UserSettings, error)
	ClearCleanPostersOverrides(ctx context.Context) (int64, error)
	Count(ctx context.Context) (int64, error)
}

// WatchlistRepository manages watchlist items per user.
type WatchlistRepository interface {
	Get(ctx context.Context, userID, itemKey string) (*models.WatchlistItem, error)
	ListByUser(ctx context.Context, userID string) ([]models.WatchlistItem, error)
	ListAll(ctx context.Context) (map[string][]models.WatchlistItem, error)
	ListTombstonesAll(ctx context.Context) (map[string][]models.WatchlistTombstone, error)
	Upsert(ctx context.Context, userID string, item *models.WatchlistItem) error
	UpsertTombstone(ctx context.Context, userID string, tombstone *models.WatchlistTombstone) error
	Delete(ctx context.Context, userID, itemKey string) error
	DeleteTombstone(ctx context.Context, userID, itemKey string) error
	DeleteByUser(ctx context.Context, userID string) error
	DeleteTombstonesByUser(ctx context.Context, userID string) error
	DeleteBySyncSource(ctx context.Context, userID, syncSource string) error
	BulkUpsert(ctx context.Context, userID string, items []models.WatchlistItem) error
	BulkUpsertTombstones(ctx context.Context, userID string, tombstones []models.WatchlistTombstone) error
	Count(ctx context.Context) (int64, error)
}

// HiddenItemsRepository manages profile-scoped title suppression markers.
type HiddenItemsRepository interface {
	ListByUser(ctx context.Context, userID string) ([]models.HiddenItem, error)
	ListAll(ctx context.Context) (map[string][]models.HiddenItem, error)
	Upsert(ctx context.Context, userID string, item *models.HiddenItem) error
	Delete(ctx context.Context, userID, itemKey string) error
	DeleteByUser(ctx context.Context, userID string) error
	BulkUpsert(ctx context.Context, userID string, items []models.HiddenItem) error
	Count(ctx context.Context) (int64, error)
}

// CustomListRepository manages user-created lists and their items.
type CustomListRepository interface {
	GetList(ctx context.Context, listID string) (*models.CustomList, error)
	ListByUser(ctx context.Context, userID string) ([]models.CustomList, error)
	CreateList(ctx context.Context, userID string, list *models.CustomList) error
	UpdateList(ctx context.Context, list *models.CustomList) error
	DeleteList(ctx context.Context, listID string) error

	GetItems(ctx context.Context, listID string) ([]models.WatchlistItem, error)
	UpsertItem(ctx context.Context, listID string, item *models.WatchlistItem) error
	DeleteItem(ctx context.Context, listID, itemKey string) error

	ListUserIDs(ctx context.Context) ([]string, error)
	Count(ctx context.Context) (int64, error)
}

// WatchHistoryRepository manages watch history.
type WatchHistoryRepository interface {
	Get(ctx context.Context, userID, itemKey string) (*models.WatchHistoryItem, error)
	ListByUser(ctx context.Context, userID string) ([]models.WatchHistoryItem, error)
	ListAll(ctx context.Context) (map[string][]models.WatchHistoryItem, error)
	Upsert(ctx context.Context, userID string, item *models.WatchHistoryItem) error
	Delete(ctx context.Context, userID, itemKey string) error
	DeleteByUser(ctx context.Context, userID string) error
	BulkUpsert(ctx context.Context, userID string, items []models.WatchHistoryItem) error
	Count(ctx context.Context) (int64, error)
}

// PlaybackProgressRepository manages playback resume positions.
type PlaybackProgressRepository interface {
	Get(ctx context.Context, userID, itemKey string) (*models.PlaybackProgress, error)
	ListByUser(ctx context.Context, userID string) ([]models.PlaybackProgress, error)
	ListAll(ctx context.Context) (map[string][]models.PlaybackProgress, error)
	Upsert(ctx context.Context, userID string, progress *models.PlaybackProgress) error
	Delete(ctx context.Context, userID, itemKey string) error
	DeleteByUser(ctx context.Context, userID string) error
	SetHidden(ctx context.Context, userID, itemKey string, hidden bool) error
	BulkUpsert(ctx context.Context, userID string, items []models.PlaybackProgress) error
	Count(ctx context.Context) (int64, error)
}

// RealtimeScrobbleSessionRepository stores provider-side playback sessions so
// cleanup can continue across backend restarts.
type RealtimeScrobbleSessionRepository interface {
	Upsert(ctx context.Context, session *models.RealtimeScrobbleSession) error
	List(ctx context.Context) ([]models.RealtimeScrobbleSession, error)
	Delete(ctx context.Context, provider, userID, mediaType, itemID string) error
}

// ContentPreferencesRepository manages per-content audio/subtitle preferences.
type ContentPreferencesRepository interface {
	Get(ctx context.Context, userID, contentID string) (*models.ContentPreference, error)
	ListByUser(ctx context.Context, userID string) ([]models.ContentPreference, error)
	Upsert(ctx context.Context, userID string, pref *models.ContentPreference) error
	Delete(ctx context.Context, userID, contentID string) error
	Count(ctx context.Context) (int64, error)
}

// SeriesOrderingRepository stores per-user, per-series episode ordering overrides.
type SeriesOrderingRepository interface {
	Get(ctx context.Context, userID string, seriesTVDBID int64) (*models.SeriesOrderingPref, error)
	// ListAll returns every preference keyed by user ID.
	ListAll(ctx context.Context) (map[string][]models.SeriesOrderingPref, error)
	Upsert(ctx context.Context, userID string, pref *models.SeriesOrderingPref) error
	Delete(ctx context.Context, userID string, seriesTVDBID int64) error
	DeleteAll(ctx context.Context) error
}

// PrequeueRepository manages prequeue entries.
// Entries are stored as JSONB blobs due to their complex nested structure.
type PrequeueRepository interface {
	Get(ctx context.Context, id string) ([]byte, error) // returns raw JSON
	GetByTitleUser(ctx context.Context, titleID, userID string) ([]byte, error)
	List(ctx context.Context) ([][]byte, error) // returns ready, unexpired entries as raw JSON
	Upsert(ctx context.Context, id, titleID, userID, status string, data []byte, expiresAt interface{}) error
	Delete(ctx context.Context, id string) error
	DeleteExpired(ctx context.Context) (int64, error)
	DeleteAll(ctx context.Context) error
	Count(ctx context.Context) (int64, error)
}

// PrewarmRepository manages prewarm entries.
// Entries are stored as JSONB blobs due to their complex nested structure.
type PrewarmRepository interface {
	Get(ctx context.Context, id string) ([]byte, error) // returns raw JSON
	List(ctx context.Context) ([][]byte, error)         // returns all entries as raw JSON
	Upsert(ctx context.Context, id, titleID, userID string, data []byte, expiresAt interface{}) error
	Delete(ctx context.Context, id string) error
	DeleteExpired(ctx context.Context) (int64, error)
	Count(ctx context.Context) (int64, error)
}

// ImportQueueRepository manages the import queue (migrated from queue.db).
type ImportQueueRepository interface {
	Count(ctx context.Context) (int64, error)
}

// FileHealthRepository manages file health tracking.
type FileHealthRepository interface {
	Count(ctx context.Context) (int64, error)
}

// MediaFileRepository manages media file tracking.
type MediaFileRepository interface {
	Count(ctx context.Context) (int64, error)
}

type LocalMediaRepository interface {
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
	GetLibrary(ctx context.Context, id string) (*models.LocalMediaLibrary, error)
	CreateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error
	UpdateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error
	DeleteLibrary(ctx context.Context, id string) error
	ListItemsByLibrary(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaItemListResult, error)
	ListAllItemsByLibrary(ctx context.Context, libraryID string) ([]models.LocalMediaItem, error)
	// ListItemsForTitle lists a library's present, matched items whose title ID
	// or metadata IDs equal the given ones (empty ones are left out), of one
	// episode when episode > 0: the candidates of a title's library copy.
	ListItemsForTitle(ctx context.Context, libraryID, titleID, imdbID, tmdbID, tvdbID string, season, episode int) ([]models.LocalMediaItem, error)
	UpsertItem(ctx context.Context, item *models.LocalMediaItem) error
	GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error)
	DeleteItemsNotSeenInScan(ctx context.Context, libraryID, scanID string) error
	DeleteItem(ctx context.Context, id string) error
}

type RemoteMediaRepository interface {
	ListLibraries(ctx context.Context) ([]models.RemoteMediaLibrary, error)
	GetLibrary(ctx context.Context, id string) (*models.RemoteMediaLibrary, error)
	CreateLibrary(ctx context.Context, library *models.RemoteMediaLibrary) error
	UpdateLibrary(ctx context.Context, library *models.RemoteMediaLibrary) error
	DeleteLibrary(ctx context.Context, id string) error
	ListItems(ctx context.Context, libraryID string, includeMissing bool) ([]models.RemoteMediaItem, error)
	GetItem(ctx context.Context, id string) (*models.RemoteMediaItem, error)
	UpsertItem(ctx context.Context, item *models.RemoteMediaItem) error
	MarkItemsMissingNotSeenInSync(ctx context.Context, libraryID, syncID string) error
}

// LibraryAccessRepository manages access policies shared by local and remote libraries.
type LibraryAccessRepository interface {
	Get(ctx context.Context, libraryID string) (*models.LibraryAccessPolicy, error)
	List(ctx context.Context) (map[string]models.LibraryAccessPolicy, error)
	Set(ctx context.Context, policy models.LibraryAccessPolicy) error
	Delete(ctx context.Context, libraryID string) error
}

type RecordingRepository interface {
	Get(ctx context.Context, id string) (*models.Recording, error)
	List(ctx context.Context, filter models.RecordingListFilter) ([]models.Recording, error)
	Create(ctx context.Context, recording *models.Recording) error
	Update(ctx context.Context, recording *models.Recording) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
	MarkStaleActiveAsFailed(ctx context.Context, now time.Time) (int64, error)
}

type RecordingRuleRepository interface {
	GetRule(ctx context.Context, id string) (*models.RecordingRule, error)
	ListRules(ctx context.Context, userID string, includeAll, enabledOnly bool) ([]models.RecordingRule, error)
	CreateRule(ctx context.Context, rule *models.RecordingRule) error
	UpdateRule(ctx context.Context, rule *models.RecordingRule) error
	DeleteRule(ctx context.Context, id string) error
	CancelPendingRuleRecordings(ctx context.Context, ruleID string, now time.Time) error
	PausePendingRuleRecordings(ctx context.Context, ruleID string, now time.Time) error
	UpdatePendingRuleRecordingBuffers(ctx context.Context, ruleID string, before, after int) error
	CreateScheduledRecordingIfAbsent(ctx context.Context, recording *models.Recording) (bool, error)
}

// SportsLinksRepository manages the "Manage Team Channels" feature's persisted state:
// team identities and the Live TV channels linked to them (primary + backups). Global/
// shared, not per-user - see the sports feature plan for why.
type SportsLinksRepository interface {
	// UpsertTeam records/refreshes a team's identity, called lazily whenever the ESPN
	// scoreboard poller observes it playing a game.
	UpsertTeam(ctx context.Context, team models.SportsTeamRecord) error
	ListTeamsByLeague(ctx context.Context, league string) ([]models.SportsTeamRecord, error)
	GetTeam(ctx context.Context, teamID string) (*models.SportsTeamRecord, error)

	GetLinksForTeam(ctx context.Context, teamID string) ([]models.SportsTeamChannelLink, error)
	// GetLinksForTeams batches the above for the GameCard "linked channels" lookup, which
	// needs several teams' links per scoreboard page rather than one at a time.
	GetLinksForTeams(ctx context.Context, teamIDs []string) (map[string][]models.SportsTeamChannelLink, error)

	// SetPrimaryLink upserts the one primary-slot link for a team (position is always 0).
	SetPrimaryLink(ctx context.Context, teamID string, link models.SportsTeamChannelLink) (*models.SportsTeamChannelLink, error)
	// InsertAutoPrimaryLinks inserts eligible primary links in one atomic statement and
	// skips teams that acquired a primary after preview rather than overwriting them.
	InsertAutoPrimaryLinks(ctx context.Context, links []models.SportsTeamChannelLink) ([]models.SportsTeamChannelLink, error)
	// AddBackupLink appends a new backup-slot link after the team's current highest backup
	// position.
	AddBackupLink(ctx context.Context, teamID string, link models.SportsTeamChannelLink) (*models.SportsTeamChannelLink, error)
	DeleteLink(ctx context.Context, teamID, linkID string) error
	// ReorderBackups rewrites backup positions to match the given link ID order.
	ReorderBackups(ctx context.Context, teamID string, orderedLinkIDs []string) error
}
