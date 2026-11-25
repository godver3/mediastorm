package remotemedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/jellyfin"
	"novastream/services/kids"
	"novastream/services/plex"
	"novastream/services/streaming"
)

var ErrNotFound = errors.New("remote media not found")

type AvailableLibrary struct {
	ID         string                       `json:"id"`
	Name       string                       `json:"name"`
	Type       models.LocalMediaLibraryType `json:"type"`
	ServerID   string                       `json:"serverId,omitempty"`
	ServerName string                       `json:"serverName,omitempty"`
}

func localLibraryType(providerType string) models.LocalMediaLibraryType {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "movie", "movies":
		return models.LocalMediaLibraryTypeMovie
	case "show", "tvshows":
		return models.LocalMediaLibraryTypeShow
	default:
		return models.LocalMediaLibraryTypeOther
	}
}

type AvailableServer struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Platform    string                `json:"platform,omitempty"`
	Online      bool                  `json:"online"`
	Connections []plex.PlexConnection `json:"connections"`
}

type VerifiedServer struct {
	ServerID   string             `json:"serverId"`
	ServerName string             `json:"serverName"`
	ServerURL  string             `json:"serverUrl"`
	Libraries  []AvailableLibrary `json:"libraries"`
}

// ErrSyncInProgress reports that the library is already being synced.
var ErrSyncInProgress = errors.New("library sync already in progress")

type Service struct {
	syncing  sync.Map // library IDs being synced
	repo     datastore.RemoteMediaRepository
	cfg      *config.Manager
	plex     *plex.Client
	jellyfin *jellyfin.Client
	servers  plexServerResolver
}

const plexServerCacheTTL = 5 * time.Minute

type plexServerCacheEntry struct {
	server    plex.PlexResource
	authToken string
	expiresAt time.Time
}

// plexServerResolver prevents artwork and playback requests from each fetching
// the Plex resource list independently. Keeping the load under the mutex also
// collapses a burst of poster requests into one plex.tv request.
type plexServerResolver struct {
	mu      sync.Mutex
	entries map[string]plexServerCacheEntry
	now     func() time.Time
}

func (r *plexServerResolver) resolve(accountID, authToken, serverID string, load func(string) ([]plex.PlexResource, error)) (plex.PlexResource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	key := accountID + "\x00" + serverID
	if cached, ok := r.entries[key]; ok && cached.authToken == authToken && now.Before(cached.expiresAt) {
		return cached.server, nil
	}

	servers, err := load(authToken)
	if err != nil {
		return plex.PlexResource{}, err
	}
	if r.entries == nil {
		r.entries = make(map[string]plexServerCacheEntry)
	}
	// A refreshed resource list may no longer grant access to this server.
	// Never reuse its previous access token when the server is absent.
	delete(r.entries, key)
	for _, server := range servers {
		serverKey := accountID + "\x00" + server.ClientIdentifier
		r.entries[serverKey] = plexServerCacheEntry{
			server:    server,
			authToken: authToken,
			expiresAt: now.Add(plexServerCacheTTL),
		}
	}
	if cached, ok := r.entries[key]; ok {
		return cached.server, nil
	}
	return plex.PlexResource{}, errors.New("Plex server unavailable")
}

func NewService(store *datastore.DataStore, cfg *config.Manager, plexClient *plex.Client, jellyfinClient *jellyfin.Client) (*Service, error) {
	if store == nil || cfg == nil {
		return nil, errors.New("remote media datastore and config are required")
	}
	return &Service{repo: store.RemoteMedia(), cfg: cfg, plex: plexClient, jellyfin: jellyfinClient}, nil
}

func (s *Service) ListLibraries(ctx context.Context) ([]models.RemoteMediaLibrary, error) {
	return s.repo.ListLibraries(ctx)
}
func (s *Service) GetLibrary(ctx context.Context, id string) (*models.RemoteMediaLibrary, error) {
	return s.repo.GetLibrary(ctx, id)
}
func (s *Service) GetItem(ctx context.Context, id string) (*models.RemoteMediaItem, error) {
	return s.repo.GetItem(ctx, id)
}

func (s *Service) DiscoverPlexServers(accountID string) ([]AvailableServer, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, err
	}
	account := settings.Plex.GetAccountByID(accountID)
	if account == nil || account.AuthToken == "" {
		return nil, errors.New("Plex account not connected")
	}
	servers, err := s.plex.GetVisibleServers(account.AuthToken)
	if err != nil {
		return nil, err
	}
	result := make([]AvailableServer, 0, len(servers))
	for _, server := range servers {
		connections := make([]plex.PlexConnection, 0, len(server.Connections))
		for _, connection := range server.Connections {
			if _, err := plex.NormalizeServerURL(connection.URI); err == nil {
				connections = append(connections, connection)
			}
		}
		result = append(result, AvailableServer{
			ID:          server.ClientIdentifier,
			Name:        server.Name,
			Platform:    server.Platform,
			Online:      server.Presence,
			Connections: connections,
		})
	}
	return result, nil
}

