package handlers

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/kids"
	"novastream/services/localmedia"
	"novastream/services/playback"
)

type prequeueLocalMedia interface {
	FindPlaybackCandidates(context.Context, models.LocalMediaMatchQuery, *models.EpisodeReference, func(string) bool) ([]models.LocalMediaItem, error)
	GetFallbackItem(context.Context, string) (*models.LocalMediaItem, error)
}

type prequeueLibraryAccess interface {
	CanAccess(context.Context, string, string, string, bool) (bool, error)
}

type prequeueLocalScope struct {
	accountID string
	master    bool
	tmdbID    string
	tvdbID    string
}

func (h *PrequeueHandler) SetLocalFallback(service prequeueLocalMedia, access prequeueLibraryAccess) {
	h.localFallback, h.localAccess = service, access
}

func (h *PrequeueHandler) localPlaybackPreferred(userID string) bool {
	defaults := models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(false)}}
	if h.configManager != nil {
		if settings, err := h.configManager.Load(); err == nil {
			defaults.Playback.PreferLocalFiles = models.BoolPtr(settings.Playback.PreferLocalFiles)
		}
	}
	if h.userSettingsSvc != nil {
		if settings, err := h.userSettingsSvc.GetWithDefaults(userID, defaults); err == nil {
			return models.BoolVal(settings.Playback.PreferLocalFiles, false)
		}
	}
	return models.BoolVal(defaults.Playback.PreferLocalFiles, false)
}

func (h *PrequeueHandler) localItemAllowed(item models.LocalMediaItem, userID string) bool {
	profiles, ok := h.users.(interface {
		Get(string) (models.User, bool)
	})
	if !ok {
		return false
	}
	profile, ok := profiles.Get(userID)
	if !ok {
		return false
	}
	if !profile.IsKidsProfile {
		return true
	}
	// Content-list membership is not part of the local catalog. Do not silently
	// substitute a local item when its list authorization cannot be established.
	if profile.KidsMode != "rating" || item.Metadata == nil {
		return false
	}
	mediaType, limit := "movie", profile.KidsMaxMovieRating
	if item.LibraryType == models.LocalMediaLibraryTypeShow {
		mediaType, limit = "series", profile.KidsMaxTVRating
	}
	if limit == "" {
		limit = profile.KidsMaxRating
	}
	return limit != "" && kids.IsRatingAllowed(item.Metadata.Certification, limit, mediaType)
}

