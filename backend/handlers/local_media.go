package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/kids"
	"novastream/services/libraryaccess"
	"novastream/services/localmedia"
	"novastream/services/remotemedia"

	"github.com/gorilla/mux"
)

type localMediaService interface {
	GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error)
	ProbeItemForPlayback(ctx context.Context, item *models.LocalMediaItem) (*models.LocalMediaProbe, error)
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
	ListGroups(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaGroupListResult, error)
	FindMatches(ctx context.Context, query models.LocalMediaMatchQuery) ([]models.LocalMediaMatchedGroup, error)
	FindLibraryCopies(ctx context.Context, query models.LocalMediaMatchQuery, season, episode, absolute int) ([]localmedia.LibraryCopy, error)
}

type localMediaUsersProvider interface {
	BelongsToAccount(profileID, accountID string) bool
	Get(id string) (models.User, bool)
}

type LocalMediaHandler struct {
	service         localMediaService
	usersSvc        localMediaUsersProvider
	transmuxEnabled bool

	metadata     metadataService
	cfgManager   *config.Manager
	userSettings userSettingsProvider
	remote       *remotemedia.Service
	access       *libraryaccess.Service
}

func (h *LocalMediaHandler) SetRemoteMediaService(service *remotemedia.Service) { h.remote = service }
func (h *LocalMediaHandler) SetLibraryAccessService(service *libraryaccess.Service) {
	h.access = service
}

func NewLocalMediaHandler(service localMediaService, usersSvc localMediaUsersProvider, transmuxEnabled bool) *LocalMediaHandler {
	return &LocalMediaHandler{
		service:         service,
		usersSvc:        usersSvc,
		transmuxEnabled: transmuxEnabled,
	}
}

// SetMetadataLanguageProviders wires the metadata service and language sources
// used to re-localize artwork on group listings for the requesting profile.
func (h *LocalMediaHandler) SetMetadataLanguageProviders(metadata metadataService, cfg *config.Manager, userSettings userSettingsProvider) {
	h.metadata = metadata
	h.cfgManager = cfg
	h.userSettings = userSettings
}