func (s *Service) VerifyPlexServer(ctx context.Context, accountID, serverID, serverURL string) (*VerifiedServer, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, err
	}
	account := settings.Plex.GetAccountByID(accountID)
	if account == nil || account.AuthToken == "" {
		return nil, errors.New("Plex account not connected")
	}
	server, err := s.servers.resolve(accountID, account.AuthToken, strings.TrimSpace(serverID), s.plex.GetVisibleServers)
	if err != nil {
		return nil, err
	}
	normalizedURL, err := plex.NormalizeServerURL(serverURL)
	if err != nil {
		return nil, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	libraries, err := s.plex.GetServerLibrariesAt(verifyCtx, server, normalizedURL)
	if err != nil {
		return nil, fmt.Errorf("connect to Plex server %q at %s: %w", server.Name, normalizedURL, err)
	}
	available := make([]AvailableLibrary, 0, len(libraries))
	for _, library := range libraries {
		available = append(available, AvailableLibrary{
			ID:         library.Key,
			Name:       library.Title,
			Type:       localLibraryType(library.Type),
			ServerID:   server.ClientIdentifier,
			ServerName: server.Name,
		})
	}
	return &VerifiedServer{
		ServerID:   server.ClientIdentifier,
		ServerName: server.Name,
		ServerURL:  normalizedURL,
		Libraries:  available,
	}, nil
}

func (s *Service) plexServerForLibrary(library *models.RemoteMediaLibrary, authToken string) (plex.PlexResource, error) {
	load := s.plex.GetAccessibleServers
	if strings.TrimSpace(library.ServerURL) != "" {
		load = s.plex.GetVisibleServers
	}
	server, err := s.servers.resolve(library.AccountID, authToken, library.ServerID, load)
	if err != nil {
		return plex.PlexResource{}, err
	}
	if strings.TrimSpace(library.ServerURL) == "" {
		return server, nil
	}
	return plex.WithConnection(server, library.ServerURL)
}

func (s *Service) Discover(ctx context.Context, provider, accountID string) ([]AvailableLibrary, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case models.MediaSourceJellyfin:
		account := settings.Jellyfin.GetAccountByID(accountID)
		if account == nil || account.Token == "" {
			return nil, errors.New("Jellyfin account not connected")
		}
		libraries, err := s.jellyfin.GetLibraries(account.ServerURL, account.Token, account.UserID)
		if err != nil {
			return nil, err
		}
		result := make([]AvailableLibrary, 0, len(libraries))
		for _, library := range libraries {
			result = append(result, AvailableLibrary{ID: library.ID, Name: library.Name, Type: localLibraryType(library.CollectionType), ServerName: account.Name})
		}
		return result, nil
	case models.MediaSourcePlex:
		account := settings.Plex.GetAccountByID(accountID)
		if account == nil || account.AuthToken == "" {
			return nil, errors.New("Plex account not connected")
		}
		servers, err := s.plex.GetAccessibleServers(account.AuthToken)
		if err != nil {
			return nil, err
		}
		result := []AvailableLibrary{}
		for _, server := range servers {
			libraries, err := s.plex.GetServerLibraries(server)
			if err != nil {
				continue
			}
			for _, library := range libraries {
				result = append(result, AvailableLibrary{ID: library.Key, Name: library.Title, Type: localLibraryType(library.Type), ServerID: server.ClientIdentifier, ServerName: server.Name})
			}
		}
		return result, nil
	default:
		return nil, errors.New("provider must be plex or jellyfin")
	}
}

func (s *Service) CreateLibrary(ctx context.Context, input models.RemoteMediaLibraryCreateInput) (*models.RemoteMediaLibrary, error) {
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if provider != models.MediaSourcePlex && provider != models.MediaSourceJellyfin {
		return nil, errors.New("invalid remote provider")
	}
	if input.AccountID == "" || input.ExternalLibraryID == "" {
		return nil, errors.New("accountId and externalLibraryId are required")
	}
	existing, err := s.repo.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range existing {
		library := &existing[i]
		if library.Provider == provider && library.AccountID == input.AccountID && library.ServerID == input.ServerID && library.ExternalLibraryID == input.ExternalLibraryID {
			updated := false
			if name := strings.TrimSpace(input.Name); name != "" && name != library.Name {
				library.Name = name
				updated = true
			}
			if provider == models.MediaSourcePlex && input.ServerURL != "" && input.ServerURL != library.ServerURL {
				normalized, err := plex.NormalizeServerURL(input.ServerURL)
				if err != nil {
					return nil, err
				}
				library.ServerURL = normalized
				updated = true
			}
			if updated {
				library.UpdatedAt = time.Now().UTC()
				if err := s.repo.UpdateLibrary(ctx, library); err != nil {
					return nil, err
				}
			}
			if _, err := s.Sync(ctx, library.ID); err != nil {
				return s.repo.GetLibrary(ctx, library.ID)
			}
			return s.repo.GetLibrary(ctx, library.ID)
		}
	}
	now := time.Now().UTC()
	serverURL := strings.TrimSpace(input.ServerURL)
	if provider == models.MediaSourcePlex && serverURL != "" {
		serverURL, err = plex.NormalizeServerURL(serverURL)
		if err != nil {
			return nil, err
		}
	}
	library := &models.RemoteMediaLibrary{ID: uuid.NewString(), Name: strings.TrimSpace(input.Name), Type: input.Type,
		Provider: provider, AccountID: input.AccountID, ServerID: input.ServerID, ServerName: input.ServerName, ServerURL: serverURL,
		ExternalLibraryID: input.ExternalLibraryID, CreatedAt: now, UpdatedAt: now, LastSyncStatus: models.LocalMediaScanStatusIdle}
	if library.Name == "" {
		library.Name = "Remote Library"
	}
	if library.Type == "" {
		library.Type = models.LocalMediaLibraryTypeMovie
	}
	if err := s.repo.CreateLibrary(ctx, library); err != nil {
		return nil, err
	}
	if _, err := s.Sync(ctx, library.ID); err != nil {
		// Keep the configuration and its failed status so the admin page can show
		// the provider error and offer Sync retry without requiring re-entry.
		return s.repo.GetLibrary(ctx, library.ID)
	}
	return s.repo.GetLibrary(ctx, library.ID)
}