func (h *PrequeueHandler) findLocalFallbackCandidates(ctx context.Context, scopes []prequeueLocalScope, userID string, query models.LocalMediaMatchQuery, episode *models.EpisodeReference) []models.LocalMediaItem {
	if h.localFallback == nil || h.localAccess == nil || len(scopes) != 1 || scopes[0].accountID == "" {
		return nil
	}
	scope := scopes[0]
	if scope.tmdbID != "" {
		query.TMDBID = scope.tmdbID
	}
	if scope.tvdbID != "" {
		query.TVDBID = scope.tvdbID
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	items, err := h.localFallback.FindPlaybackCandidates(checkCtx, query, episode, func(libraryID string) bool {
		allowed, err := h.localAccess.CanAccess(checkCtx, libraryID, scope.accountID, userID, scope.master)
		return err == nil && allowed
	})
	if err != nil {
		return nil
	}
	filtered := items[:0]
	for _, item := range items {
		if h.localItemAllowed(item, userID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

var localFallbackProbes = make(chan struct{}, 8)

func (h *PrequeueHandler) resolveLocalItem(ctx context.Context, item models.LocalMediaItem, opts prequeueResolutionOptions) (*models.PlaybackResolution, error) {
	if h.localFallback == nil || h.fullProber == nil {
		return nil, errors.New("local playback validation unavailable")
	}
	current, err := h.localFallback.GetFallbackItem(ctx, item.ID)
	if err != nil || current == nil {
		return nil, errors.New("existing library file is missing or unavailable")
	}
	// A scan or manual rematch may have changed the catalog during remote search.
	if current.LibraryID != item.LibraryID || current.MatchedTitleID != item.MatchedTitleID || current.SeasonNumber != item.SeasonNumber || current.EpisodeNumber != item.EpisodeNumber || current.IsMissing || current.MatchStatus != item.MatchStatus || !reflect.DeepEqual(current.ExternalIDs, item.ExternalIDs) {
		return nil, errors.New("local library match changed; start playback again")
	}
	if opts.localScope == nil || h.localAccess == nil || !h.localItemAllowed(*current, opts.userID) {
		return nil, errors.New("local library file unavailable")
	}
	allowed, accessErr := h.localAccess.CanAccess(ctx, current.LibraryID, opts.localScope.accountID, opts.userID, opts.localScope.master)
	if accessErr != nil || !allowed {
		return nil, errors.New("local library file unavailable")
	}
	if !readyEntryFitsAdaptiveCaps(&playback.PrequeueEntry{FileSize: current.SizeBytes, MediaType: opts.mediaType}, opts.adaptiveCaps) {
		return nil, errors.New("local library file exceeds the current playback size limit")
	}
	limit := opts.localFilter.MaxSizeMovieGB
	if opts.mediaType != "movie" {
		limit = opts.localFilter.MaxSizeEpisodeGB
	}
	if limit != nil && *limit > 0 && float64(current.SizeBytes)/(1024*1024*1024) > *limit {
		return nil, errors.New("local library file exceeds the current playback size limit")
	}
	path := localmedia.BuildStreamPath(*current)
	select {
	case localFallbackProbes <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	type probeResult struct {
		probe *VideoFullResult
		err   error
	}
	done := make(chan probeResult, 1)
	go func() {
		defer func() { <-localFallbackProbes }()
		probe, err := h.fullProber.ProbeVideoFull(ctx, path)
		done <- probeResult{probe, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		if result.err != nil {
			return nil, errors.New("existing library file could not be validated")
		}
		if err := validateLocalFallbackQuality(result.probe, opts.localFilter); err != nil {
			return nil, err
		}
		return &models.PlaybackResolution{WebDAVPath: path, FileSize: current.SizeBytes, HealthStatus: "healthy", Probe: result.probe}, nil
	}
}

func (h *PrequeueHandler) resolveLocalFallback(ctx context.Context, prequeueID string, items []models.LocalMediaItem, opts prequeueResolutionOptions) (prequeueResolutionChoice, error) {
	localCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	opts.localItems = make(map[string]models.LocalMediaItem, len(items))
	var results []models.NZBResult
	for _, item := range items {
		path := localmedia.BuildStreamPath(item)
		opts.localItems[path] = item
		results = append(results, models.NZBResult{Title: item.FileName, Link: path, SizeBytes: item.SizeBytes, ServiceType: models.ContentServiceType("local")})
	}
	choice, err := h.resolveCandidates(localCtx, prequeueID, newSliceCandidateSource(results), opts)
	// Local catalog entries cannot be handed to the Usenet/debrid migration API.
	choice.migrationCandidates = nil
	if choice.resolution == nil && ctx.Err() == nil {
		return choice, errors.New("no usable search source or accessible matching local file; check the library and try again")
	}
	return choice, err
}

func validateLocalFallbackQuality(probe *VideoFullResult, settings models.FilterSettings) error {
	if err := validatePrequeueVideoProbe(probe); err != nil {
		return err
	}
	if settings.HDRDVPolicy == models.HDRDVPolicyNoExclusion && (probe.HasDolbyVision || probe.HasHDR10) {
		return errors.New("local library file exceeds the current HDR policy")
	}
	if settings.MaxResolution != nil && strings.TrimSpace(*settings.MaxResolution) != "" {
		resolution := strings.ToLower(strings.TrimSpace(*settings.MaxResolution))
		if resolution == "4k" {
			resolution = "2160p"
		}
		limit, _ := strconv.Atoi(strings.TrimSuffix(resolution, "p"))
		if limit > 0 && (probe.VideoWidth <= 0 || probe.VideoHeight <= 0 || probe.VideoHeight > limit || probe.VideoWidth > limit*16/9) {
			return errors.New("local library file exceeds the current resolution limit")
		}
	}
	return nil
}

func (h *PrequeueHandler) localEntryAllowed(r *http.Request, entry *playback.PrequeueEntry) bool {
	itemID, local := localmedia.ParseStreamPath(entry.StreamPath)
	if !local {
		return true
	}
	if h.localFallback == nil || h.localAccess == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	item, err := h.localFallback.GetFallbackItem(ctx, itemID)
	if err != nil || item == nil || !h.localItemAllowed(*item, entry.UserID) {
		return false
	}
	allowed, err := h.localAccess.CanAccess(ctx, item.LibraryID, auth.GetAccountID(r), entry.UserID, auth.IsMaster(r))
	return err == nil && allowed
}

func (h *PrequeueHandler) validateReadyEntryForRequest(r *http.Request, entry *playback.PrequeueEntry, requests ...playback.PrequeueRequest) error {
	if !h.localEntryAllowed(r, entry) {
		return errors.New("local library file unavailable")
	}
	if _, local := localmedia.ParseStreamPath(entry.StreamPath); !local && h.localPlaybackPreferred(entry.UserID) {
		query := models.LocalMediaMatchQuery{TitleID: entry.TitleID, MediaType: entry.MediaType}
		scope := prequeueLocalScope{accountID: auth.GetAccountID(r), master: auth.IsMaster(r)}
		if len(requests) == 1 {
			query.IMDBID = requests[0].ImdbID
			scope.tmdbID, scope.tvdbID = requests[0].TmdbID, requests[0].TvdbID
		}
		if len(h.findLocalFallbackCandidates(r.Context(), []prequeueLocalScope{scope}, entry.UserID, query, entry.TargetEpisode)) > 0 {
			return errors.New("local preference requires a fresh source selection")
		}
	}
	return h.validateReadyEntryForReuse(r.Context(), entry)
}

func positiveIDString(id int64) string {
	if id > 0 {
		return strconv.FormatInt(id, 10)
	}
	return ""
}
