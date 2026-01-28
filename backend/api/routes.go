package api

import (
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"

	"novastream/handlers"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/users"

	"github.com/gorilla/mux"
)

func itoa(i int) string      { return strconv.Itoa(i) }
func itoa64(i uint64) string { return strconv.FormatUint(i, 10) }

// localhostOnlyMiddleware restricts access to localhost requests only
func localhostOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port if present
		if idx := len(host) - 1; idx >= 0 {
			for i := len(host) - 1; i >= 0; i-- {
				if host[i] == ':' {
					host = host[:i]
					break
				}
			}
		}
		// Allow localhost, 127.0.0.1, ::1
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			http.Error(w, "Debug endpoints only accessible from localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// devOnlyMiddleware restricts access to dev hosts (localhost + docker hostname)
func devOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port if present
		for i := len(host) - 1; i >= 0; i-- {
			if host[i] == ':' {
				host = host[:i]
				break
			}
		}
		// Allow localhost, 127.0.0.1, ::1, and docker hostname
		if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "docker" {
			http.Error(w, "Dev endpoints only accessible from allowed hosts", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

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
	epgHandler *handlers.EPGHandler,
	userSettingsHandler *handlers.UserSettingsHandler,
	subtitlesHandler *handlers.SubtitlesHandler,
	clientsHandler *handlers.ClientsHandler,
	contentPreferencesHandler *handlers.ContentPreferencesHandler,
	imageHandler *handlers.ImageHandler,
	accountsSvc *accounts.Service,
	sessionsSvc *sessions.Service,
	usersSvc *users.Service,
) {
	api := r.PathPrefix("/api").Subrouter()

	// Add CORS middleware to API subrouter
	api.Use(corsMiddleware)

	// Auth routes (no authentication required)
	authHandler := handlers.NewAuthHandler(accountsSvc, sessionsSvc)
	api.HandleFunc("/auth/login", authHandler.Login).Methods(http.MethodPost)
	api.HandleFunc("/auth/login", authHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/auth/logout", authHandler.Logout).Methods(http.MethodPost)
	api.HandleFunc("/auth/logout", authHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/auth/me", authHandler.Me).Methods(http.MethodGet)
	api.HandleFunc("/auth/me", authHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/auth/refresh", authHandler.Refresh).Methods(http.MethodPost)
	api.HandleFunc("/auth/refresh", authHandler.Options).Methods(http.MethodOptions)
	api.HandleFunc("/auth/password", authHandler.ChangePassword).Methods(http.MethodPut)
	api.HandleFunc("/auth/password", authHandler.Options).Methods(http.MethodOptions)

	// Check if master account has default password (public endpoint for warning)
	accountsHandler := handlers.NewAccountsHandler(accountsSvc, sessionsSvc, usersSvc)
	api.HandleFunc("/auth/default-password", accountsHandler.HasDefaultPassword).Methods(http.MethodGet)
	api.HandleFunc("/auth/default-password", accountsHandler.Options).Methods(http.MethodOptions)

	// Profile icon endpoint (public - needed for Image components that can't send auth headers)
	// Must be registered before protected routes to avoid auth middleware
	api.HandleFunc("/users/{userID}/icon", usersHandler.ServeProfileIcon).Methods(http.MethodGet)
	api.HandleFunc("/users/{userID}/icon", handleOptions).Methods(http.MethodOptions)

	// Protected routes - require authentication
	protected := api.PathPrefix("").Subrouter()
	protected.Use(AccountAuthMiddleware(sessionsSvc))

	// Account management routes (master only)
	masterOnly := protected.PathPrefix("/accounts").Subrouter()
	masterOnly.Use(MasterOnlyMiddleware())
	masterOnly.HandleFunc("", accountsHandler.List).Methods(http.MethodGet)
	masterOnly.HandleFunc("", accountsHandler.Create).Methods(http.MethodPost)
	masterOnly.HandleFunc("", accountsHandler.Options).Methods(http.MethodOptions)
	masterOnly.HandleFunc("/{accountID}", accountsHandler.Get).Methods(http.MethodGet)
	masterOnly.HandleFunc("/{accountID}", accountsHandler.Rename).Methods(http.MethodPatch)
	masterOnly.HandleFunc("/{accountID}", accountsHandler.Delete).Methods(http.MethodDelete)
	masterOnly.HandleFunc("/{accountID}", accountsHandler.Options).Methods(http.MethodOptions)
	masterOnly.HandleFunc("/{accountID}/password", accountsHandler.ResetPassword).Methods(http.MethodPut)
	masterOnly.HandleFunc("/{accountID}/password", accountsHandler.Options).Methods(http.MethodOptions)

	// Profile reassignment (master only)
	masterOnly2 := protected.PathPrefix("/profiles").Subrouter()
	masterOnly2.Use(MasterOnlyMiddleware())
	masterOnly2.HandleFunc("/{profileID}/reassign", accountsHandler.ReassignProfile).Methods(http.MethodPost)
	masterOnly2.HandleFunc("/{profileID}/reassign", accountsHandler.Options).Methods(http.MethodOptions)

	// Profile ownership middleware for user routes
	profileProtected := protected.PathPrefix("/users").Subrouter()
	profileProtected.Use(ProfileOwnershipMiddleware(usersSvc))

	// Settings routes - GET for all authenticated, PUT/POST for master only
	protected.HandleFunc("/settings", settingsHandler.GetSettings).Methods(http.MethodGet)
	protected.HandleFunc("/settings", handleOptions).Methods(http.MethodOptions)

	settingsWriteRouter := protected.PathPrefix("/settings").Subrouter()
	settingsWriteRouter.Use(MasterOnlyMiddleware())
	settingsWriteRouter.HandleFunc("", settingsHandler.PutSettings).Methods(http.MethodPut)
	settingsWriteRouter.HandleFunc("/cache/clear", settingsHandler.ClearMetadataCache).Methods(http.MethodPost)
	settingsWriteRouter.HandleFunc("/cache/clear", handleOptions).Methods(http.MethodOptions)

	// Content discovery and metadata (all authenticated users)
	protected.HandleFunc("/discover/new", metadataHandler.DiscoverNew).Methods(http.MethodGet)
	protected.HandleFunc("/discover/new", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/lists/custom", metadataHandler.CustomList).Methods(http.MethodGet)
	protected.HandleFunc("/lists/custom", handleOptions).Methods(http.MethodOptions)

	protected.HandleFunc("/search", metadataHandler.Search).Methods(http.MethodGet)
	protected.HandleFunc("/search", handleOptions).Methods(http.MethodOptions)

	protected.HandleFunc("/metadata/series/details", metadataHandler.SeriesDetails).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/series/details", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/series/batch", metadataHandler.BatchSeriesDetails).Methods(http.MethodPost)
	protected.HandleFunc("/metadata/series/batch", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/movies/details", metadataHandler.MovieDetails).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/movies/details", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/movies/releases", metadataHandler.BatchMovieReleases).Methods(http.MethodPost)
	protected.HandleFunc("/metadata/movies/releases", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/collection", metadataHandler.CollectionDetails).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/collection", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/similar", metadataHandler.Similar).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/similar", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/person", metadataHandler.PersonDetails).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/person", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/trailers", metadataHandler.Trailers).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/trailers", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/trailers/stream", metadataHandler.TrailerStream).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/trailers/stream", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/trailers/proxy", metadataHandler.TrailerProxy).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/trailers/proxy", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/trailers/prequeue", metadataHandler.TrailerPrequeue).Methods(http.MethodPost)
	protected.HandleFunc("/metadata/trailers/prequeue", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/trailers/prequeue/status", metadataHandler.TrailerPrequeueStatus).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/trailers/prequeue/status", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/metadata/trailers/prequeue/serve", metadataHandler.TrailerPrequeueServe).Methods(http.MethodGet)
	protected.HandleFunc("/metadata/trailers/prequeue/serve", handleOptions).Methods(http.MethodOptions)

	protected.HandleFunc("/indexers/search", indexerHandler.Search).Methods(http.MethodGet)
	protected.HandleFunc("/indexers/search", indexerHandler.Options).Methods(http.MethodOptions)

	protected.HandleFunc("/playback/resolve", playbackHandler.Resolve).Methods(http.MethodPost)
	protected.HandleFunc("/playback/resolve", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/playback/queue/{queueID}", playbackHandler.QueueStatus).Methods(http.MethodGet)
	protected.HandleFunc("/playback/queue/{queueID}", handleOptions).Methods(http.MethodOptions)

	// Prequeue endpoints for pre-loading playback streams
	if prequeueHandler != nil {
		protected.HandleFunc("/playback/prequeue", prequeueHandler.Prequeue).Methods(http.MethodPost)
		protected.HandleFunc("/playback/prequeue", prequeueHandler.Options).Methods(http.MethodOptions)
		protected.HandleFunc("/playback/prequeue/{prequeueID}", prequeueHandler.GetStatus).Methods(http.MethodGet)
		protected.HandleFunc("/playback/prequeue/{prequeueID}", prequeueHandler.Options).Methods(http.MethodOptions)
		// Lazy subtitle extraction - called when user plays with known offset
		protected.HandleFunc("/playback/prequeue/{prequeueID}/start-subtitles", prequeueHandler.StartSubtitles).Methods(http.MethodPost)
		protected.HandleFunc("/playback/prequeue/{prequeueID}/start-subtitles", prequeueHandler.Options).Methods(http.MethodOptions)
	}

	protected.HandleFunc("/usenet/health", usenetHandler.CheckHealth).Methods(http.MethodPost)
	protected.HandleFunc("/usenet/health", handleOptions).Methods(http.MethodOptions)

	protected.HandleFunc("/debrid/proxy", debridHandler.Proxy).Methods(http.MethodGet, http.MethodHead)
	protected.HandleFunc("/debrid/proxy", debridHandler.Options).Methods(http.MethodOptions)
	protected.HandleFunc("/debrid/cached", debridHandler.CheckCached).Methods(http.MethodPost)
	protected.HandleFunc("/debrid/cached", debridHandler.Options).Methods(http.MethodOptions)

	protected.HandleFunc("/live/playlist", liveHandler.FetchPlaylist).Methods(http.MethodGet)
	protected.HandleFunc("/live/playlist", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/live/channels", liveHandler.GetChannels).Methods(http.MethodGet)
	protected.HandleFunc("/live/channels", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/live/categories", liveHandler.GetCategories).Methods(http.MethodGet)
	protected.HandleFunc("/live/categories", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/live/cache/clear", liveHandler.ClearCache).Methods(http.MethodPost)
	protected.HandleFunc("/live/cache/clear", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/live/stream", liveHandler.StreamChannel).Methods(http.MethodGet, http.MethodHead)
	protected.HandleFunc("/live/stream", handleOptions).Methods(http.MethodOptions)
	protected.HandleFunc("/live/hls/start", videoHandler.StartLiveHLSSession).Methods(http.MethodGet, http.MethodOptions)

	// EPG (Electronic Program Guide) endpoints
	if epgHandler != nil {
		protected.HandleFunc("/live/epg/now", epgHandler.GetNowPlaying).Methods(http.MethodGet)
		protected.HandleFunc("/live/epg/now", epgHandler.Options).Methods(http.MethodOptions)
		protected.HandleFunc("/live/epg/schedule", epgHandler.GetSchedule).Methods(http.MethodGet)
		protected.HandleFunc("/live/epg/schedule", epgHandler.Options).Methods(http.MethodOptions)
		protected.HandleFunc("/live/epg/schedule/batch", epgHandler.GetScheduleMultiple).Methods(http.MethodGet)
		protected.HandleFunc("/live/epg/schedule/batch", epgHandler.Options).Methods(http.MethodOptions)
		protected.HandleFunc("/live/epg/channel/{id}", epgHandler.GetChannelSchedule).Methods(http.MethodGet)
		protected.HandleFunc("/live/epg/channel/{id}", epgHandler.Options).Methods(http.MethodOptions)
		protected.HandleFunc("/live/epg/status", epgHandler.GetStatus).Methods(http.MethodGet)
		protected.HandleFunc("/live/epg/status", epgHandler.Options).Methods(http.MethodOptions)
		protected.HandleFunc("/live/epg/refresh", epgHandler.Refresh).Methods(http.MethodPost)
		protected.HandleFunc("/live/epg/refresh", epgHandler.Options).Methods(http.MethodOptions)
	}

	// Video streaming endpoints
	protected.HandleFunc("/video/stream", videoHandler.StreamVideo).Methods(http.MethodGet, http.MethodHead, http.MethodOptions)
	protected.HandleFunc("/video/metadata", videoHandler.ProbeVideo).Methods(http.MethodGet, http.MethodOptions)
	protected.HandleFunc("/video/direct-url", videoHandler.GetDirectURL).Methods(http.MethodGet, http.MethodOptions)

	// HLS streaming endpoints for Dolby Vision
	protected.HandleFunc("/video/hls/start", videoHandler.StartHLSSession).Methods(http.MethodGet, http.MethodOptions)
	protected.HandleFunc("/video/hls/{sessionID}/stream.m3u8", videoHandler.ServeHLSPlaylist).Methods(http.MethodGet, http.MethodOptions)
	protected.HandleFunc("/video/hls/{sessionID}/subtitles.vtt", videoHandler.ServeHLSSubtitles).Methods(http.MethodGet, http.MethodOptions)
	protected.HandleFunc("/video/hls/{sessionID}/keepalive", videoHandler.KeepAliveHLSSession).Methods(http.MethodPost, http.MethodOptions)
	protected.HandleFunc("/video/hls/{sessionID}/status", videoHandler.GetHLSSessionStatus).Methods(http.MethodGet, http.MethodOptions)
	protected.HandleFunc("/video/hls/{sessionID}/seek", videoHandler.SeekHLSSession).Methods(http.MethodPost, http.MethodOptions)
	protected.HandleFunc("/video/hls/{sessionID}/{segment}", videoHandler.ServeHLSSegment).Methods(http.MethodGet, http.MethodOptions)

	// Standalone subtitle extraction endpoints (for non-HLS streams)
	protected.HandleFunc("/video/subtitles/tracks", videoHandler.ProbeSubtitleTracks).Methods(http.MethodGet, http.MethodOptions)
	protected.HandleFunc("/video/subtitles/start", videoHandler.StartSubtitleExtract).Methods(http.MethodGet, http.MethodOptions)
	protected.HandleFunc("/video/subtitles/{sessionID}/subtitles.vtt", videoHandler.ServeExtractedSubtitles).Methods(http.MethodGet, http.MethodOptions)

	// Subtitle search endpoints (using subliminal)
	protected.HandleFunc("/subtitles/search", subtitlesHandler.Search).Methods(http.MethodGet)
	protected.HandleFunc("/subtitles/search", subtitlesHandler.Options).Methods(http.MethodOptions)
	protected.HandleFunc("/subtitles/download", subtitlesHandler.Download).Methods(http.MethodGet)
	protected.HandleFunc("/subtitles/download", subtitlesHandler.Options).Methods(http.MethodOptions)

	protected.HandleFunc("/debug/log", debugHandler.Capture).Methods(http.MethodPost, http.MethodOptions)

	// Log submission endpoint
	protected.HandleFunc("/logs/submit", logsHandler.Submit).Methods(http.MethodPost)
	protected.HandleFunc("/logs/submit", logsHandler.Options).Methods(http.MethodOptions)

	// Version endpoint (public)
	versionHandler := handlers.NewVersionHandler()
	api.HandleFunc("/version", versionHandler.GetVersion).Methods(http.MethodGet, http.MethodOptions)

	// Homepage dashboard integration endpoint (public - for Homepage widgets)
	homepageHandler := handlers.NewHomepageHandler(accountsSvc)
	homepageHandler.SetUserService(usersSvc)
	homepageHandler.SetHLSManager(videoHandler.GetHLSManager())
	homepageHandler.SetProgressService(historyHandler.Service)
	homepageHandler.SetMetadataService(metadataHandler.Service)
	api.HandleFunc("/homepage", homepageHandler.GetStats).Methods(http.MethodGet, http.MethodOptions)

	// Static assets endpoint (public - rating icons, etc.)
	staticHandler := handlers.NewStaticHandler()
	api.PathPrefix("/static/").Handler(http.StripPrefix("/api/static/", staticHandler))

	// Image proxy endpoint (public - no auth required for image loading)
	if imageHandler != nil {
		api.HandleFunc("/images/proxy", imageHandler.Proxy).Methods(http.MethodGet, http.MethodHead)
		api.HandleFunc("/images/proxy", imageHandler.Options).Methods(http.MethodOptions)
	}

	// Admin endpoints for monitoring (master only)
	adminHandler := handlers.NewAdminHandler(videoHandler.GetHLSManager())
	adminHandler.SetProgressService(historyHandler.Service)
	adminHandler.SetUserService(usersSvc)
	adminRouter := protected.PathPrefix("/admin").Subrouter()
	adminRouter.Use(MasterOnlyMiddleware())
	adminRouter.HandleFunc("/streams", adminHandler.GetActiveStreams).Methods(http.MethodGet, http.MethodOptions)

	// Pprof debug endpoints for profiling (localhost only, no auth required for debugging)
	// These are essential for diagnosing production issues and are safe since they're read-only
	pprofRouter := api.PathPrefix("/debug/pprof").Subrouter()
	pprofRouter.Use(localhostOnlyMiddleware)
	pprofRouter.HandleFunc("/", pprof.Index)
	pprofRouter.HandleFunc("/cmdline", pprof.Cmdline)
	pprofRouter.HandleFunc("/profile", pprof.Profile)
	pprofRouter.HandleFunc("/symbol", pprof.Symbol)
	pprofRouter.HandleFunc("/trace", pprof.Trace)
	pprofRouter.HandleFunc("/allocs", pprof.Handler("allocs").ServeHTTP)
	pprofRouter.HandleFunc("/block", pprof.Handler("block").ServeHTTP)
	pprofRouter.HandleFunc("/goroutine", pprof.Handler("goroutine").ServeHTTP)
	pprofRouter.HandleFunc("/heap", pprof.Handler("heap").ServeHTTP)
	pprofRouter.HandleFunc("/mutex", pprof.Handler("mutex").ServeHTTP)
	pprofRouter.HandleFunc("/threadcreate", pprof.Handler("threadcreate").ServeHTTP)

	// Dev tools (localhost + docker hostname only, no auth required)
	devHandler := handlers.NewDevHandler("/root/strmr/docs")
	devRouter := api.PathPrefix("/dev").Subrouter()
	devRouter.Use(devOnlyMiddleware)
	devRouter.HandleFunc("/todo", devHandler.TodoEditor).Methods(http.MethodGet, http.MethodPost)

	// Runtime stats endpoint (localhost only, no auth required for debugging)
	runtimeRouter := api.PathPrefix("/debug/runtime").Subrouter()
	runtimeRouter.Use(localhostOnlyMiddleware)
	runtimeRouter.HandleFunc("", func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{` +
			`"goroutines":` + itoa(runtime.NumGoroutine()) + `,` +
			`"heapAlloc":` + itoa64(m.HeapAlloc) + `,` +
			`"heapSys":` + itoa64(m.HeapSys) + `,` +
			`"heapInuse":` + itoa64(m.HeapInuse) + `,` +
			`"heapObjects":` + itoa64(m.HeapObjects) + `,` +
			`"stackInuse":` + itoa64(m.StackInuse) + `,` +
			`"stackSys":` + itoa64(m.StackSys) + `,` +
			`"mSpanInuse":` + itoa64(m.MSpanInuse) + `,` +
			`"mCacheInuse":` + itoa64(m.MCacheInuse) + `,` +
			`"numGC":` + itoa(int(m.NumGC)) + `,` +
			`"lastGC":` + itoa64(m.LastGC) + `,` +
			`"pauseTotalNs":` + itoa64(m.PauseTotalNs) + `,` +
			`"numCgoCall":` + itoa64(uint64(runtime.NumCgoCall())) + `,` +
			`"numCPU":` + itoa(runtime.NumCPU()) +
			`}`))
	}).Methods(http.MethodGet)

	// Runtime stats endpoint (master only, authenticated)
	adminRouter.HandleFunc("/runtime", func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{` +
			`"goroutines":` + itoa(runtime.NumGoroutine()) + `,` +
			`"heapAlloc":` + itoa64(m.HeapAlloc) + `,` +
			`"heapSys":` + itoa64(m.HeapSys) + `,` +
			`"heapObjects":` + itoa64(m.HeapObjects) + `,` +
			`"stackInuse":` + itoa64(m.StackInuse) + `,` +
			`"numGC":` + itoa(int(m.NumGC)) + `,` +
			`"lastGC":` + itoa64(m.LastGC) +
			`}`))
	}).Methods(http.MethodGet, http.MethodOptions)

	// User profile routes (with ownership validation)
	profileProtected.HandleFunc("", usersHandler.List).Methods(http.MethodGet)
	profileProtected.HandleFunc("", usersHandler.Create).Methods(http.MethodPost)
	profileProtected.HandleFunc("", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}", usersHandler.Rename).Methods(http.MethodPatch)
	profileProtected.HandleFunc("/{userID}", usersHandler.Delete).Methods(http.MethodDelete)
	profileProtected.HandleFunc("/{userID}", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/color", usersHandler.SetColor).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/color", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/icon", usersHandler.SetIconURL).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/icon", usersHandler.ClearIconURL).Methods(http.MethodDelete)
	profileProtected.HandleFunc("/{userID}/icon", usersHandler.ServeProfileIcon).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/icon", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/icon/upload", usersHandler.UploadProfileIcon).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/icon/upload", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/pin", usersHandler.SetPin).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/pin", usersHandler.ClearPin).Methods(http.MethodDelete)
	profileProtected.HandleFunc("/{userID}/pin", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/pin/verify", usersHandler.VerifyPin).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/pin/verify", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/trakt", usersHandler.SetTraktAccount).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/trakt", usersHandler.ClearTraktAccount).Methods(http.MethodDelete)
	profileProtected.HandleFunc("/{userID}/trakt", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/kids-profile", usersHandler.SetKidsProfile).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/kids-profile", usersHandler.Options).Methods(http.MethodOptions)
	// Kids profile content restriction configuration
	profileProtected.HandleFunc("/{userID}/kids/mode", usersHandler.SetKidsMode).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/kids/mode", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/kids/rating", usersHandler.SetKidsMaxRating).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/kids/rating", usersHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/kids/lists", usersHandler.SetKidsAllowedLists).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/kids/lists", usersHandler.AddKidsAllowedList).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/kids/lists", usersHandler.RemoveKidsAllowedList).Methods(http.MethodDelete)
	profileProtected.HandleFunc("/{userID}/kids/lists", usersHandler.Options).Methods(http.MethodOptions)

	profileProtected.HandleFunc("/{userID}/settings", userSettingsHandler.GetSettings).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/settings", userSettingsHandler.PutSettings).Methods(http.MethodPut)
	profileProtected.HandleFunc("/{userID}/settings", userSettingsHandler.Options).Methods(http.MethodOptions)

	// Client device management routes
	if clientsHandler != nil {
		// Registration endpoint (all authenticated users)
		protected.HandleFunc("/clients/register", clientsHandler.Register).Methods(http.MethodPost)
		protected.HandleFunc("/clients/register", clientsHandler.Options).Methods(http.MethodOptions)

		// Client management (master only for list all, otherwise filtered by user)
		protected.HandleFunc("/clients", clientsHandler.List).Methods(http.MethodGet)
		protected.HandleFunc("/clients", clientsHandler.Options).Methods(http.MethodOptions)
		protected.HandleFunc("/clients/{clientID}", clientsHandler.Get).Methods(http.MethodGet)
		protected.HandleFunc("/clients/{clientID}", clientsHandler.Update).Methods(http.MethodPut)
		protected.HandleFunc("/clients/{clientID}", clientsHandler.Delete).Methods(http.MethodDelete)
		protected.HandleFunc("/clients/{clientID}", clientsHandler.Options).Methods(http.MethodOptions)

		// Client-specific filter settings
		protected.HandleFunc("/clients/{clientID}/settings", clientsHandler.GetSettings).Methods(http.MethodGet)
		protected.HandleFunc("/clients/{clientID}/settings", clientsHandler.UpdateSettings).Methods(http.MethodPut)
		protected.HandleFunc("/clients/{clientID}/settings", clientsHandler.Options).Methods(http.MethodOptions)

		// Client ping check (for device identification)
		protected.HandleFunc("/clients/{clientID}/ping", clientsHandler.CheckPing).Methods(http.MethodGet)
		protected.HandleFunc("/clients/{clientID}/ping", clientsHandler.Options).Methods(http.MethodOptions)
	}

	profileProtected.HandleFunc("/{userID}/watchlist", watchlistHandler.List).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/watchlist", watchlistHandler.Add).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/watchlist", watchlistHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/watchlist/{mediaType}/{id}", watchlistHandler.UpdateState).Methods(http.MethodPatch)
	profileProtected.HandleFunc("/{userID}/watchlist/{mediaType}/{id}", watchlistHandler.Remove).Methods(http.MethodDelete)
	profileProtected.HandleFunc("/{userID}/watchlist/{mediaType}/{id}", watchlistHandler.Options).Methods(http.MethodOptions)

	profileProtected.HandleFunc("/{userID}/history/continue", historyHandler.ListContinueWatching).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/history/continue", historyHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/history/continue/{seriesID}/hide", historyHandler.HideFromContinueWatching).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/history/continue/{seriesID}/hide", historyHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/history/series/{seriesID}", historyHandler.GetSeriesWatchState).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/history/series/{seriesID}", historyHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/history/episodes", historyHandler.RecordEpisode).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/history/episodes", historyHandler.Options).Methods(http.MethodOptions)

	// Watch History endpoints (unified watch tracking for all media)
	profileProtected.HandleFunc("/{userID}/history/watched", historyHandler.ListWatchHistory).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/history/watched", historyHandler.UpdateWatchHistory).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/history/watched", historyHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/history/watched/bulk", historyHandler.BulkUpdateWatchHistory).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/history/watched/bulk", historyHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/history/watched/{mediaType}/{id}", historyHandler.GetWatchHistoryItem).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/history/watched/{mediaType}/{id}", historyHandler.UpdateWatchHistory).Methods(http.MethodPatch)
	profileProtected.HandleFunc("/{userID}/history/watched/{mediaType}/{id}/toggle", historyHandler.ToggleWatched).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/history/watched/{mediaType}/{id}", historyHandler.Options).Methods(http.MethodOptions)

	// Playback Progress endpoints (continuous progress tracking for native player)
	profileProtected.HandleFunc("/{userID}/history/progress", historyHandler.ListPlaybackProgress).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/history/progress", historyHandler.UpdatePlaybackProgress).Methods(http.MethodPost)
	profileProtected.HandleFunc("/{userID}/history/progress", historyHandler.Options).Methods(http.MethodOptions)
	profileProtected.HandleFunc("/{userID}/history/progress/{mediaType}/{id}", historyHandler.GetPlaybackProgress).Methods(http.MethodGet)
	profileProtected.HandleFunc("/{userID}/history/progress/{mediaType}/{id}", historyHandler.UpdatePlaybackProgress).Methods(http.MethodPatch)
	profileProtected.HandleFunc("/{userID}/history/progress/{mediaType}/{id}", historyHandler.DeletePlaybackProgress).Methods(http.MethodDelete)
	profileProtected.HandleFunc("/{userID}/history/progress/{mediaType}/{id}", historyHandler.Options).Methods(http.MethodOptions)

	// Content Preferences endpoints (per-content audio/subtitle preferences)
	if contentPreferencesHandler != nil {
		profileProtected.HandleFunc("/{userID}/preferences/content", contentPreferencesHandler.ListPreferences).Methods(http.MethodGet)
		profileProtected.HandleFunc("/{userID}/preferences/content", contentPreferencesHandler.SetPreference).Methods(http.MethodPut)
		profileProtected.HandleFunc("/{userID}/preferences/content", contentPreferencesHandler.Options).Methods(http.MethodOptions)
		profileProtected.HandleFunc("/{userID}/preferences/content/{contentID}", contentPreferencesHandler.GetPreference).Methods(http.MethodGet)
		profileProtected.HandleFunc("/{userID}/preferences/content/{contentID}", contentPreferencesHandler.DeletePreference).Methods(http.MethodDelete)
		profileProtected.HandleFunc("/{userID}/preferences/content/{contentID}", contentPreferencesHandler.Options).Methods(http.MethodOptions)
	}
}

// RegisterTraktRoutes registers Trakt account management API endpoints.
func RegisterTraktRoutes(r *mux.Router, traktHandler *handlers.TraktAccountsHandler, sessionsSvc *sessions.Service) {
	api := r.PathPrefix("/api/trakt").Subrouter()
	api.Use(corsMiddleware)
	api.Use(AccountAuthMiddleware(sessionsSvc))

	// Trakt accounts management
	api.HandleFunc("/accounts", traktHandler.ListAccounts).Methods(http.MethodGet)
	api.HandleFunc("/accounts", traktHandler.CreateAccount).Methods(http.MethodPost)
	api.HandleFunc("/accounts", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/accounts/{accountID}", traktHandler.GetAccount).Methods(http.MethodGet)
	api.HandleFunc("/accounts/{accountID}", traktHandler.UpdateAccount).Methods(http.MethodPatch)
	api.HandleFunc("/accounts/{accountID}", traktHandler.DeleteAccount).Methods(http.MethodDelete)
	api.HandleFunc("/accounts/{accountID}", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/accounts/{accountID}/auth/start", traktHandler.StartAuth).Methods(http.MethodPost)
	api.HandleFunc("/accounts/{accountID}/auth/start", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/accounts/{accountID}/auth/check/{deviceCode}", traktHandler.CheckAuth).Methods(http.MethodGet)
	api.HandleFunc("/accounts/{accountID}/auth/check/{deviceCode}", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/accounts/{accountID}/disconnect", traktHandler.Disconnect).Methods(http.MethodPost)
	api.HandleFunc("/accounts/{accountID}/disconnect", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/accounts/{accountID}/scrobbling", traktHandler.SetScrobbling).Methods(http.MethodPost)
	api.HandleFunc("/accounts/{accountID}/scrobbling", handleOptions).Methods(http.MethodOptions)
	api.HandleFunc("/accounts/{accountID}/history", traktHandler.GetHistory).Methods(http.MethodGet)
	api.HandleFunc("/accounts/{accountID}/history", handleOptions).Methods(http.MethodOptions)
}