func (s *Service) DeleteLibrary(ctx context.Context, id string) error {
	return s.repo.DeleteLibrary(ctx, id)
}

func (s *Service) Sync(ctx context.Context, id string) (int, error) {
	// Overlapping syncs of one library would mark each other's items missing.
	if _, running := s.syncing.LoadOrStore(id, struct{}{}); running {
		return 0, ErrSyncInProgress
	}
	defer s.syncing.Delete(id)
	library, err := s.repo.GetLibrary(ctx, id)
	if err != nil || library == nil {
		if err == nil {
			err = ErrNotFound
		}
		return 0, err
	}
	now := time.Now().UTC()
	library.LastSyncStartedAt = &now
	library.LastSyncStatus = models.LocalMediaScanStatusScanning
	library.LastSyncError = ""
	library.UpdatedAt = now
	if err := s.repo.UpdateLibrary(ctx, library); err != nil {
		return 0, err
	}
	items, syncErr := s.fetch(ctx, library)
	if syncErr == nil {
		syncID := uuid.NewString()
		for i := range items {
			items[i].LastSeenSyncID = syncID
			items[i].LibraryID = library.ID
			if err := s.repo.UpsertItem(ctx, &items[i]); err != nil {
				syncErr = err
				break
			}
		}
		if syncErr == nil {
			syncErr = s.repo.MarkItemsMissingNotSeenInSync(ctx, library.ID, syncID)
		}
	}
	finished := time.Now().UTC()
	library.LastSyncFinishedAt = &finished
	library.UpdatedAt = finished
	if syncErr != nil {
		library.LastSyncStatus = models.LocalMediaScanStatusFailed
		library.LastSyncError = syncErr.Error()
	} else {
		library.LastSyncStatus = models.LocalMediaScanStatusComplete
		library.LastSyncTotal = len(items)
	}
	_ = s.repo.UpdateLibrary(ctx, library)
	return len(items), syncErr
}

func (s *Service) fetch(ctx context.Context, library *models.RemoteMediaLibrary) ([]models.RemoteMediaItem, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, err
	}
	if library.Provider == models.MediaSourceJellyfin {
		account := settings.Jellyfin.GetAccountByID(library.AccountID)
		if account == nil {
			return nil, errors.New("Jellyfin account missing")
		}
		collectionType := ""
		switch library.Type {
		case models.LocalMediaLibraryTypeMovie:
			collectionType = "movies"
		case models.LocalMediaLibraryTypeShow:
			collectionType = "tvshows"
		}
		items, err := s.jellyfin.GetLibraryItems(account.ServerURL, account.Token, account.UserID, library.ExternalLibraryID, collectionType)
		if err != nil {
			return nil, err
		}
		return normalizeJellyfin(library, items), nil
	}
	account := settings.Plex.GetAccountByID(library.AccountID)
	if account == nil {
		return nil, errors.New("Plex account missing")
	}
	server, err := s.plexServerForLibrary(library, account.AuthToken)
	if err != nil {
		return nil, err
	}
	items, err := s.plex.GetServerLibraryItems(server, library.ExternalLibraryID, string(library.Type))
	if err != nil {
		return nil, err
	}
	return normalizePlex(library, items), nil
}

func normalizeJellyfin(library *models.RemoteMediaLibrary, source []jellyfin.JellyfinItem) []models.RemoteMediaItem {
	now := time.Now().UTC()
	result := []models.RemoteMediaItem{}
	for _, item := range source {
		createdAt := now
		if item.DateCreated != nil && !item.DateCreated.IsZero() {
			createdAt = item.DateCreated.UTC()
		}
		groupKey := item.ID
		title := item.Name
		episodeTitle := ""
		if item.Type == "Episode" {
			groupKey = item.SeriesID
			title = item.SeriesName
			episodeTitle = item.Name
		}
		exts := &models.LocalMediaExternalIDs{IMDB: item.ProviderIDs["imdb"], TMDB: item.ProviderIDs["tmdb"], TVDB: item.ProviderIDs["tvdb"]}
		sources := item.MediaSources
		if len(sources) == 0 {
			sources = []jellyfin.JellyfinMediaSource{{ID: item.ID, Name: item.Name}}
		}
		for _, media := range sources {
			durationTicks := media.RunTimeTicks
			if durationTicks <= 0 {
				durationTicks = item.RunTimeTicks
			}
			v := models.RemoteMediaItem{ID: stableItemID(library.ID, item.ID, media.ID), ExternalItemID: item.ID, ExternalMediaID: media.ID, GroupKey: groupKey,
				LibraryType: library.Type, Title: title, Year: item.Year, Overview: item.Overview, Certification: item.OfficialRating,
				SeasonNumber: item.SeasonNum, EpisodeNumber: item.EpisodeNum, EpisodeTitle: episodeTitle, ExternalIDs: exts,
				FileName: filepath.Base(media.Path), Container: media.Container, DurationSeconds: float64(durationTicks) / 10_000_000, SizeBytes: media.Size, CreatedAt: createdAt, UpdatedAt: now,
				ProviderData: map[string]string{"itemId": item.ID, "mediaSourceId": media.ID}}
			for _, stream := range media.MediaStreams {
				if stream.Type == "Video" {
					v.VideoCodec = stream.Codec
					v.Width = stream.Width
					v.Height = stream.Height
					v.HDRFormat = stream.VideoRange
				}
				if stream.Type == "Audio" && v.AudioCodec == "" {
					v.AudioCodec = stream.Codec
				}
			}
			v.VersionLabel = versionLabel(v.Height, v.VideoCodec, v.HDRFormat, media.Name)
			v.StreamPath = "jellyfinmedia:" + v.ID
			result = append(result, v)
		}
	}
	return result
}