func (h *LocalMediaHandler) ListLibraries(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}
	libraries, err := h.service.ListLibraries(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if libraries == nil {
		libraries = []models.LocalMediaLibrary{}
	}
	for i := range libraries {
		libraries[i].SourceType = models.MediaSourceLocal
		libraries[i].SourceName = "Local"
	}
	if h.remote != nil {
		remoteLibraries, remoteErr := h.remote.ListLibraries(r.Context())
		if remoteErr != nil {
			http.Error(w, remoteErr.Error(), http.StatusInternalServerError)
			return
		}
		for _, remote := range remoteLibraries {
			libraries = append(libraries, models.LocalMediaLibrary{ID: remote.ID, Name: remote.Name, Type: remote.Type, CreatedAt: remote.CreatedAt, UpdatedAt: remote.UpdatedAt, LastScanStartedAt: remote.LastSyncStartedAt, LastScanFinishedAt: remote.LastSyncFinishedAt, LastScanStatus: remote.LastSyncStatus, LastScanError: remote.LastSyncError, LastScanTotal: remote.LastSyncTotal, LastScanDiscovered: remote.LastSyncTotal, LastScanMatched: remote.LastSyncTotal, SourceType: remote.Provider, SourceName: strings.Title(remote.Provider), SourceServerName: remote.ServerName})
		}
	}
	if h.access != nil {
		accountID, profileID, ok := h.librarySubject(w, r)
		if !ok {
			return
		}
		isMaster := auth.IsMaster(r)
		filtered := libraries[:0]
		for _, library := range libraries {
			allowed, accessErr := h.access.CanAccess(r.Context(), library.ID, accountID, profileID, isMaster)
			if accessErr != nil {
				http.Error(w, accessErr.Error(), http.StatusInternalServerError)
				return
			}
			if allowed {
				filtered = append(filtered, library)
			}
		}
		libraries = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(libraries)
}

func (h *LocalMediaHandler) librarySubject(w http.ResponseWriter, r *http.Request) (accountID, profileID string, ok bool) {
	accountID = auth.GetAccountID(r)
	profileID = strings.TrimSpace(r.URL.Query().Get("profileId"))
	if profileID == "" {
		profileID = strings.TrimSpace(r.URL.Query().Get("userId"))
	}
	if profileID == "" {
		return accountID, profileID, true
	}
	if accountID == "" || h.usersSvc == nil || !h.usersSvc.BelongsToAccount(profileID, accountID) {
		http.Error(w, "library not found", http.StatusNotFound)
		return "", "", false
	}
	return accountID, profileID, true
}

func (h *LocalMediaHandler) requireLibraryAccess(w http.ResponseWriter, r *http.Request, libraryID string) bool {
	if h.access == nil {
		return true
	}
	accountID, profileID, ok := h.librarySubject(w, r)
	if !ok {
		return false
	}
	allowed, err := h.access.CanAccess(r.Context(), libraryID, accountID, profileID, auth.IsMaster(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if !allowed {
		http.Error(w, "library not found", http.StatusNotFound)
		return false
	}
	return true
}

// kidsRatingCaps resolves the (movie, tv) rating caps for the requesting
// profile when it is a kids profile in rating mode. Returns empty strings when
// no rating restriction applies.
func (h *LocalMediaHandler) kidsRatingCaps(r *http.Request) (movieRating, tvRating string) {
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if profileID == "" {
		profileID = strings.TrimSpace(r.URL.Query().Get("userId"))
	}
	return h.profileKidsRatingCaps(profileID)
}

// profileKidsRatingCaps are a kids profile's rating caps ("" when none).
func (h *LocalMediaHandler) profileKidsRatingCaps(profileID string) (movieRating, tvRating string) {
	if h.usersSvc == nil || profileID == "" {
		return "", ""
	}
	user, ok := h.usersSvc.Get(profileID)
	if ok && isCuratedKidsUser(user) {
		// Curated (content_list) profiles may only see their allowed lists,
		// which library items cannot be matched against here: hide them all.
		return kidsBlockAllRating, kidsBlockAllRating
	}
	if !ok || !user.IsKidsProfile || user.KidsMode != "rating" {
		return "", ""
	}
	movieRating = user.KidsMaxMovieRating
	tvRating = user.KidsMaxTVRating
	if movieRating == "" && tvRating == "" && user.KidsMaxRating != "" {
		movieRating = user.KidsMaxRating
		tvRating = user.KidsMaxRating
	}
	return movieRating, tvRating
}

func (h *LocalMediaHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	libraryID := strings.TrimSpace(mux.Vars(r)["libraryID"])
	if libraryID == "" {
		http.Error(w, "missing library ID", http.StatusBadRequest)
		return
	}
	if !h.requireLibraryAccess(w, r, libraryID) {
		return
	}
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	offset, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("offset")))

	// Apply kids profile rating caps so restricted profiles never see local
	// content above their limit (or content we can't verify a rating for).
	maxMovieRating, maxTVRating := h.kidsRatingCaps(r)

	log.Printf("[localmedia] ListGroups: libraryID=%s limit=%d offset=%d filter=%q sort=%q", libraryID, limit, offset, r.URL.Query().Get("filter"), r.URL.Query().Get("sort"))
	t0 := time.Now()
	listQuery := models.LocalMediaItemListQuery{
		Filter:         r.URL.Query().Get("filter"),
		Sort:           r.URL.Query().Get("sort"),
		Dir:            r.URL.Query().Get("dir"),
		Query:          r.URL.Query().Get("query"),
		MediaType:      r.URL.Query().Get("mediaType"),
		Alphabet:       r.URL.Query().Get("alphabet"),
		Limit:          limit,
		Offset:         offset,
		IncludeCards:   strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include")), "cards"),
		MaxMovieRating: maxMovieRating,
		MaxTVRating:    maxTVRating,
	}
	var groups *models.LocalMediaGroupListResult
	var err error
	if h.remote != nil {
		if remoteLibrary, remoteErr := h.remote.GetLibrary(r.Context(), libraryID); remoteErr != nil {
			err = remoteErr
		} else if remoteLibrary != nil {
			groups, err = h.remote.ListGroups(r.Context(), libraryID, listQuery)
		}
	}
	if groups == nil && err == nil {
		groups, err = h.service.ListGroups(r.Context(), libraryID, listQuery)
	}
	if err != nil {
		log.Printf("[localmedia] ListGroups: libraryID=%s error after %s: %v", libraryID, time.Since(t0).Round(time.Millisecond), err)
		switch {
		case errors.Is(err, localmedia.ErrLibraryNotFound):
			http.Error(w, "library not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	if groups == nil {
		groups = &models.LocalMediaGroupListResult{Groups: []models.LocalMediaItemGroup{}}
	}

	// Stored local-media metadata (including poster artwork) is resolved once at
	// scan time in the global metadata language. Re-localize artwork for profiles
	// whose effective language differs so shelves match the profile's setting.
	h.localizeGroupArtwork(r, groups.Groups)

	log.Printf("[localmedia] ListGroups: libraryID=%s returned %d groups (total=%d) in %s", libraryID, len(groups.Groups), groups.Total, time.Since(t0).Round(time.Millisecond))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(groups)
}

// localizeGroupArtwork actively fetches localized TMDB artwork for each group
// when the requesting profile's metadata language differs from the global
// default the stored artwork was resolved in. It is a no-op for default-language
// profiles so the common path keeps serving the baked-in artwork.
func (h *LocalMediaHandler) localizeGroupArtwork(r *http.Request, groups []models.LocalMediaItemGroup) {
	if len(groups) == 0 || h.metadata == nil || h.cfgManager == nil {
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		userID = strings.TrimSpace(r.URL.Query().Get("profileId"))
	}
	if userID == "" {
		return
	}
	settings, err := h.cfgManager.Load()
	if err != nil {
		return
	}
	if _, isDefault := resolveMetadataLanguage(settings, h.userSettings, userID); isDefault {
		return
	}
	svc := metadataServiceForUser(h.metadata, h.cfgManager, h.userSettings, userID)
	if svc == nil {
		return
	}

	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for i := range groups {
		g := &groups[i]
		if g.TMDBID <= 0 && g.TVDBID <= 0 && strings.TrimSpace(g.IMDBID) == "" {
			continue
		}
		wg.Add(1)
		go func(g *models.LocalMediaItemGroup) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mediaType := "series"
			if g.LibraryType == models.LocalMediaLibraryTypeMovie {
				mediaType = "movie"
			}
			title := models.Title{
				MediaType:    mediaType,
				TMDBID:       g.TMDBID,
				TVDBID:       g.TVDBID,
				IMDBID:       g.IMDBID,
				Poster:       g.Poster,
				TextPoster:   g.TextPoster,
				Backdrop:     g.Backdrop,
				TextBackdrop: g.TextBackdrop,
			}
			if svc.ApplyLocalizedArtwork(r.Context(), &title) {
				g.Poster = title.Poster
				g.TextPoster = title.TextPoster
				g.Backdrop = title.Backdrop
				g.TextBackdrop = title.TextBackdrop
				if title.TMDBID > 0 {
					g.TMDBID = title.TMDBID
				}
			}
		}(g)
	}
	wg.Wait()
}

func (h *LocalMediaHandler) FindMatches(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}

	year, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("year")))
	matches, err := h.service.FindMatches(r.Context(), models.LocalMediaMatchQuery{
		MediaType: strings.TrimSpace(r.URL.Query().Get("mediaType")),
		TitleID:   strings.TrimSpace(r.URL.Query().Get("titleId")),
		Title:     strings.TrimSpace(r.URL.Query().Get("title")),
		Year:      year,
		IMDBID:    strings.TrimSpace(r.URL.Query().Get("imdbId")),
		TMDBID:    strings.TrimSpace(r.URL.Query().Get("tmdbId")),
		TVDBID:    strings.TrimSpace(r.URL.Query().Get("tvdbId")),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if matches == nil {
		matches = []models.LocalMediaMatchedGroup{}
	}
	if h.remote != nil {
		remoteMatches, remoteErr := h.remote.FindMatches(r.Context(), models.LocalMediaMatchQuery{MediaType: strings.TrimSpace(r.URL.Query().Get("mediaType")), TitleID: strings.TrimSpace(r.URL.Query().Get("titleId")), Title: strings.TrimSpace(r.URL.Query().Get("title")), Year: year, IMDBID: strings.TrimSpace(r.URL.Query().Get("imdbId")), TMDBID: strings.TrimSpace(r.URL.Query().Get("tmdbId")), TVDBID: strings.TrimSpace(r.URL.Query().Get("tvdbId"))})
		if remoteErr != nil {
			http.Error(w, remoteErr.Error(), http.StatusInternalServerError)
			return
		}
		matches = append(matches, remoteMatches...)
	}
	if h.access != nil {
		accountID, profileID, ok := h.librarySubject(w, r)
		if !ok {
			return
		}
		isMaster := auth.IsMaster(r)
		filtered := matches[:0]
		for _, match := range matches {
			allowed, accessErr := h.access.CanAccess(r.Context(), match.LibraryID, accountID, profileID, isMaster)
			if accessErr != nil {
				http.Error(w, accessErr.Error(), http.StatusInternalServerError)
				return
			}
			if allowed {
				filtered = append(filtered, match)
			}
		}
		matches = filtered
	}
	// The same kids rating caps the library listings apply, for local, Plex and
	// Jellyfin matches alike.
	if maxMovieRating, maxTVRating := h.kidsRatingCaps(r); maxMovieRating != "" || maxTVRating != "" {
		filtered := matches[:0]
		for _, match := range matches {
			if kids.IsLibraryGroupAllowed(match.Group, maxMovieRating, maxTVRating) {
				filtered = append(filtered, match)
			}
		}
		matches = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(matches)
}

func (h *LocalMediaHandler) GetPlayback(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(mux.Vars(r)["itemID"])
	if itemID == "" {
		http.Error(w, "missing item ID", http.StatusBadRequest)
		return
	}
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}

	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	profileName := strings.TrimSpace(r.URL.Query().Get("profileName"))
	if profileID != "" {
		accountID := auth.GetAccountID(r)
		if accountID == "" || h.usersSvc == nil || !h.usersSvc.BelongsToAccount(profileID, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	}
	if h.access != nil {
		libraryID, accessErr := h.access.LibraryIDForItem(r.Context(), itemID)
		if accessErr != nil {
			http.Error(w, accessErr.Error(), http.StatusInternalServerError)
			return
		}
		if libraryID != "" && !h.requireLibraryAccess(w, r, libraryID) {
			return
		}
	}

	item, err := h.service.GetItem(r.Context(), itemID)
	if errors.Is(err, localmedia.ErrItemNotFound) && h.remote != nil {
		playback, remoteErr := h.remote.Playback(r.Context(), itemID)
		if remoteErr == nil {
			if playbackURL, parseErr := url.Parse(playback.StreamURL); parseErr == nil {
				query := playbackURL.Query()
				if profileID != "" {
					query.Set("profileId", profileID)
				}
				if profileName != "" {
					query.Set("profileName", profileName)
				}
				playbackURL.RawQuery = query.Encode()
				playback.StreamURL = playbackURL.String()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(playback)
			return
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, localmedia.ErrItemNotFound), errors.Is(err, localmedia.ErrLibraryNotFound):
			http.Error(w, "local media item not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	probe, err := h.service.ProbeItemForPlayback(r.Context(), item)
	if err != nil {
		log.Printf("[localmedia] playback probe failed item=%s file=%q err=%v", item.ID, item.FilePath, err)
		switch {
		case errors.Is(err, localmedia.ErrLocalMediaNotPlayable):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		case errors.Is(err, localmedia.ErrItemNotFound), errors.Is(err, localmedia.ErrLibraryNotFound):
			http.Error(w, "local media item not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	item.Probe = probe

	streamPath := localmedia.BuildStreamPath(*item)
	query := url.Values{}
	query.Set("path", streamPath)
	query.Set("transmux", "0")
	if profileID != "" {
		query.Set("profileId", profileID)
	}
	if profileName != "" {
		query.Set("profileName", profileName)
	}

	displayName := strings.TrimSpace(item.MatchedName)
	if displayName == "" {
		displayName = strings.TrimSpace(item.DetectedTitle)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(item.FileName)
	}
	if displayName == "" {
		displayName = filepath.Base(strings.TrimSpace(item.FilePath))
	}
	if displayName != "" {
		query.Set("displayName", displayName)
	}

	titleID := strings.TrimSpace(item.MatchedTitleID)
	title := displayName
	seriesTitle := ""
	year := item.MatchedYear
	posterURL := ""
	backdropURL := ""
	episodeImageURL := ""
	externalIDs := make(map[string]string)

	if item.Metadata != nil {
		if strings.TrimSpace(item.Metadata.ID) != "" {
			titleID = strings.TrimSpace(item.Metadata.ID)
		}
		if strings.TrimSpace(item.Metadata.Name) != "" {
			title = strings.TrimSpace(item.Metadata.Name)
		}
		if item.Metadata.Year > 0 {
			year = item.Metadata.Year
		}
		if item.Metadata.Poster != nil {
			posterURL = item.Metadata.Poster.URL
		}
		if item.Metadata.Backdrop != nil {
			backdropURL = item.Metadata.Backdrop.URL
		}
		if item.Metadata.IMDBID != "" {
			externalIDs["imdb"] = item.Metadata.IMDBID
		}
		if item.Metadata.TMDBID > 0 {
			externalIDs["tmdb"] = strconv.FormatInt(item.Metadata.TMDBID, 10)
		}
		if item.Metadata.TVDBID > 0 {
			externalIDs["tvdb"] = strconv.FormatInt(item.Metadata.TVDBID, 10)
		}
	}

	if item.ExternalIDs != nil {
		if item.ExternalIDs.IMDB != "" && externalIDs["imdb"] == "" {
			externalIDs["imdb"] = item.ExternalIDs.IMDB
		}
		if item.ExternalIDs.TMDB != "" && externalIDs["tmdb"] == "" {
			externalIDs["tmdb"] = item.ExternalIDs.TMDB
		}
		if item.ExternalIDs.TVDB != "" && externalIDs["tvdb"] == "" {
			externalIDs["tvdb"] = item.ExternalIDs.TVDB
		}
	}

	if item.LibraryType == models.LocalMediaLibraryTypeShow || item.SeasonNumber > 0 || item.EpisodeNumber > 0 {
		seriesTitle = title
	}
	if item.EpisodeImage != nil {
		episodeImageURL = item.EpisodeImage.URL
	}
	if title == "" {
		title = displayName
	}

	mediaType := strings.TrimSpace(item.MatchedMediaType)
	if mediaType == "" {
		switch item.LibraryType {
		case models.LocalMediaLibraryTypeShow:
			mediaType = "episode"
		case models.LocalMediaLibraryTypeMovie:
			mediaType = "movie"
		}
	}
	if mediaType == "series" && (item.LibraryType == models.LocalMediaLibraryTypeShow || item.SeasonNumber > 0 || item.EpisodeNumber > 0) {
		mediaType = "episode"
	}
	if mediaType != "" {
		query.Set("mediaType", mediaType)
	}

	streamItemID := strings.TrimSpace(titleID)
	if mediaType == "episode" && streamItemID != "" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
		streamItemID = streamItemID + ":" + formatSeasonEpisode(item.SeasonNumber, item.EpisodeNumber)
	}
	if streamItemID == "" {
		streamItemID = strings.TrimSpace(item.ID)
	}
	if streamItemID != "" {
		query.Set("itemId", streamItemID)
	}
	if title != "" {
		query.Set("title", title)
	}
	if year > 0 {
		query.Set("year", strconv.Itoa(year))
	}
	if season := item.SeasonNumber; season > 0 {
		query.Set("seasonNumber", strconv.Itoa(season))
	}
	if episode := item.EpisodeNumber; episode > 0 {
		query.Set("episodeNumber", strconv.Itoa(episode))
	}
	if mediaType == "episode" {
		if seriesTitle != "" {
			query.Set("seriesName", seriesTitle)
			query.Set("title", seriesTitle)
		}
		if titleID != "" {
			query.Set("seriesId", titleID)
		}
		if item.EpisodeTitle != "" {
			query.Set("episodeName", item.EpisodeTitle)
		}
	} else if mediaType == "movie" && title != "" {
		query.Set("movieName", title)
	}
	for key, value := range externalIDs {
		if strings.TrimSpace(value) != "" {
			query.Set(key, value)
		}
	}
	if probe.DurationSeconds > 0 {
		query.Set("durationHint", strconv.FormatFloat(probe.DurationSeconds, 'f', -1, 64))
	}

	resp := models.LocalMediaPlaybackResponse{
		ItemID:          item.ID,
		FileName:        item.FileName,
		DisplayName:     displayName,
		TitleID:         titleID,
		Title:           title,
		SeriesTitle:     seriesTitle,
		EpisodeTitle:    item.EpisodeTitle,
		Year:            year,
		DurationSeconds: probe.DurationSeconds,
		PosterURL:       posterURL,
		BackdropURL:     backdropURL,
		EpisodeImage:    episodeImageURL,
		ExternalIDs:     externalIDs,
		StreamPath:      streamPath,
		StreamURL:       "/api/video/stream?" + query.Encode(),
		DirectStream:    true,
		SourceType:      models.MediaSourceLocal,
		SourceName:      "Local",
	}
	if h.transmuxEnabled {
		resp.HLSAvailable = true
		resp.HLSStartURL = "/api/video/hls/start?" + query.Encode()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *LocalMediaHandler) GetArtwork(w http.ResponseWriter, r *http.Request) {
	if h.remote == nil {
		http.NotFound(w, r)
		return
	}
	if h.access != nil {
		libraryID, err := h.access.LibraryIDForItem(r.Context(), strings.TrimSpace(mux.Vars(r)["itemID"]))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if libraryID == "" || !h.requireLibraryAccess(w, r, libraryID) {
			return
		}
	}
	resp, err := h.remote.OpenArtwork(r.Context(), strings.TrimSpace(mux.Vars(r)["itemID"]), strings.TrimSpace(mux.Vars(r)["kind"]))
	if err != nil {
		http.Error(w, "artwork not found", http.StatusNotFound)
		return
	}
	remotemedia.CopyArtwork(w, resp)
}

// LibraryCopyQuery names a catalog title (and, for a series, an episode) to
// look up in the local libraries.
type LibraryCopyQuery struct {
	MediaType string // movie or series
	TitleID   string
	Title     string
	Year      int
	IMDBID    string
	TMDBID    string
	TVDBID    string
	// Season and Episode name the episode of a series; 0 means any. Absolute
	// is its absolute number (anime), when known.
	Season, Episode, Absolute int
}

// LibraryCopy is a profile's copy of a catalog title in a local library.
type LibraryCopy struct {
	ItemID      string
	StreamPath  string
	Name        string
	LibraryName string
	SizeBytes   int64
}

// FindLibraryCopy returns the profile's copy of a catalog title (or episode)
// in a local library, nil when it has none. Libraries the profile may not
// open and titles above a kids profile's rating cap do not count, as in the
// library listings. Plex and Jellyfin libraries are not included: they play
// through their own server. Of several files the largest counts. Without a
// request (a scheduled pre-warm) the profile's own account decides.
func (h *LocalMediaHandler) FindLibraryCopy(r *http.Request, profileID string, q LibraryCopyQuery) (*LibraryCopy, error) {
	if h == nil || h.service == nil {
		return nil, nil
	}
	if strings.TrimSpace(q.TitleID) == "" && strings.TrimSpace(q.Title) == "" && q.IMDBID == "" && q.TMDBID == "" && q.TVDBID == "" {
		return nil, nil // nothing to look up
	}
	query := localmedia.WithTitleIDExternalIDs(models.LocalMediaMatchQuery{MediaType: q.MediaType, TitleID: strings.TrimSpace(q.TitleID), Title: strings.TrimSpace(q.Title), Year: q.Year,
		IMDBID: strings.TrimSpace(q.IMDBID), TMDBID: strings.TrimSpace(q.TMDBID), TVDBID: strings.TrimSpace(q.TVDBID)})
	ctx, accountID, isMaster := context.Background(), "", false
	if r != nil {
		ctx, accountID, isMaster = r.Context(), auth.GetAccountID(r), auth.IsMaster(r)
	} else if h.usersSvc != nil {
		if user, ok := h.usersSvc.Get(profileID); ok {
			accountID = user.AccountID
		}
	}
	copies, err := h.service.FindLibraryCopies(ctx, query, q.Season, q.Episode, q.Absolute)
	if err != nil || len(copies) == 0 {
		return nil, err
	}
	// A master key may act for any profile; others only for their account's.
	if profileID != "" && h.access != nil && !isMaster && (accountID == "" || h.usersSvc == nil || !h.usersSvc.BelongsToAccount(profileID, accountID)) {
		return nil, nil
	}
	movieCap, tvCap := h.profileKidsRatingCaps(profileID)
	best, err := localmedia.PickLibraryCopy(copies, func(c localmedia.LibraryCopy) (bool, error) {
		if h.access != nil {
			if allowed, err := h.access.CanAccess(ctx, c.LibraryID, accountID, profileID, isMaster); err != nil || !allowed {
				return false, err
			}
		}
		if movieCap != "" || tvCap != "" {
			group := models.LocalMediaItemGroup{LibraryType: c.Item.LibraryType}
			if c.Item.Metadata != nil {
				group.Certification = strings.TrimSpace(c.Item.Metadata.Certification)
			}
			return kids.IsLibraryGroupAllowed(group, movieCap, tvCap), nil
		}
		return true, nil
	})
	if err != nil || best == nil {
		return nil, err
	}
	name := strings.TrimSpace(best.Item.FileName)
	if name == "" {
		name = filepath.Base(best.Item.FilePath)
	}
	return &LibraryCopy{ItemID: best.Item.ID, StreamPath: localmedia.BuildStreamPath(best.Item), Name: name, LibraryName: best.LibraryName, SizeBytes: best.Item.SizeBytes}, nil
}
