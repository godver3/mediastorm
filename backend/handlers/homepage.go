package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"novastream/models"
	"novastream/services/accounts"
)

// HomepageMetadataService interface for fetching poster URLs
type HomepageMetadataService interface {
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
}

// HomepageStreamsProvider supplies the canonical active-stream view.
type HomepageStreamsProvider interface {
	ActiveStreams() StreamsResponse
}

// HomepageHandler provides stats for Homepage dashboard integration
type HomepageHandler struct {
	accounts        *accounts.Service
	userService     UserService
	streamsProvider HomepageStreamsProvider
	metadataService HomepageMetadataService
	apiKey          string // Required API key for authentication
}

// HomepageProfile represents a user profile for Homepage
type HomepageProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HomepageAccount represents an account with its profiles for Homepage
type HomepageAccount struct {
	ID       string            `json:"id"`
	Username string            `json:"username"`
	IsMaster bool              `json:"isMaster"`
	Profiles []HomepageProfile `json:"profiles"`
}

// HomepageStream represents an active stream for Homepage
type HomepageStream struct {
	ID              string    `json:"id"`
	Type            string    `json:"type"` // "hls" or "direct"
	Filename        string    `json:"filename"`
	ProfileName     string    `json:"profileName,omitempty"`
	ProfileNames    []string  `json:"profileNames,omitempty"`
	ClientIP        string    `json:"clientIp,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	Duration        float64   `json:"duration,omitempty"`
	CurrentPosition float64   `json:"currentPosition,omitempty"`
	PercentWatched  float64   `json:"percentWatched,omitempty"`
	HasDV           bool      `json:"hasDv"`
	HasHDR          bool      `json:"hasHdr"`
	// Media identification
	MediaType     string            `json:"mediaType,omitempty"` // "movie" or "episode"
	Title         string            `json:"title,omitempty"`
	Year          int               `json:"year,omitempty"`
	SeasonNumber  int               `json:"seasonNumber,omitempty"`
	EpisodeNumber int               `json:"episodeNumber,omitempty"`
	EpisodeName   string            `json:"episodeName,omitempty"`
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`
	PosterURL     string            `json:"posterUrl,omitempty"` // TMDB poster URL for display
}

// HomepageStats represents the stats returned to Homepage
type HomepageStats struct {
	Version       string            `json:"version"`
	ActiveStreams int               `json:"activeStreams"`
	TotalAccounts int               `json:"totalAccounts"`
	TotalProfiles int               `json:"totalProfiles"`
	Accounts      []HomepageAccount `json:"accounts"`
	Streams       []HomepageStream  `json:"streams"`
}