func normalizePlex(library *models.RemoteMediaLibrary, source []plex.PlexLibraryItem) []models.RemoteMediaItem {
	now := time.Now().UTC()
	result := []models.RemoteMediaItem{}
	for _, item := range source {
		createdAt := now
		if item.AddedAt > 0 {
			createdAt = time.Unix(item.AddedAt, 0).UTC()
		}
		groupKey := item.RatingKey
		title := item.Title
		episodeTitle := ""
		posterPath := item.Thumb
		backdropPath := item.Art
		if item.Type == "episode" {
			groupKey = item.GrandparentRatingKey
			title = item.GrandparentTitle
			episodeTitle = item.Title
			posterPath = item.GrandparentThumb
			backdropPath = item.GrandparentArt
		}
		guids := item.Guid
		year := item.Year
		if item.Type == "episode" {
			if len(item.GrandparentGuid) > 0 {
				guids = item.GrandparentGuid
			}
			if item.GrandparentYear > 0 {
				year = item.GrandparentYear
			}
		}
		exts := plexExternalIDs(guids)
		for _, media := range item.Media {
			for _, part := range media.Part {
				externalMediaID := strconv.FormatInt(part.ID, 10)
				v := models.RemoteMediaItem{ID: stableItemID(library.ID, item.RatingKey, externalMediaID), ExternalItemID: item.RatingKey, ExternalMediaID: externalMediaID, GroupKey: groupKey,
					LibraryType: library.Type, Title: title, Year: year, Overview: item.Summary, Certification: item.ContentRating,
					SeasonNumber: item.ParentIndex, EpisodeNumber: item.Index, EpisodeTitle: episodeTitle, ExternalIDs: exts,
					FileName: filepath.Base(part.File), Container: part.Container, VideoCodec: media.VideoCodec, AudioCodec: media.AudioCodec,
					Width: media.Width, Height: media.Height, HDRFormat: media.VideoDynamicRange, DurationSeconds: float64(item.Duration) / 1000, SizeBytes: part.Size, CreatedAt: createdAt, UpdatedAt: now,
					ProviderData: map[string]string{"partKey": part.Key, "posterPath": posterPath, "backdropPath": backdropPath}}
				v.VersionLabel = versionLabel(v.Height, v.VideoCodec, v.HDRFormat, media.VideoResolution)
				v.StreamPath = "plexmedia:" + v.ID
				result = append(result, v)
			}
		}
	}
	return result
}

func plexExternalIDs(guids []plex.PlexGuid) *models.LocalMediaExternalIDs {
	v := &models.LocalMediaExternalIDs{}
	for _, guid := range guids {
		parts := strings.SplitN(guid.ID, "://", 2)
		if len(parts) != 2 {
			continue
		}
		switch strings.ToLower(parts[0]) {
		case "imdb":
			v.IMDB = parts[1]
		case "tmdb":
			v.TMDB = parts[1]
		case "tvdb":
			v.TVDB = parts[1]
		}
	}
	return v
}

func versionLabel(height int, codec, hdr, fallback string) string {
	bits := []string{}
	if height > 0 {
		bits = append(bits, fmt.Sprintf("%dp", height))
	}
	if codec != "" {
		bits = append(bits, strings.ToUpper(codec))
	}
	if hdr != "" && !strings.EqualFold(hdr, "SDR") {
		bits = append(bits, strings.ToUpper(hdr))
	}
	if len(bits) == 0 {
		bits = append(bits, strings.TrimSpace(fallback))
	}
	return strings.Join(bits, " · ")
}

func stableItemID(libraryID, externalItemID, externalMediaID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(libraryID+"\x00"+externalItemID+"\x00"+externalMediaID)).String()
}

