package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
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

// HomepageHandler provides stats for Homepage dashboard integration
type HomepageHandler struct {
	accounts        *accounts.Service
	userService     UserService
	hlsManager      *HLSManager
	progressService ProgressService
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
	ID              string            `json:"id"`
	Type            string            `json:"type"` // "hls"
	Filename        string            `json:"filename"`
	ProfileName     string            `json:"profileName,omitempty"`
	ClientIP        string            `json:"clientIp,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	Duration        float64           `json:"duration,omitempty"`
	CurrentPosition float64           `json:"currentPosition,omitempty"`
	PercentWatched  float64           `json:"percentWatched,omitempty"`
	HasDV           bool              `json:"hasDv"`
	HasHDR          bool              `json:"hasHdr"`
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

// SetHLSManager sets the HLS manager for stream info
func (h *HomepageHandler) SetHLSManager(mgr *HLSManager) {
	h.hlsManager = mgr
}

// SetProgressService sets the progress service for media matching
func (h *HomepageHandler) SetProgressService(svc ProgressService) {
	h.progressService = svc
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

	// Build user ID -> user map and account -> profiles map
	usersByID := make(map[string]models.User)
	userNames := make(map[string]string)
	profilesByAccount := make(map[string][]HomepageProfile)
	totalProfiles := 0

	if h.userService != nil {
		for _, user := range h.userService.ListAll() {
			usersByID[user.ID] = user
			userNames[user.ID] = user.Name
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

	// Get all playback progress for matching
	var allProgress map[string][]models.PlaybackProgress
	if h.progressService != nil {
		allProgress = h.progressService.ListAllPlaybackProgress()
	}

	// Build reverse lookup: user name -> user ID for progress matching
	nameToUserID := make(map[string]string)
	for userID, name := range userNames {
		nameToUserID[strings.ToLower(name)] = userID
	}

	// Build streams list from HLS manager
	var streamsList []HomepageStream
	if h.hlsManager != nil {
		h.hlsManager.mu.RLock()
		for _, session := range h.hlsManager.sessions {
			session.mu.RLock()

			// Extract filename from path
			filename := filepath.Base(session.Path)
			if filename == "" || filename == "." {
				filename = filepath.Base(session.OriginalPath)
			}

			// Look up profile name
			profileName := session.ProfileName
			if profileName == "" && session.ProfileID != "" {
				if user, ok := usersByID[session.ProfileID]; ok {
					profileName = user.Name
				}
			}

			// Calculate current position estimate
			elapsed := time.Since(session.LastAccess).Seconds()
			currentPos := session.StartOffset + elapsed
			if currentPos > session.Duration && session.Duration > 0 {
				currentPos = session.Duration
			}

			// Calculate percent watched
			percentWatched := 0.0
			if session.Duration > 0 {
				percentWatched = (currentPos / session.Duration) * 100
				if percentWatched > 100 {
					percentWatched = 100
				}
			}

			stream := HomepageStream{
				ID:              session.ID,
				Type:            "hls",
				Filename:        filename,
				ProfileName:     profileName,
				ClientIP:        session.ClientIP,
				CreatedAt:       session.CreatedAt,
				Duration:        session.Duration,
				CurrentPosition: currentPos,
				PercentWatched:  percentWatched,
				HasDV:           session.HasDV,
				HasHDR:          session.HasHDR,
			}

			// Try to match to playback progress for media info
			cleanedFilename := cleanFilenameForMatch(filename)

			// Determine which user IDs to try for progress lookup
			userIDsToTry := []string{}
			if session.ProfileID != "" {
				userIDsToTry = append(userIDsToTry, session.ProfileID)
			}
			if profileName != "" {
				if mappedID, ok := nameToUserID[strings.ToLower(profileName)]; ok && mappedID != session.ProfileID {
					userIDsToTry = append(userIDsToTry, mappedID)
				}
			}

			// Try each user ID to find matching progress
			for _, userID := range userIDsToTry {
				if userProgress, ok := allProgress[userID]; ok {
					if match := findMatchingProgress(userProgress, cleanedFilename, filename); match != nil {
						stream.CurrentPosition = match.Position
						stream.PercentWatched = match.PercentWatched
						if match.Duration > 0 {
							stream.Duration = match.Duration
						}
						stream.MediaType = match.MediaType
						stream.ExternalIDs = match.ExternalIDs
						if match.MediaType == "episode" {
							stream.Title = match.SeriesName
							stream.SeasonNumber = match.SeasonNumber
							stream.EpisodeNumber = match.EpisodeNumber
							stream.EpisodeName = match.EpisodeName
						} else {
							stream.Title = match.MovieName
							stream.Year = match.Year
						}
						// Fetch poster URL from metadata service
						if h.metadataService != nil {
							stream.PosterURL = h.fetchPosterURL(r.Context(), match)
						}
						break
					}
				}
			}

			session.mu.RUnlock()
			streamsList = append(streamsList, stream)
		}
		h.hlsManager.mu.RUnlock()
	}

	// Deduplicate streams by profileName + filename
	// Same user watching the same file = one stream entry
	deduped := deduplicateStreams(streamsList)

	stats := HomepageStats{
		Version:       GetBackendVersion(),
		ActiveStreams: len(deduped),
		TotalAccounts: len(accountsList),
		TotalProfiles: totalProfiles,
		Accounts:      accountsList,
		Streams:       deduped,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// findMatchingProgress finds a matching progress entry for a filename
func findMatchingProgress(progressList []models.PlaybackProgress, cleanedFilename, originalFilename string) *models.PlaybackProgress {
	for i := range progressList {
		progress := &progressList[i]
		progressName := ""
		if progress.MediaType == "episode" {
			progressName = progress.SeriesName
			// Also try to match season/episode from filename
			if progress.SeasonNumber > 0 && progress.EpisodeNumber > 0 {
				sePattern := strings.ToLower(formatSeasonEpisode(progress.SeasonNumber, progress.EpisodeNumber))
				if strings.Contains(strings.ToLower(originalFilename), sePattern) {
					cleanedProgressName := cleanFilenameForMatch(progressName)
					if cleanedProgressName != "" && cleanedFilename != "" &&
						strings.Contains(cleanedFilename, cleanedProgressName) {
						return progress
					}
				}
			}
		} else {
			progressName = progress.MovieName
		}
		cleanedProgressName := cleanFilenameForMatch(progressName)

		// Check if progress name is contained in filename
		if cleanedProgressName != "" && cleanedFilename != "" &&
			strings.Contains(cleanedFilename, cleanedProgressName) {
			return progress
		}
	}
	return nil
}

// fetchPosterURL fetches the poster URL from the metadata service
func (h *HomepageHandler) fetchPosterURL(ctx context.Context, progress *models.PlaybackProgress) string {
	if h.metadataService == nil || progress == nil {
		return ""
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
		if title, err := h.metadataService.MovieDetails(ctx, query); err == nil && title != nil && title.Poster != nil {
			return title.Poster.URL
		}
	case "episode":
		query := models.SeriesDetailsQuery{
			Name:   progress.SeriesName,
			TMDBID: tmdbID,
			TVDBID: tvdbID,
		}
		if details, err := h.metadataService.SeriesDetails(ctx, query); err == nil && details != nil && details.Title.Poster != nil {
			return details.Title.Poster.URL
		}
	}

	return ""
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