// DashboardShelfStream is the presentation-safe subset of an active dashboard
// stream exposed to authenticated app clients. It intentionally omits file
// paths, network addresses, user agents, and transport statistics.
type DashboardShelfStream struct {
	ID              string            `json:"id"`
	ItemID          string            `json:"itemId,omitempty"`
	ProfileNames    []string          `json:"profileNames,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	Duration        float64           `json:"duration,omitempty"`
	CurrentPosition float64           `json:"currentPosition,omitempty"`
	PercentWatched  float64           `json:"percentWatched,omitempty"`
	IsPaused        bool              `json:"isPaused"`
	Status          string            `json:"status"`
	MediaType       string            `json:"mediaType,omitempty"`
	Title           string            `json:"title,omitempty"`
	Year            int               `json:"year,omitempty"`
	SeasonNumber    int               `json:"seasonNumber,omitempty"`
	EpisodeNumber   int               `json:"episodeNumber,omitempty"`
	EpisodeName     string            `json:"episodeName,omitempty"`
	ExternalIDs     map[string]string `json:"externalIds,omitempty"`
	PosterURL       string            `json:"posterUrl,omitempty"`
	BackdropURL     string            `json:"backdropUrl,omitempty"`
	LiveSourceURL   string            `json:"liveSourceUrl,omitempty"`
	LiveSourceID    string            `json:"liveSourceId,omitempty"`
	LiveChannelLogo string            `json:"liveChannelLogo,omitempty"`
}

type DashboardShelfResponse struct {
	Streams []DashboardShelfStream `json:"streams"`
	Count   int                    `json:"count"`
}

// NewHomepageHandler creates a new Homepage handler
func NewHomepageHandler(accounts *accounts.Service) *HomepageHandler {
	return &HomepageHandler{
		accounts: accounts,
	}
}

// SetUserService sets the user service for profile lookup
func (h *HomepageHandler) SetUserService(svc UserService) {
	h.userService = svc
}

// SetStreamsProvider sets the canonical dashboard stream source.
func (h *HomepageHandler) SetStreamsProvider(provider HomepageStreamsProvider) {
	h.streamsProvider = provider
}

// SetMetadataService sets the metadata service for poster URL lookup
func (h *HomepageHandler) SetMetadataService(svc HomepageMetadataService) {
	h.metadataService = svc
}

// SetAPIKey sets the required API key for authentication
func (h *HomepageHandler) SetAPIKey(key string) {
	h.apiKey = key
}

// GetStats returns stats for Homepage dashboard widget
func (h *HomepageHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	// Validate API key - check query param or header
	providedKey := r.URL.Query().Get("apikey")
	if providedKey == "" {
		providedKey = r.Header.Get("X-API-Key")
	}
	if h.apiKey == "" || providedKey != h.apiKey {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Build account -> profiles map
	profilesByAccount := make(map[string][]HomepageProfile)
	totalProfiles := 0

	if h.userService != nil {
		for _, user := range h.userService.ListAll() {
			profilesByAccount[user.AccountID] = append(profilesByAccount[user.AccountID], HomepageProfile{
				ID:   user.ID,
				Name: user.Name,
			})
			totalProfiles++
		}
	}

	// Build accounts list with profiles
	var accountsList []HomepageAccount
	if h.accounts != nil {
		for _, acc := range h.accounts.List() {
			accountsList = append(accountsList, HomepageAccount{
				ID:       acc.ID,
				Username: acc.Username,
				IsMaster: acc.IsMaster,
				Profiles: profilesByAccount[acc.ID],
			})
		}
	}

	streamsList := make([]HomepageStream, 0)
	if h.streamsProvider != nil {
		active := h.streamsProvider.ActiveStreams()
		streamsList = make([]HomepageStream, 0, len(active.Streams))
		for _, source := range active.Streams {
			stream := homepageStreamFromDashboard(source)
			if h.metadataService != nil && stream.PosterURL == "" && stream.MediaType != "" && stream.Title != "" {
				stream.PosterURL = h.fetchPosterURL(r.Context(), homepageProgressForPoster(stream))
			}
			streamsList = append(streamsList, stream)
		}
	}

	stats := HomepageStats{
		Version:       GetBackendVersion(),
		ActiveStreams: len(streamsList),
		TotalAccounts: len(accountsList),
		TotalProfiles: totalProfiles,
		Accounts:      accountsList,
		Streams:       streamsList,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GetDashboardShelf returns active playback cards for authenticated app
// clients. The protected API router handles authentication.
func (h *HomepageHandler) GetDashboardShelf(w http.ResponseWriter, r *http.Request) {
	streams := make([]DashboardShelfStream, 0)
	if h.streamsProvider != nil {
		active := h.streamsProvider.ActiveStreams()
		privacy := newDashboardShelfPrivacyIndex(h.userService)
		streams = make([]DashboardShelfStream, 0, len(active.Streams))
		for _, source := range active.Streams {
			profileNames := privacy.visibleProfileNames(source)
			if len(profileNames) == 0 {
				continue
			}
			stream := dashboardShelfStreamFromDashboard(source, profileNames)
			if h.metadataService != nil && stream.MediaType != "" && stream.Title != "" {
				posterURL, backdropURL := h.fetchShelfArtwork(r.Context(), homepageProgressForShelfArtwork(stream))
				if stream.PosterURL == "" {
					stream.PosterURL = posterURL
				}
				stream.BackdropURL = backdropURL
			}
			streams = append(streams, stream)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DashboardShelfResponse{Streams: streams, Count: len(streams)})
}

type dashboardShelfPrivacyIndex struct {
	byID   map[string]models.User
	byName map[string]models.User
}

func newDashboardShelfPrivacyIndex(userService UserService) dashboardShelfPrivacyIndex {
	index := dashboardShelfPrivacyIndex{
		byID:   make(map[string]models.User),
		byName: make(map[string]models.User),
	}
	if userService == nil {
		return index
	}
	for _, user := range userService.ListAll() {
		index.byID[user.ID] = user
		if name := strings.ToLower(strings.TrimSpace(user.Name)); name != "" {
			index.byName[name] = user
		}
	}
	return index
}

func (i dashboardShelfPrivacyIndex) visibleProfileNames(source StreamInfo) []string {
	profileIDs := append([]string(nil), source.ProfileIDs...)
	profileNames := append([]string(nil), source.ProfileNames...)
	if len(profileIDs) == 0 && strings.TrimSpace(source.ProfileID) != "" {
		profileIDs = []string{source.ProfileID}
	}
	if len(profileNames) == 0 && strings.TrimSpace(source.ProfileName) != "" {
		profileNames = []string{source.ProfileName}
	}

	count := maxInt(len(profileIDs), len(profileNames))
	visible := make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	for idx := 0; idx < count; idx++ {
		var profileID, profileName string
		if idx < len(profileIDs) {
			profileID = strings.TrimSpace(profileIDs[idx])
		}
		if idx < len(profileNames) {
			profileName = strings.TrimSpace(profileNames[idx])
		}
		user, ok := i.byID[profileID]
		if !ok && profileName != "" {
			user, ok = i.byName[strings.ToLower(profileName)]
		}
		if !ok {
			continue
		}

		var displayName string
		switch models.NormalizeActivityPrivacy(user.ActivityPrivacy) {
		case models.ActivityPrivacyShared:
			displayName = strings.TrimSpace(user.Name)
		case models.ActivityPrivacySharedAnonymous:
			displayName = "Fellow user"
		default:
			continue
		}
		if displayName == "" {
			continue
		}
		key := strings.ToLower(displayName)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		visible = append(visible, displayName)
	}
	return visible
}

func dashboardShelfStreamFromDashboard(source StreamInfo, profileNames []string) DashboardShelfStream {
	status := "playing"
	if source.IsPaused {
		status = "paused"
	}
	return DashboardShelfStream{
		ID:              source.ID,
		ItemID:          source.ItemID,
		ProfileNames:    profileNames,
		CreatedAt:       source.CreatedAt,
		Duration:        source.Duration,
		CurrentPosition: source.CurrentPosition,
		PercentWatched:  source.PercentWatched,
		IsPaused:        source.IsPaused,
		Status:          status,
		MediaType:       source.MediaType,
		Title:           source.Title,
		Year:            source.Year,
		SeasonNumber:    source.SeasonNumber,
		EpisodeNumber:   source.EpisodeNumber,
		EpisodeName:     source.EpisodeName,
		ExternalIDs:     source.ExternalIDs,
		PosterURL:       source.PosterURL,
		LiveSourceURL:   source.LiveSourceURL,
		LiveSourceID:    source.LiveSourceID,
		LiveChannelLogo: source.LiveChannelLogo,
	}
}

func homepageProgressForShelfArtwork(stream DashboardShelfStream) *models.PlaybackProgress {
	progress := &models.PlaybackProgress{
		ItemID:        stream.ItemID,
		MediaType:     stream.MediaType,
		ExternalIDs:   stream.ExternalIDs,
		SeasonNumber:  stream.SeasonNumber,
		EpisodeNumber: stream.EpisodeNumber,
		EpisodeName:   stream.EpisodeName,
		Year:          stream.Year,
	}
	if stream.MediaType == "episode" {
		progress.SeriesName = stream.Title
	} else {
		progress.MovieName = stream.Title
	}
	return progress
}

func homepageStreamFromDashboard(source StreamInfo) HomepageStream {
	return HomepageStream{
		ID:              source.ID,
		Type:            source.Type,
		Filename:        source.Filename,
		ProfileName:     source.ProfileName,
		ProfileNames:    source.ProfileNames,
		ClientIP:        source.ClientIP,
		CreatedAt:       source.CreatedAt,
		Duration:        source.Duration,
		CurrentPosition: source.CurrentPosition,
		PercentWatched:  source.PercentWatched,
		HasDV:           source.HasDV,
		HasHDR:          source.HasHDR,
		MediaType:       source.MediaType,
		Title:           source.Title,
		Year:            source.Year,
		SeasonNumber:    source.SeasonNumber,
		EpisodeNumber:   source.EpisodeNumber,
		EpisodeName:     source.EpisodeName,
		ExternalIDs:     source.ExternalIDs,
		PosterURL:       source.PosterURL,
	}
}

func homepageProgressForPoster(stream HomepageStream) *models.PlaybackProgress {
	progress := &models.PlaybackProgress{
		MediaType:     stream.MediaType,
		ExternalIDs:   stream.ExternalIDs,
		SeasonNumber:  stream.SeasonNumber,
		EpisodeNumber: stream.EpisodeNumber,
		EpisodeName:   stream.EpisodeName,
		Year:          stream.Year,
	}
	if stream.MediaType == "episode" {
		progress.SeriesName = stream.Title
	} else {
		progress.MovieName = stream.Title
	}
	return progress
}

// findMatchingProgress finds a matching progress entry for a filename.
// Uses a two-pass approach: first tries precise match (series name + S##E##),
// then falls back to name-only match picking the most recently updated entry.
func findMatchingProgress(progressList []models.PlaybackProgress, cleanedFilename, originalFilename string) *models.PlaybackProgress {
	lowerOriginal := strings.ToLower(originalFilename)

	// Pass 1: Precise match — series name + S##E## pattern
	for i := range progressList {
		progress := &progressList[i]
		if progress.MediaType == "episode" && progress.SeasonNumber > 0 && progress.EpisodeNumber > 0 {
			sePattern := strings.ToLower(formatSeasonEpisode(progress.SeasonNumber, progress.EpisodeNumber))
			if strings.Contains(lowerOriginal, sePattern) {
				cleanedProgressName := cleanFilenameForMatch(progress.SeriesName)
				if cleanedProgressName != "" && cleanedFilename != "" &&
					strings.Contains(cleanedFilename, cleanedProgressName) {
					return progress
				}
			}
		} else if progress.MediaType != "episode" {
			// Movies: match on name only
			cleanedProgressName := cleanFilenameForMatch(progress.MovieName)
			if cleanedProgressName != "" && cleanedFilename != "" &&
				strings.Contains(cleanedFilename, cleanedProgressName) {
				return progress
			}
		}
	}

	// Pass 2: Name-only fallback for episodes — pick most recently updated match
	var bestMatch *models.PlaybackProgress
	for i := range progressList {
		progress := &progressList[i]
		if progress.MediaType == "episode" {
			cleanedProgressName := cleanFilenameForMatch(progress.SeriesName)
			if cleanedProgressName != "" && cleanedFilename != "" &&
				strings.Contains(cleanedFilename, cleanedProgressName) {
				if bestMatch == nil || progress.UpdatedAt.After(bestMatch.UpdatedAt) {
					bestMatch = &progressList[i]
				}
			}
		}
	}
	return bestMatch
}

// fetchPosterURL fetches the poster URL from the metadata service
func (h *HomepageHandler) fetchPosterURL(ctx context.Context, progress *models.PlaybackProgress) string {
	posterURL, _ := h.fetchShelfArtwork(ctx, progress)
	return posterURL
}

func (h *HomepageHandler) fetchShelfArtwork(ctx context.Context, progress *models.PlaybackProgress) (string, string) {
	if h.metadataService == nil || progress == nil {
		return "", ""
	}

	// Parse external IDs
	var tmdbID, tvdbID int64
	var imdbID string
	if id, ok := progress.ExternalIDs["tmdb"]; ok && id != "" {
		if parsed, err := strconv.ParseInt(id, 10, 64); err == nil {
			tmdbID = parsed
		}
	}
	if id, ok := progress.ExternalIDs["tvdb"]; ok && id != "" {
		if parsed, err := strconv.ParseInt(id, 10, 64); err == nil {
			tvdbID = parsed
		}
	}
	if id, ok := progress.ExternalIDs["imdb"]; ok && id != "" {
		imdbID = id
	}

	switch strings.ToLower(progress.MediaType) {
	case "movie":
		query := models.MovieDetailsQuery{
			Name:   progress.MovieName,
			Year:   progress.Year,
			IMDBID: imdbID,
			TMDBID: tmdbID,
			TVDBID: tvdbID,
		}
		if title, err := h.metadataService.MovieDetails(ctx, query); err == nil && title != nil {
			var posterURL, backdropURL string
			if title.Poster != nil {
				posterURL = title.Poster.URL
			}
			if title.Backdrop != nil {
				backdropURL = title.Backdrop.URL
			}
			return posterURL, backdropURL
		}
	case "episode":
		query := models.SeriesDetailsQuery{
			Name:   progress.SeriesName,
			TMDBID: tmdbID,
			TVDBID: tvdbID,
		}
		if details, err := h.metadataService.SeriesDetails(ctx, query); err == nil && details != nil {
			var posterURL, backdropURL string
			if details.Title.Poster != nil {
				posterURL = details.Title.Poster.URL
			}
			if details.Title.Backdrop != nil {
				backdropURL = details.Title.Backdrop.URL
			}
			return posterURL, backdropURL
		}
	}

	return "", ""
}

// deduplicateStreams removes duplicate streams based on profileName + filename
// When the same user is watching the same file, we only want one entry
func deduplicateStreams(streams []HomepageStream) []HomepageStream {
	if len(streams) == 0 {
		return streams
	}

	// Map to track unique streams by profileName + filename
	seen := make(map[string]int) // key -> index in result
	var result []HomepageStream

	for _, stream := range streams {
		key := strings.ToLower(stream.ProfileName) + "|" + strings.ToLower(stream.Filename)

		if existingIdx, exists := seen[key]; exists {
			// Keep the one with more recent activity (higher currentPosition typically means more recent)
			// or if the new one has more complete metadata
			existing := result[existingIdx]
			if stream.CurrentPosition > existing.CurrentPosition ||
				(stream.Title != "" && existing.Title == "") ||
				(stream.PosterURL != "" && existing.PosterURL == "") {
				result[existingIdx] = stream
			}
		} else {
			seen[key] = len(result)
			result = append(result, stream)
		}
	}

	return result
}