func (s *Service) ListGroups(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaGroupListResult, error) {
	library, err := s.repo.GetLibrary(ctx, libraryID)
	if err != nil || library == nil {
		return nil, ErrNotFound
	}
	items, err := s.repo.ListItems(ctx, libraryID, query.IncludeMissing)
	if err != nil {
		return nil, err
	}
	groups := groupItems(library, items, query.IncludeCards)
	// Kids profile rating caps, as local libraries apply them: before paging, so
	// totals and alphabet buckets only count what the profile may see.
	groups = kids.FilterLibraryGroupsByRatings(groups, query.MaxMovieRating, query.MaxTVRating)
	if filter := strings.TrimSpace(query.Filter); filter != "" && filter != "all" && filter != string(models.LocalMediaMatchStatusMatched) && filter != string(models.LocalMediaMatchStatusManual) {
		groups = []models.LocalMediaItemGroup{}
	}
	if q := strings.ToLower(strings.TrimSpace(query.Query)); q != "" {
		filtered := groups[:0]
		for _, g := range groups {
			if strings.Contains(strings.ToLower(g.Title), q) {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
	}
	if mediaType := strings.ToLower(strings.TrimSpace(query.MediaType)); mediaType != "" && mediaType != "all" {
		want := models.LocalMediaLibraryTypeMovie
		if mediaType == "series" || mediaType == "tv" || mediaType == "show" {
			want = models.LocalMediaLibraryTypeShow
		}
		filtered := groups[:0]
		for _, group := range groups {
			if group.LibraryType == want {
				filtered = append(filtered, group)
			}
		}
		groups = filtered
	}
	alphabetBuckets := remoteMediaAlphabetBuckets(groups)
	if alphabet := strings.ToUpper(strings.TrimSpace(query.Alphabet)); alphabet != "" {
		filtered := groups[:0]
		for _, group := range groups {
			if remoteMediaAlphabetBucket(group.Title) == alphabet {
				filtered = append(filtered, group)
			}
		}
		groups = filtered
	}
	sortRemoteMediaGroups(groups, query.Sort, query.Dir)
	total := len(groups)
	limit := query.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	return &models.LocalMediaGroupListResult{Groups: groups[offset:end], Total: total, Limit: limit, Offset: offset, AlphabetBuckets: alphabetBuckets}, nil
}

func remoteMediaAlphabetBuckets(groups []models.LocalMediaItemGroup) []string {
	set := make(map[string]struct{})
	for _, group := range groups {
		set[remoteMediaAlphabetBucket(group.Title)] = struct{}{}
	}
	buckets := make([]string, 0, len(set))
	for bucket := range set {
		buckets = append(buckets, bucket)
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i] == "#" {
			return true
		}
		if buckets[j] == "#" {
			return false
		}
		return buckets[i] < buckets[j]
	})
	return buckets
}

func remoteMediaAlphabetBucket(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	for _, article := range []string{"THE ", "AN ", "A "} {
		if strings.HasPrefix(name, article) {
			name = strings.TrimSpace(name[len(article):])
			break
		}
	}
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return "#"
	}
	return name[:1]
}

func groupItems(library *models.RemoteMediaLibrary, items []models.RemoteMediaItem, cards bool) []models.LocalMediaItemGroup {
	byKey := map[string]*models.LocalMediaItemGroup{}
	order := []string{}
	for _, item := range items {
		g := byKey[item.GroupKey]
		if g == nil {
			g = &models.LocalMediaItemGroup{ID: item.GroupKey, GroupType: "title", LibraryType: item.LibraryType, Title: item.Title, Overview: item.Overview, Certification: item.Certification, Year: item.Year, MatchStatus: models.LocalMediaMatchStatusMatched, Poster: &models.Image{URL: "/api/library/items/" + item.ID + "/artwork/poster", Type: "poster"}, Backdrop: &models.Image{URL: "/api/library/items/" + item.ID + "/artwork/backdrop", Type: "backdrop"}}
			if item.LibraryType == models.LocalMediaLibraryTypeShow {
				g.GroupType = "show"
			}
			applyExternalIDs(g, item.ExternalIDs)
			byKey[item.GroupKey] = g
			order = append(order, item.GroupKey)
		}
		g.ItemCount++
		g.TotalSizeBytes += item.SizeBytes
		g.LatestCreatedAt = latestRemoteTime(g.LatestCreatedAt, item.CreatedAt)
		g.LatestUpdatedAt = latestRemoteTime(g.LatestUpdatedAt, item.UpdatedAt)
		if cards {
			continue
		}
		converted := toLocalItem(library, item)
		if item.LibraryType == models.LocalMediaLibraryTypeMovie {
			g.Items = append(g.Items, converted)
			continue
		}
		seasonIndex := -1
		for i := range g.Seasons {
			if g.Seasons[i].SeasonNumber == item.SeasonNumber {
				seasonIndex = i
				break
			}
		}
		if seasonIndex < 0 {
			g.Seasons = append(g.Seasons, models.LocalMediaSeasonGroup{ID: fmt.Sprintf("%s:s%d", item.GroupKey, item.SeasonNumber), SeasonNumber: item.SeasonNumber, MatchStatus: models.LocalMediaMatchStatusMatched})
			seasonIndex = len(g.Seasons) - 1
		}
		season := &g.Seasons[seasonIndex]
		season.ItemCount++
		episodeIndex := -1
		for i := range season.Episodes {
			if season.Episodes[i].EpisodeNumber == item.EpisodeNumber {
				episodeIndex = i
				break
			}
		}
		if episodeIndex < 0 {
			season.Episodes = append(season.Episodes, models.LocalMediaEpisodeGroup{ID: fmt.Sprintf("%s:s%de%d", item.GroupKey, item.SeasonNumber, item.EpisodeNumber), EpisodeNumber: item.EpisodeNumber, EpisodeTitle: item.EpisodeTitle, MatchStatus: models.LocalMediaMatchStatusMatched})
			episodeIndex = len(season.Episodes) - 1
		}
		ep := &season.Episodes[episodeIndex]
		ep.Items = append(ep.Items, converted)
		ep.ItemCount++
	}
	result := make([]models.LocalMediaItemGroup, 0, len(order))
	for _, key := range order {
		result = append(result, *byKey[key])
	}
	return result
}

