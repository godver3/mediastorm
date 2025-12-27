package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"novastream/handlers"

	"github.com/gorilla/mux"
)

// corsMiddleware handles CORS for API routes
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleOptions handles OPTIONS requests for CORS preflight
func handleOptions(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Register mounts API endpoints onto the provided router.
func Register(
	r *mux.Router,
	settingsHandler *handlers.SettingsHandler,
	metadataHandler *handlers.MetadataHandler,
	indexerHandler *handlers.IndexerHandler,
	playbackHandler *handlers.PlaybackHandler,
	prequeueHandler *handlers.PrequeueHandler,
	usenetHandler *handlers.UsenetHandler,
	debridHandler *handlers.DebridHandler,
	videoHandler *handlers.VideoHandler,
	usersHandler *handlers.UsersHandler,
	watchlistHandler *handlers.WatchlistHandler,
	historyHandler *handlers.HistoryHandler,
	debugHandler *handlers.DebugHandler,
	logsHandler *handlers.LogsHandler,
	liveHandler *handlers.LiveHandler,
	debugVideoHandler *handlers.DebugVideoHandler,
	userSettingsHandler *handlers.UserSettingsHandler,
	subtitlesHandler *handlers.SubtitlesHandler,
	getPIN func() string,
) {
	api := r.PathPrefix("/api").Subrouter()

	// Add CORS middleware to API subrouter
	api.Use(corsMiddleware)
	api.Use(pinMiddleware(getPIN))

	api.HandleFunc("/settings", settingsHandler.GetSettings).Methods(http.MethodGet)
	api.HandleFunc("/settings", settingsHandler.PutSettings).Methods(http.MethodPut)
	api.HandleFunc("/settings", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/settings/cache/clear", settingsHandler.ClearMetadataCache).Methods(http.MethodPost)
	api.HandleFunc("/settings/cache/clear", handleOptions).Methods(http.MethodOptions)

	api.HandleFunc("/discover/new", metadataHandler.DiscoverNew).Methods(http.MethodGet)
	api.HandleFunc("/discover/new", handleOptions).Methods(http.MethodOptions)

	api.HandleFunc("/search", metadataHandler.Search).Methods(http.MethodGet)
	api.HandleFunc("/search", handleOptions).Methods(http.MethodOptions)

	api.HandleFunc("/metadata/series/details", metadataHandler.SeriesDetails).Methods(http.MethodGet)
	api.HandleFunc("/metadata/series/details", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/metadata/series/batch", metadataHandler.BatchSeriesDetails).Methods(http.MethodPost)
	api.HandleFunc("/metadata/series/batch", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/metadata/movies/details", metadataHandler.MovieDetails).Methods(http.MethodGet)
	api.HandleFunc("/metadata/movies/details", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/metadata/trailers", metadataHandler.Trailers).Methods(http.MethodGet)
	api.HandleFunc("/metadata/trailers", handleOptions).Methods(http.MethodOptions)

	api.HandleFunc("/indexers/search", indexerHandler.Search).Methods(http.MethodGet)
	api.HandleFunc("/indexers/search", indexerHandler.Options).Methods(http.MethodOptions)

	api.HandleFunc("/playback/resolve", playbackHandler.Resolve).Methods(http.MethodPost)
	api.HandleFunc("/playback/resolve", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/playback/queue/{queueID}", playbackHandler.QueueStatus).Methods(http.MethodGet)
	api.HandleFunc("/playback/queue/{queueID}", handleOptions).Methods(http.MethodOptions)

	// Prequeue endpoints for pre-loading playback streams
	if prequeueHandler != nil {
		api.HandleFunc("/playback/prequeue", prequeueHandler.Prequeue).Methods(http.MethodPost)
		api.HandleFunc("/playback/prequeue", prequeueHandler.Options).Methods(http.MethodOptions)
		api.HandleFunc("/playback/prequeue/{prequeueID}", prequeueHandler.GetStatus).Methods(http.MethodGet)
		api.HandleFunc("/playback/prequeue/{prequeueID}", prequeueHandler.Options).Methods(http.MethodOptions)
	}

	api.HandleFunc("/usenet/health", usenetHandler.CheckHealth).Methods(http.MethodPost)
	api.HandleFunc("/usenet/health", handleOptions).Methods(http.MethodOptions)

	api.HandleFunc("/debrid/proxy", debridHandler.Proxy).Methods(http.MethodGet, http.MethodHead)
	api.HandleFunc("/debrid/proxy", debridHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/debrid/cached", debridHandler.CheckCached).Methods(http.MethodPost)
	api.HandleFunc("/debrid/cached", debridHandler.Options).Methods(http.MethodOptions)

	api.HandleFunc("/live/playlist", liveHandler.FetchPlaylist).Methods(http.MethodGet)
	api.HandleFunc("/live/playlist", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/live/stream", liveHandler.StreamChannel).Methods(http.MethodGet, http.MethodHead)
	api.HandleFunc("/live/stream", handleOptions).Methods(http.MethodOptions)

	// Video streaming endpoints
	api.HandleFunc("/video/stream", videoHandler.StreamVideo).Methods(http.MethodGet, http.MethodHead, http.MethodOptions)
	api.HandleFunc("/video/metadata", videoHandler.ProbeVideo).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/direct-url", videoHandler.GetDirectURL).Methods(http.MethodGet, http.MethodOptions)

	// HLS streaming endpoints for Dolby Vision
	api.HandleFunc("/video/hls/start", videoHandler.StartHLSSession).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/hls/{sessionID}/stream.m3u8", videoHandler.ServeHLSPlaylist).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/hls/{sessionID}/subtitles.vtt", videoHandler.ServeHLSSubtitles).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/hls/{sessionID}/keepalive", videoHandler.KeepAliveHLSSession).Methods(http.MethodPost, http.MethodOptions)
	api.HandleFunc("/video/hls/{sessionID}/status", videoHandler.GetHLSSessionStatus).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/hls/{sessionID}/{segment}", videoHandler.ServeHLSSegment).Methods(http.MethodGet, http.MethodOptions)

	// Standalone subtitle extraction endpoints (for non-HLS streams)
	api.HandleFunc("/video/subtitles/tracks", videoHandler.ProbeSubtitleTracks).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/subtitles/start", videoHandler.StartSubtitleExtract).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/subtitles/{sessionID}/subtitles.vtt", videoHandler.ServeExtractedSubtitles).Methods(http.MethodGet, http.MethodOptions)

	// Subtitle search endpoints (using subliminal)
	api.HandleFunc("/subtitles/search", subtitlesHandler.Search).Methods(http.MethodGet)
	api.HandleFunc("/subtitles/search", subtitlesHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/subtitles/download", subtitlesHandler.Download).Methods(http.MethodGet)
	api.HandleFunc("/subtitles/download", subtitlesHandler.Options).Methods(http.MethodOptions)

	api.HandleFunc("/debug/log", debugHandler.Capture).Methods(http.MethodPost, http.MethodOptions)

	// Log submission endpoint
	api.HandleFunc("/logs/submit", logsHandler.Submit).Methods(http.MethodPost)
	api.HandleFunc("/logs/submit", logsHandler.Options).Methods(http.MethodOptions)

	// Version endpoint
	versionHandler := handlers.NewVersionHandler()
	api.HandleFunc("/version", versionHandler.GetVersion).Methods(http.MethodGet, http.MethodOptions)

	// Admin endpoints for monitoring
	adminHandler := handlers.NewAdminHandler(videoHandler.GetHLSManager())
	api.HandleFunc("/admin/streams", adminHandler.GetActiveStreams).Methods(http.MethodGet, http.MethodOptions)

	// MP4Box debug endpoints for DV/HDR testing (bypasses normal streaming pipeline)
	api.HandleFunc("/video/debug/mp4box/start", debugVideoHandler.StartMP4BoxHLSSession).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/debug/mp4box/probe", debugVideoHandler.ProbeVideoURL).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/debug/mp4box/{sessionID}/stream.m3u8", debugVideoHandler.ServeMP4BoxPlaylist).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/video/debug/mp4box/{sessionID}/{segment}", debugVideoHandler.ServeMP4BoxSegment).Methods(http.MethodGet, http.MethodOptions)

	api.HandleFunc("/users", usersHandler.List).Methods(http.MethodGet)
	api.HandleFunc("/users", usersHandler.Create).Methods(http.MethodPost)
	api.HandleFunc("/users", usersHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}", usersHandler.Rename).Methods(http.MethodPatch)
	api.HandleFunc("/users/{userID}", usersHandler.Delete).Methods(http.MethodDelete)
	api.HandleFunc("/users/{userID}", usersHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/color", usersHandler.SetColor).Methods(http.MethodPut)
	api.HandleFunc("/users/{userID}/color", usersHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/pin", usersHandler.SetPin).Methods(http.MethodPut)
	api.HandleFunc("/users/{userID}/pin", usersHandler.ClearPin).Methods(http.MethodDelete)
	api.HandleFunc("/users/{userID}/pin", usersHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/pin/verify", usersHandler.VerifyPin).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/pin/verify", usersHandler.Options).Methods(http.MethodOptions)

	api.HandleFunc("/users/{userID}/settings", userSettingsHandler.GetSettings).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/settings", userSettingsHandler.PutSettings).Methods(http.MethodPut)
	api.HandleFunc("/users/{userID}/settings", userSettingsHandler.Options).Methods(http.MethodOptions)

	api.HandleFunc("/users/{userID}/watchlist", watchlistHandler.List).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/watchlist", watchlistHandler.Add).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/watchlist", watchlistHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/watchlist/{mediaType}/{id}", watchlistHandler.UpdateState).Methods(http.MethodPatch)
	api.HandleFunc("/users/{userID}/watchlist/{mediaType}/{id}", watchlistHandler.Remove).Methods(http.MethodDelete)
	api.HandleFunc("/users/{userID}/watchlist/{mediaType}/{id}", watchlistHandler.Options).Methods(http.MethodOptions)

	api.HandleFunc("/users/{userID}/history/continue", historyHandler.ListContinueWatching).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/history/continue", historyHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/history/continue/{seriesID}/hide", historyHandler.HideFromContinueWatching).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/history/continue/{seriesID}/hide", historyHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/history/series/{seriesID}", historyHandler.GetSeriesWatchState).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/history/series/{seriesID}", historyHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/history/episodes", historyHandler.RecordEpisode).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/history/episodes", historyHandler.Options).Methods(http.MethodOptions)

	// Watch History endpoints (unified watch tracking for all media)
	api.HandleFunc("/users/{userID}/history/watched", historyHandler.ListWatchHistory).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/history/watched", historyHandler.UpdateWatchHistory).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/history/watched", historyHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/history/watched/bulk", historyHandler.BulkUpdateWatchHistory).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/history/watched/bulk", historyHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/history/watched/{mediaType}/{id}", historyHandler.GetWatchHistoryItem).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/history/watched/{mediaType}/{id}", historyHandler.UpdateWatchHistory).Methods(http.MethodPatch)
	api.HandleFunc("/users/{userID}/history/watched/{mediaType}/{id}/toggle", historyHandler.ToggleWatched).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/history/watched/{mediaType}/{id}", historyHandler.Options).Methods(http.MethodOptions)

	// Playback Progress endpoints (continuous progress tracking for native player)
	api.HandleFunc("/users/{userID}/history/progress", historyHandler.ListPlaybackProgress).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/history/progress", historyHandler.UpdatePlaybackProgress).Methods(http.MethodPost)
	api.HandleFunc("/users/{userID}/history/progress", historyHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/users/{userID}/history/progress/{mediaType}/{id}", historyHandler.GetPlaybackProgress).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/history/progress/{mediaType}/{id}", historyHandler.UpdatePlaybackProgress).Methods(http.MethodPatch)
	api.HandleFunc("/users/{userID}/history/progress/{mediaType}/{id}", historyHandler.DeletePlaybackProgress).Methods(http.MethodDelete)
	api.HandleFunc("/users/{userID}/history/progress/{mediaType}/{id}", historyHandler.Options).Methods(http.MethodOptions)
}

func pinMiddleware(getPIN func() string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Get current PIN (supports hot reload)
			expectedPIN := strings.TrimSpace(getPIN())
			if expectedPIN == "" {
				// No PIN configured, allow access
				next.ServeHTTP(w, r)
				return
			}

			receivedPIN := strings.TrimSpace(r.Header.Get("X-PIN"))
			if receivedPIN == "" {
				authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
				if len(authHeader) > 0 {
					lower := strings.ToLower(authHeader)
					switch {
					case strings.HasPrefix(lower, "bearer "):
						receivedPIN = strings.TrimSpace(authHeader[7:])
					case strings.HasPrefix(lower, "pin "):
						receivedPIN = strings.TrimSpace(authHeader[4:])
					}
				}
			}

			if receivedPIN == "" {
				query := r.URL.Query()
				for _, pinParam := range []string{"pin", "PIN"} {
					candidate := strings.TrimSpace(query.Get(pinParam))
					if candidate != "" {
						receivedPIN = candidate
						break
					}
				}
			}

			// Legacy support: also check for old API key parameters
			if receivedPIN == "" {
				receivedKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
				if receivedKey == "" {
					authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
					if len(authHeader) > 0 {
						lower := strings.ToLower(authHeader)
						switch {
						case strings.HasPrefix(lower, "bearer "):
							receivedKey = strings.TrimSpace(authHeader[7:])
						case strings.HasPrefix(lower, "apikey "):
							receivedKey = strings.TrimSpace(authHeader[7:])
						}
					}
				}

				if receivedKey == "" {
					query := r.URL.Query()
					for _, keyParam := range []string{"apiKey", "apikey", "api_key", "key"} {
						candidate := strings.TrimSpace(query.Get(keyParam))
						if candidate != "" {
							receivedKey = candidate
							break
						}
					}
				}

				// If we found a legacy API key, treat it as a PIN for backward compatibility
				if receivedKey != "" {
					receivedPIN = receivedKey
				}
			}

			if receivedPIN == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "missing PIN"})
				return
			}

			if subtle.ConstantTimeCompare([]byte(receivedPIN), []byte(expectedPIN)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid PIN"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
