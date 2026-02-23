package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"novastream/models"
)

// ProgressService provides access to playback progress data for admin dashboard
type ProgressService interface {
	ListAllPlaybackProgress() map[string][]models.PlaybackProgress
}

// UserService provides access to user profile data
type UserService interface {
	ListAll() []models.User
}

// AdminHandler provides administrative endpoints for monitoring the server
type AdminHandler struct {
	hlsManager      *HLSManager
	progressService ProgressService
	userService     UserService
}

// NewAdminHandler creates a new admin handler
func NewAdminHandler(hlsManager *HLSManager) *AdminHandler {
	return &AdminHandler{
		hlsManager: hlsManager,
	}
}

// SetProgressService sets the playback progress service for continue watching data
func (h *AdminHandler) SetProgressService(svc ProgressService) {
	h.progressService = svc
}

// SetUserService sets the user service for profile name lookup
func (h *AdminHandler) SetUserService(svc UserService) {
	h.userService = svc
}

// StreamInfo represents information about an active stream
type StreamInfo struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"` // "hls", "direct", or "debrid"
	Path          string    `json:"path"`
	OriginalPath  string    `json:"original_path,omitempty"`
	Filename      string    `json:"filename"`
	ClientIP      string    `json:"client_ip,omitempty"`
	ProfileID     string    `json:"profile_id,omitempty"`
	ProfileName   string    `json:"profile_name,omitempty"`
	ProfileIDs    []string  `json:"profile_ids,omitempty"`   // Multiple profiles watching same item
	ProfileNames  []string  `json:"profile_names,omitempty"` // Multiple profile names watching same item
	CreatedAt     time.Time `json:"created_at"`
	LastAccess    time.Time `json:"last_access"`
	Duration      float64   `json:"duration,omitempty"`
	BytesStreamed int64     `json:"bytes_streamed"`
	ContentLength int64     `json:"content_length,omitempty"`
	HasDV         bool      `json:"has_dv"`
	HasHDR        bool      `json:"has_hdr"`
	DVProfile     string    `json:"dv_profile,omitempty"`
	Segments      int       `json:"segments,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	// Progress tracking
	StartOffset     float64 `json:"start_offset,omitempty"`     // Where playback started (seconds)
	CurrentPosition float64 `json:"current_position,omitempty"` // Estimated current playback position (seconds)
	PercentWatched  float64 `json:"percent_watched,omitempty"`  // Progress percentage (0-100)
	// Media identification (from matched playback progress)
	MediaType     string            `json:"media_type,omitempty"`     // "movie" or "episode"
	Title         string            `json:"title,omitempty"`          // Movie name or series name
	Year          int               `json:"year,omitempty"`           // Release year (for movies)
	SeasonNumber  int               `json:"season_number,omitempty"`  // Season number (for episodes)
	EpisodeNumber int               `json:"episode_number,omitempty"` // Episode number (for episodes)
	EpisodeName   string            `json:"episode_name,omitempty"`   // Episode title (for episodes)
	ExternalIDs   map[string]string `json:"externalIds,omitempty"` // tmdbId, tvdbId, imdbId
	// Pause detection
	IsPaused      bool      `json:"is_paused,omitempty"`       // True if no recent activity (likely paused)
	PausedSince   time.Time `json:"paused_since,omitempty"`    // When the stream was detected as paused
}

// StreamsResponse is the response for the streams endpoint
type StreamsResponse struct {
	Streams []StreamInfo `json:"streams"`
	Count   int          `json:"count"`
	HLS     int          `json:"hls_count"`
	Direct  int          `json:"direct_count"`
}

// GetActiveStreams returns all active streams (both HLS and direct)
func (h *AdminHandler) GetActiveStreams(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Build user ID -> name map for profile lookup
	userNames := make(map[string]string)
	if h.userService != nil {
		for _, user := range h.userService.ListAll() {
			userNames[user.ID] = user.Name
		}
	}

	// Get all playback progress for matching
	var allProgress map[string][]models.PlaybackProgress
	if h.progressService != nil {
		allProgress = h.progressService.ListAllPlaybackProgress()
	}

	// Pause detection thresholds
	const pauseThreshold = 30 * time.Second // No activity for 30s = paused
	const hideThreshold = 60 * time.Second  // Paused for 30s beyond pause threshold = hide from dashboard
	now := time.Now()

	// Collect all raw streams first
	type rawStream struct {
		info     StreamInfo
		streamID string
	}
	var rawStreams []rawStream

	// Get HLS sessions
	hlsCount := 0
	if h.hlsManager != nil {
		h.hlsManager.mu.RLock()
		for _, session := range h.hlsManager.sessions {
			session.mu.RLock()

			// Extract filename from path
			filename := filepath.Base(session.Path)
			if filename == "" || filename == "." {
				filename = filepath.Base(session.OriginalPath)
			}

			// Look up profile name if not set
			profileName := session.ProfileName
			if profileName == "" && session.ProfileID != "" {
				if name, ok := userNames[session.ProfileID]; ok {
					profileName = name
				}
			}

			// For pause detection, use LastSegmentRequest (updated by keepalives every 10s)
			// Falls back to LastAccess if no segment requests yet
			lastActivity := session.LastSegmentRequest
			if lastActivity.IsZero() {
				lastActivity = session.LastAccess
			}

			info := StreamInfo{
				ID:           session.ID,
				Type:         "hls",
				Path:         session.Path,
				OriginalPath: session.OriginalPath,
				Filename:     filename,
				ClientIP:     session.ClientIP,
				ProfileID:    session.ProfileID,
				ProfileName:  profileName,
				CreatedAt:    session.CreatedAt,
				LastAccess:   lastActivity,
				Duration:     session.Duration,
				BytesStreamed: session.BytesStreamed,
				HasDV:        session.HasDV && !session.DVDisabled,
				HasHDR:       session.HasHDR,
				DVProfile:    session.DVProfile,
				Segments:     session.SegmentsCreated,
				StartOffset:  session.StartOffset,
			}

			session.mu.RUnlock()
			rawStreams = append(rawStreams, rawStream{info: info, streamID: session.ID})
			hlsCount++
		}
		h.hlsManager.mu.RUnlock()
	}

	// Get direct streams from the global tracker
	directCount := 0
	tracker := GetStreamTracker()
	for _, stream := range tracker.GetActiveStreams() {
		// Look up profile name if not set
		profileName := stream.ProfileName
		if profileName == "" && stream.ProfileID != "" {
			if name, ok := userNames[stream.ProfileID]; ok {
				profileName = name
			}
		}

		info := StreamInfo{
			ID:            stream.ID,
			Type:          "direct",
			Path:          stream.Path,
			Filename:      stream.Filename,
			ClientIP:      stream.ClientIP,
			ProfileID:     stream.ProfileID,
			ProfileName:   profileName,
			CreatedAt:     stream.StartTime,
			LastAccess:    stream.LastActivity,
			BytesStreamed: stream.BytesStreamed,
			ContentLength: stream.ContentLength,
			UserAgent:     stream.UserAgent,
		}
		rawStreams = append(rawStreams, rawStream{info: info, streamID: stream.ID})
		directCount++
	}

	// Build reverse lookup: user name -> user ID for progress matching
	nameToUserID := make(map[string]string)
	for userID, name := range userNames {
		nameToUserID[strings.ToLower(name)] = userID
	}

	// Match streams to playback progress and consolidate by user+media
	// Key: profileID + cleaned filename base
	consolidated := make(map[string]*StreamInfo)

	for _, rs := range rawStreams {
		// Skip "default" user streams - but only if profileName is also empty/default
		// A stream with profileID="default" but valid profileName should still be shown
		// (e.g., a user profile might legitimately have ID "default")
		isDefaultProfile := strings.ToLower(rs.info.ProfileID) == "default" || rs.info.ProfileID == ""
		hasValidProfileName := rs.info.ProfileName != "" && strings.ToLower(rs.info.ProfileName) != "default"
		if isDefaultProfile && !hasValidProfileName {
			continue
		}
		info := rs.info
		cleanedFilename := cleanFilenameForMatch(info.Filename)

		// Try to find matching playback progress
		var matchedProgress *models.PlaybackProgress

		// Helper to find matching progress in a user's list
		findMatch := func(progressList []models.PlaybackProgress) *models.PlaybackProgress {
			for i := range progressList {
				progress := &progressList[i]
				progressName := ""
				if progress.MediaType == "episode" {
					progressName = progress.SeriesName
					// Also try to match season/episode from filename
					if progress.SeasonNumber > 0 && progress.EpisodeNumber > 0 {
						// Check if filename contains S##E## pattern matching this episode
						sePattern := strings.ToLower(formatSeasonEpisode(progress.SeasonNumber, progress.EpisodeNumber))
						if strings.Contains(strings.ToLower(info.Filename), sePattern) {
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

		// Determine which user ID to use for progress lookup
		// Priority: ProfileID -> lookup by ProfileName -> empty
		userIDsToTry := []string{}
		if info.ProfileID != "" {
			userIDsToTry = append(userIDsToTry, info.ProfileID)
		}
		// Also try looking up by profile name (handles case where ProfileID != user ID in progress)
		if info.ProfileName != "" {
			if mappedID, ok := nameToUserID[strings.ToLower(info.ProfileName)]; ok && mappedID != info.ProfileID {
				userIDsToTry = append(userIDsToTry, mappedID)
			}
		}

		// Try each user ID to find matching progress
		for _, userID := range userIDsToTry {
			if userProgress, ok := allProgress[userID]; ok {
				if match := findMatch(userProgress); match != nil {
					matchedProgress = match
					break
				}
			}
		}

		// Apply matched progress including media identification
		if matchedProgress != nil {
			info.CurrentPosition = matchedProgress.Position
			info.PercentWatched = matchedProgress.PercentWatched
			if matchedProgress.Duration > 0 {
				info.Duration = matchedProgress.Duration
			}
			// Media identification from progress
			info.MediaType = matchedProgress.MediaType
			info.ExternalIDs = matchedProgress.ExternalIDs
			if matchedProgress.MediaType == "episode" {
				info.Title = matchedProgress.SeriesName
				info.SeasonNumber = matchedProgress.SeasonNumber
				info.EpisodeNumber = matchedProgress.EpisodeNumber
				info.EpisodeName = matchedProgress.EpisodeName
			} else {
				info.Title = matchedProgress.MovieName
				info.Year = matchedProgress.Year
			}
		}

		// Consolidation key: group by media item only (not by profile)
		// This groups multiple profiles watching the same item together
		consolidationKey := cleanFilenameForConsolidation(info.Filename)

		if existing, ok := consolidated[consolidationKey]; ok {
			// Merge with existing - keep most recent, sum bytes
			existing.BytesStreamed += info.BytesStreamed
			if info.LastAccess.After(existing.LastAccess) {
				existing.LastAccess = info.LastAccess
			}
			if info.CreatedAt.Before(existing.CreatedAt) {
				existing.CreatedAt = info.CreatedAt
			}
			// Keep DV/HDR flags if either has them
			existing.HasDV = existing.HasDV || info.HasDV
			existing.HasHDR = existing.HasHDR || info.HasHDR
			// Keep duration if we have it
			if info.Duration > 0 && existing.Duration == 0 {
				existing.Duration = info.Duration
			}
			// Add this profile to the list if not already present
			profileID := info.ProfileID
			profileName := info.ProfileName
			if profileName == "" {
				profileName = profileID
			}
			if profileName != "" {
				// Check if profile already added
				found := false
				for _, name := range existing.ProfileNames {
					if name == profileName {
						found = true
						break
					}
				}
				if !found {
					existing.ProfileNames = append(existing.ProfileNames, profileName)
					if profileID != "" {
						existing.ProfileIDs = append(existing.ProfileIDs, profileID)
					}
				}
			}
			// Keep media info from whichever has it
			if existing.MediaType == "" && info.MediaType != "" {
				existing.MediaType = info.MediaType
				existing.Title = info.Title
				existing.Year = info.Year
				existing.SeasonNumber = info.SeasonNumber
				existing.EpisodeNumber = info.EpisodeNumber
				existing.EpisodeName = info.EpisodeName
				existing.ExternalIDs = info.ExternalIDs
			}
		} else {
			// New entry - initialize with this profile
			infoCopy := info
			profileName := info.ProfileName
			if profileName == "" {
				profileName = info.ProfileID
			}
			if profileName != "" {
				infoCopy.ProfileNames = []string{profileName}
				if info.ProfileID != "" {
					infoCopy.ProfileIDs = []string{info.ProfileID}
				}
			}
			consolidated[consolidationKey] = &infoCopy
		}
	}

	// Build final response
	response := StreamsResponse{
		Streams: make([]StreamInfo, 0, len(consolidated)),
		HLS:     hlsCount,
		Direct:  directCount,
	}

	for _, info := range consolidated {
		// Skip streams with 0 bytes transferred (not actually playing)
		if info.BytesStreamed == 0 {
			continue
		}

		// Pause detection: mark as paused if no recent activity
		idleDuration := now.Sub(info.LastAccess)
		if idleDuration > pauseThreshold {
			// Hide streams that have been idle too long
			if idleDuration > hideThreshold {
				continue
			}
			info.IsPaused = true
			info.PausedSince = info.LastAccess
		}

		response.Streams = append(response.Streams, *info)
	}
	response.Count = len(response.Streams)

	json.NewEncoder(w).Encode(response)
}

// cleanFilenameForMatch removes common filename artifacts for matching against media titles
func cleanFilenameForMatch(name string) string {
	if name == "" {
		return ""
	}
	// Remove known video file extensions only (filepath.Ext misinterprets dots in titles)
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".ts", ".m4v", ".webm", ".mov", ".wmv", ".flv"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	// Replace common separators with spaces
	name = strings.ReplaceAll(name, ".", " ")
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	// Lowercase for comparison
	name = strings.ToLower(name)
	// Remove common quality/codec indicators
	for _, pattern := range []string{"1080p", "720p", "2160p", "4k", "bluray", "webrip", "webdl", "web dl", "hdtv", "x264", "x265", "hevc", "h264", "h265", "aac", "dts", "atmos", "truehd", "remux"} {
		name = strings.ReplaceAll(name, pattern, "")
	}
	// Collapse multiple spaces
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	return strings.TrimSpace(name)
}

// cleanFilenameForConsolidation creates a key for consolidating duplicate streams
func cleanFilenameForConsolidation(filename string) string {
	if filename == "" {
		return ""
	}
	// Remove file extension
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	// Lowercase
	return strings.ToLower(name)
}

// formatSeasonEpisode returns a pattern like "s01e01" for matching
func formatSeasonEpisode(season, episode int) string {
	return fmt.Sprintf("s%02de%02d", season, episode)
}