func sortRemoteMediaGroups(groups []models.LocalMediaItemGroup, sortBy, dir string) {
	sortMode := strings.TrimSpace(sortBy)
	// An omitted sort preserves the library's historical default (title A-Z),
	// even though clients send their current direction alongside it.
	desc := sortMode != "" && dir == "desc"
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		cmp := 0
		switch sortMode {
		case "created":
			cmp = compareRemoteTime(a.LatestCreatedAt, b.LatestCreatedAt)
		case "year":
			if a.Year < b.Year {
				cmp = -1
			} else if a.Year > b.Year {
				cmp = 1
			}
		default:
			cmp = strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		}
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		}
		if cmp == 0 {
			cmp = strings.Compare(a.ID, b.ID)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func latestRemoteTime(current *time.Time, candidate time.Time) *time.Time {
	if candidate.IsZero() || (current != nil && !candidate.After(*current)) {
		return current
	}
	value := candidate
	return &value
}

func compareRemoteTime(a, b *time.Time) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	if a.Before(*b) {
		return -1
	}
	if a.After(*b) {
		return 1
	}
	return 0
}

func applyExternalIDs(g *models.LocalMediaItemGroup, ids *models.LocalMediaExternalIDs) {
	if ids == nil {
		return
	}
	g.IMDBID = ids.IMDB
	g.TMDBID, _ = strconv.ParseInt(ids.TMDB, 10, 64)
	g.TVDBID, _ = strconv.ParseInt(ids.TVDB, 10, 64)
}
func toLocalItem(library *models.RemoteMediaLibrary, item models.RemoteMediaItem) models.LocalMediaItem {
	return models.LocalMediaItem{ID: item.ID, LibraryID: item.LibraryID, FileName: item.FileName, LibraryType: item.LibraryType, DetectedTitle: item.Title, DetectedYear: item.Year, SeasonNumber: item.SeasonNumber, EpisodeNumber: item.EpisodeNumber, MatchStatus: models.LocalMediaMatchStatusMatched, MatchedName: item.Title, MatchedYear: item.Year, ExternalIDs: item.ExternalIDs, EpisodeTitle: item.EpisodeTitle, SizeBytes: item.SizeBytes, Probe: &models.LocalMediaProbe{DurationSeconds: item.DurationSeconds, SizeBytes: item.SizeBytes, VideoCodec: item.VideoCodec, Width: item.Width, Height: item.Height, HDRFormat: item.HDRFormat, AudioCodecs: []string{item.AudioCodec}}, SourceType: library.Provider, SourceName: strings.Title(library.Provider), SourceServerName: library.ServerName, VersionLabel: item.VersionLabel}
}

func (s *Service) FindMatches(ctx context.Context, query models.LocalMediaMatchQuery) ([]models.LocalMediaMatchedGroup, error) {
	libraries, err := s.repo.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	targetType := remoteLookupLibraryType(query.MediaType)
	result := []models.LocalMediaMatchedGroup{}
	for i := range libraries {
		library := libraries[i]
		if targetType != "" && library.Type != targetType {
			continue
		}
		// Scan the full library. ListGroups is paginated (max 200) and title-
		// prefiltered, which drops later titles (e.g. Zootropolis) and alternate
		// localized names that still share IMDB/TMDB/TVDB IDs with the details page.
		items, err := s.repo.ListItems(ctx, library.ID, false)
		if err != nil {
			continue
		}
		for _, g := range groupItems(&library, items, false) {
			if matches(g, query) {
				result = append(result, models.LocalMediaMatchedGroup{
					LibraryID:   library.ID,
					LibraryName: library.Name,
					LibraryType: library.Type,
					Group:       g,
				})
			}
		}
	}
	return result, nil
}

func remoteLookupLibraryType(mediaType string) models.LocalMediaLibraryType {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return models.LocalMediaLibraryTypeMovie
	case "series", "show", "tv":
		return models.LocalMediaLibraryTypeShow
	default:
		return ""
	}
}

func matches(g models.LocalMediaItemGroup, q models.LocalMediaMatchQuery) bool {
	queryIMDB := strings.TrimSpace(q.IMDBID)
	queryTMDB := strings.TrimSpace(q.TMDBID)
	queryTVDB := strings.TrimSpace(q.TVDBID)
	if queryIMDB != "" && strings.EqualFold(strings.TrimSpace(g.IMDBID), queryIMDB) {
		return true
	}
	if queryTMDB != "" && strconv.FormatInt(g.TMDBID, 10) == queryTMDB {
		return true
	}
	if queryTVDB != "" && strconv.FormatInt(g.TVDBID, 10) == queryTVDB {
		return true
	}
	return q.Title != "" &&
		strings.EqualFold(strings.TrimSpace(g.Title), strings.TrimSpace(q.Title)) &&
		(q.Year == 0 || g.Year == 0 || q.Year == g.Year)
}

func (s *Service) Playback(ctx context.Context, itemID string) (*models.LocalMediaPlaybackResponse, error) {
	item, err := s.repo.GetItem(ctx, itemID)
	if err != nil || item == nil {
		return nil, ErrNotFound
	}
	library, err := s.repo.GetLibrary(ctx, item.LibraryID)
	if err != nil || library == nil {
		return nil, ErrNotFound
	}
	mediaType := "movie"
	if item.LibraryType == models.LocalMediaLibraryTypeShow {
		mediaType = "episode"
	}
	seriesTitle := ""
	if mediaType == "episode" {
		seriesTitle = item.Title
	}
	// Only expose a titleId when the item has real catalog external IDs.
	// Plex/Jellyfin group keys are library-local and collide with TVDB/TMDB
	// numeric IDs (e.g. home-video rating key 264995 → wrong movie metadata).
	titleID := remoteCatalogTitleID(item)
	values := url.Values{
		"path":              {item.StreamPath},
		"transmux":          {"0"},
		"mediaType":         {mediaType},
		"sourceServiceType": {library.Provider},
	}
	streamItemID := titleID
	if mediaType == "episode" && streamItemID != "" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
		streamItemID += fmt.Sprintf(":S%02dE%02d", item.SeasonNumber, item.EpisodeNumber)
	}
	if streamItemID == "" {
		streamItemID = item.ID
	}
	values.Set("itemId", streamItemID)
	if titleID != "" {
		values.Set("titleId", titleID)
	}
	if item.Title != "" {
		values.Set("title", item.Title)
	}
	if item.Year > 0 {
		values.Set("year", strconv.Itoa(item.Year))
	}
	if mediaType == "episode" {
		values.Set("seriesName", item.Title)
		if titleID != "" {
			values.Set("seriesId", titleID)
		}
		if item.EpisodeTitle != "" {
			values.Set("episodeName", item.EpisodeTitle)
		}
		if item.SeasonNumber > 0 {
			values.Set("seasonNumber", strconv.Itoa(item.SeasonNumber))
		}
		if item.EpisodeNumber > 0 {
			values.Set("episodeNumber", strconv.Itoa(item.EpisodeNumber))
		}
	} else if item.Title != "" {
		values.Set("movieName", item.Title)
	}
	for key, value := range externalMap(item.ExternalIDs) {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return &models.LocalMediaPlaybackResponse{ItemID: item.ID, FileName: item.FileName, DisplayName: item.Title, TitleID: titleID, Title: item.Title, SeriesTitle: seriesTitle, EpisodeTitle: item.EpisodeTitle, Year: item.Year, DurationSeconds: item.DurationSeconds, ExternalIDs: externalMap(item.ExternalIDs), StreamPath: item.StreamPath, StreamURL: "/api/video/stream?" + values.Encode(), DirectStream: true, SourceType: library.Provider, SourceName: strings.Title(library.Provider)}, nil
}

// remoteCatalogTitleID returns a provider-prefixed catalog title id when the
// remote item has IMDB/TMDB/TVDB tags. Untagged library media returns empty so
// clients key progress by stream path instead of a bare library rating key.
func remoteCatalogTitleID(item *models.RemoteMediaItem) string {
	if item == nil {
		return ""
	}
	ids := item.ExternalIDs
	if ids == nil {
		return ""
	}
	imdb := strings.TrimSpace(ids.IMDB)
	tmdb := strings.TrimSpace(ids.TMDB)
	tvdb := strings.TrimSpace(ids.TVDB)
	if imdb == "" && tmdb == "" && tvdb == "" {
		return ""
	}
	isShow := item.LibraryType == models.LocalMediaLibraryTypeShow
	if tmdb != "" {
		if isShow {
			return "tmdb:tv:" + tmdb
		}
		return "tmdb:movie:" + tmdb
	}
	if tvdb != "" {
		if isShow {
			return "tvdb:series:" + tvdb
		}
		return "tvdb:movie:" + tvdb
	}
	return imdb
}

func externalMap(ids *models.LocalMediaExternalIDs) map[string]string {
	m := map[string]string{}
	if ids != nil {
		if ids.IMDB != "" {
			m["imdb"] = ids.IMDB
		}
		if ids.TMDB != "" {
			m["tmdb"] = ids.TMDB
		}
		if ids.TVDB != "" {
			m["tvdb"] = ids.TVDB
		}
	}
	return m
}

type Provider struct{ service *Service }

func NewProvider(service *Service) *Provider { return &Provider{service: service} }
func (p *Provider) GetDuration(ctx context.Context, path string) (float64, error) {
	if p == nil || p.service == nil {
		return 0, streaming.ErrNotFound
	}
	prefix := ""
	if strings.HasPrefix(path, "plexmedia:") {
		prefix = "plexmedia:"
	} else if strings.HasPrefix(path, "jellyfinmedia:") {
		prefix = "jellyfinmedia:"
	} else {
		return 0, streaming.ErrNotFound
	}
	id := strings.TrimSpace(strings.TrimPrefix(path, prefix))
	if slash := strings.IndexByte(id, '/'); slash >= 0 {
		id = id[:slash]
	}
	item, err := p.service.repo.GetItem(ctx, id)
	if err != nil || item == nil || item.DurationSeconds <= 0 {
		return 0, streaming.ErrNotFound
	}
	return item.DurationSeconds, nil
}
func (p *Provider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	prefix := ""
	provider := ""
	if strings.HasPrefix(req.Path, "plexmedia:") {
		prefix = "plexmedia:"
		provider = models.MediaSourcePlex
	} else if strings.HasPrefix(req.Path, "jellyfinmedia:") {
		prefix = "jellyfinmedia:"
		provider = models.MediaSourceJellyfin
	} else {
		return nil, streaming.ErrNotFound
	}
	id := strings.TrimSpace(strings.TrimPrefix(req.Path, prefix))
	if slash := strings.IndexByte(id, '/'); slash >= 0 {
		id = id[:slash]
	}
	item, err := p.service.repo.GetItem(ctx, id)
	if err != nil || item == nil {
		return nil, streaming.ErrNotFound
	}
	library, err := p.service.repo.GetLibrary(ctx, item.LibraryID)
	if err != nil || library == nil || library.Provider != provider {
		return nil, streaming.ErrNotFound
	}
	resp, err := p.service.openStream(ctx, library, item, req.Method, req.RangeHeader)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition"} {
		if value := resp.Header.Get(key); value != "" {
			headers.Set(key, value)
		}
	}
	length, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return &streaming.Response{Body: resp.Body, Headers: headers, Status: resp.StatusCode, ContentLength: length, Filename: item.FileName}, nil
}
func (s *Service) openStream(ctx context.Context, library *models.RemoteMediaLibrary, item *models.RemoteMediaItem, method, rangeHeader string) (*http.Response, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, err
	}
	if method == "" {
		method = http.MethodGet
	}
	if library.Provider == models.MediaSourceJellyfin {
		account := settings.Jellyfin.GetAccountByID(library.AccountID)
		if account == nil {
			return nil, ErrNotFound
		}
		return s.jellyfin.OpenStream(ctx, account.ServerURL, account.Token, item.ExternalItemID, item.ExternalMediaID, method, rangeHeader)
	}
	account := settings.Plex.GetAccountByID(library.AccountID)
	if account == nil {
		return nil, ErrNotFound
	}
	server, err := s.plexServerForLibrary(library, account.AuthToken)
	if err != nil {
		return nil, err
	}
	partKey := ""
	if item.ProviderData != nil {
		partKey = strings.TrimSpace(item.ProviderData["partKey"])
	}
	if partKey == "" {
		// Backup restore used to drop providerData (json:"-"); repair on demand from Plex.
		repaired, repairErr := s.repairPlexPartKey(ctx, library, item, server)
		if repairErr != nil {
			return nil, fmt.Errorf("Plex item %s missing partKey: %w (re-sync the remote library)", item.ID, repairErr)
		}
		partKey = repaired
	}
	return s.plex.OpenServerPath(ctx, server, partKey, method, rangeHeader)
}

// repairPlexPartKey re-fetches Plex metadata for a single item and persists partKey
// (and poster/backdrop paths) when they were lost (e.g. backup restore without providerData).
func (s *Service) repairPlexPartKey(ctx context.Context, library *models.RemoteMediaLibrary, item *models.RemoteMediaItem, server plex.PlexResource) (string, error) {
	if s == nil || s.plex == nil || item == nil {
		return "", errors.New("unavailable")
	}
	ratingKey := strings.TrimSpace(item.ExternalItemID)
	if ratingKey == "" {
		return "", errors.New("item has no external rating key")
	}
	meta, err := s.plex.GetServerMetadata(ctx, server, ratingKey)
	if err != nil {
		return "", err
	}
	normalized := normalizePlex(library, []plex.PlexLibraryItem{*meta})
	var match *models.RemoteMediaItem
	for i := range normalized {
		if normalized[i].ExternalMediaID == item.ExternalMediaID ||
			normalized[i].ID == item.ID ||
			(item.ExternalMediaID == "" && len(normalized) == 1) {
			match = &normalized[i]
			break
		}
	}
	if match == nil && len(normalized) > 0 {
		// Fall back to first part when media ID is missing from stored row.
		match = &normalized[0]
	}
	if match == nil {
		return "", errors.New("item not found during repair")
	}
	partKey := strings.TrimSpace(match.ProviderData["partKey"])
	if partKey == "" {
		return "", errors.New("Plex metadata has empty part key")
	}
	if item.ProviderData == nil {
		item.ProviderData = map[string]string{}
	}
	for k, v := range match.ProviderData {
		item.ProviderData[k] = v
	}
	item.LibraryID = library.ID
	item.UpdatedAt = time.Now().UTC()
	if err := s.repo.UpsertItem(ctx, item); err != nil {
		log.Printf("[remotemedia] failed to persist repaired partKey for %s: %v", item.ID, err)
		// Still return the key so this request can stream.
	} else {
		log.Printf("[remotemedia] repaired partKey for plex item %s", item.ID)
	}
	return partKey, nil
}

func (s *Service) OpenArtwork(ctx context.Context, itemID, kind string) (*http.Response, error) {
	item, err := s.repo.GetItem(ctx, itemID)
	if err != nil || item == nil {
		return nil, ErrNotFound
	}
	library, err := s.repo.GetLibrary(ctx, item.LibraryID)
	if err != nil || library == nil {
		return nil, ErrNotFound
	}
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, err
	}
	if library.Provider == models.MediaSourceJellyfin {
		account := settings.Jellyfin.GetAccountByID(library.AccountID)
		if account == nil {
			return nil, ErrNotFound
		}
		target := item.ExternalItemID
		if item.LibraryType == models.LocalMediaLibraryTypeShow && item.GroupKey != "" {
			target = item.GroupKey
		}
		imageKind := "Primary"
		if kind == "backdrop" {
			imageKind = "Backdrop"
		}
		return s.jellyfin.OpenImage(ctx, account.ServerURL, account.Token, target, imageKind)
	}
	account := settings.Plex.GetAccountByID(library.AccountID)
	if account == nil {
		return nil, ErrNotFound
	}
	path := item.ProviderData["posterPath"]
	if kind == "backdrop" {
		path = item.ProviderData["backdropPath"]
	}
	server, err := s.plexServerForLibrary(library, account.AuthToken)
	if err != nil {
		return nil, err
	}
	return s.plex.OpenServerPath(ctx, server, path, http.MethodGet, "")
}

func CopyArtwork(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
