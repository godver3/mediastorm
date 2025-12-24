package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"novastream/api"
	"novastream/config"
	"novastream/handlers"
	"novastream/internal/database"
	"novastream/internal/integration"
	"novastream/internal/pool"
	"novastream/internal/webdav"
	"novastream/services/debrid"
	"novastream/services/history"
	"novastream/services/indexer"
	"novastream/services/metadata"
	"novastream/services/playback"
	"novastream/services/usenet"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
	"novastream/services/watchlist"
	"novastream/utils"

	"github.com/gorilla/mux"
)

func main() {

	demoMode := flag.Bool("demo", false, "serve curated public domain metadata instead of live feeds")
	portOverride := flag.Int("port", 0, "override server port from config")
	flag.Parse()

	fmt.Println("🚀 strmr Backend Starting...")
	if *demoMode {
		fmt.Println("🧪 Demo mode enabled: returning curated public domain trending rows.")
	}

	// Determine config path (env or default)
	configPath := os.Getenv("STRMR_CONFIG")
	if configPath == "" {
		configPath = os.Getenv("NOVASTREAM_CONFIG") // legacy env var
	}
	if configPath == "" {
		configPath = filepath.Join("cache", "settings.json")
	}

	// Init config manager and load settings (creates defaults if missing)
	cfgManager := config.NewManager(configPath)
	settings, err := cfgManager.Load()
	if err != nil {
		log.Fatalf("failed to load settings: %v", err)
	}

	// Apply port override if specified
	if *portOverride > 0 {
		settings.Server.Port = *portOverride
	}

	// Handle PIN generation and legacy API key migration
	settings.Server.APIKey = strings.TrimSpace(settings.Server.APIKey)
	settings.Server.PIN = strings.TrimSpace(settings.Server.PIN)

	pinGenerated := false
	legacyKeyFound := false

	// Check if we have a legacy API key but no PIN
	if settings.Server.APIKey != "" && settings.Server.PIN == "" {
		legacyKeyFound = true
		fmt.Println("🔄 Legacy API key detected, generating new 6-digit PIN...")
	}

	// Generate PIN if missing
	if settings.Server.PIN == "" {
		pin, err := utils.GeneratePIN()
		if err != nil {
			log.Fatalf("failed to generate PIN: %v", err)
		}
		settings.Server.PIN = pin
		if err := cfgManager.Save(settings); err != nil {
			log.Fatalf("failed to persist generated PIN: %v", err)
		}
		pinGenerated = true
	}

	fmt.Printf("🔑 strmr PIN: %s\n", settings.Server.PIN)
	if pinGenerated {
		if legacyKeyFound {
			fmt.Println("✅ Legacy API key has been replaced with a 6-digit PIN.")
			fmt.Println("📱 Update your frontend configuration to use the PIN instead of the API key.")
		} else {
			fmt.Println("📱 Configure your frontend to use this 6-digit PIN for authentication.")
		}
	}

	// Construct router
	var r *mux.Router = utils.NewRouter()

	// Register API routes
	settingsHandler := handlers.NewSettingsHandlerWithDemoMode(cfgManager, *demoMode)
	metadataService := metadata.NewService(settings.Metadata.TVDBAPIKey, settings.Metadata.TMDBAPIKey, settings.Metadata.Language, settings.Cache.Directory, settings.Cache.MetadataTTLHours, *demoMode)
	metadataHandler := handlers.NewMetadataHandler(metadataService, cfgManager)
	debridSearchService := debrid.NewSearchService(cfgManager)
	indexerService := indexer.NewService(cfgManager, metadataService, debridSearchService)
	indexerHandler := handlers.NewIndexerHandler(indexerService, *demoMode)
	// Note: user settings service wiring happens later after userSettingsService is created
	debridProxyService := debrid.NewProxyService(cfgManager)
	// Create HealthService with ffprobe path for pre-resolved stream validation
	debridHealthService := debrid.NewHealthService(cfgManager)
	debridHealthService.SetFFProbePath(settings.Transmux.FFprobePath)
	debridPlaybackService := debrid.NewPlaybackService(cfgManager, debridHealthService)
	debridHandler := handlers.NewDebridHandler(debridProxyService, debridPlaybackService)

	// Initialize pool manager early so usenet service can use it
	poolManager := pool.NewManager()
	settingsHandler.SetPoolManager(poolManager)           // Enable hot reload of usenet providers
	settingsHandler.SetMetadataService(metadataService)   // Enable hot reload of API keys
	settingsHandler.SetDebridSearchService(debridSearchService) // Enable hot reload of scrapers

	usenetService := usenet.NewService(cfgManager, poolManager)
	streamRoot := filepath.Join(settings.Cache.Directory, "streams")
	if err := os.MkdirAll(streamRoot, 0o755); err != nil {
		log.Fatalf("failed to create stream cache: %v", err)
	}

	// Initialize config adapter for altmount compatibility
	configAdapter := config.NewConfigAdapter(cfgManager)

	// Initialize NNTP pool if configured
	debugArticleID := strings.TrimSpace(os.Getenv("NOVASTREAM_DEBUG_ARTICLE_ID"))
	debugGroupsEnv := strings.TrimSpace(os.Getenv("NOVASTREAM_DEBUG_ARTICLE_GROUPS"))
	var debugGroups []string
	if debugGroupsEnv != "" {
		for _, g := range strings.Split(debugGroupsEnv, ",") {
			trimmed := strings.TrimSpace(g)
			if trimmed != "" {
				debugGroups = append(debugGroups, trimmed)
			}
		}
	}
	providers := config.ToNNTPProviders(settings.Usenet)
	if len(providers) > 0 {
		if err := poolManager.SetProviders(providers); err != nil {
			log.Printf("warning: failed to initialize usenet pool: %v", err)
		} else {
			log.Printf("initialized usenet pool with %d provider(s)", len(providers))
			if debugArticleID != "" {
				func() {
					ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
					defer cancel()

					if err := warmUpUsenetArticle(ctx, poolManager, debugArticleID, debugGroups); err != nil {
						slog.Warn("startup NNTP warmup failed",
							"article_id", debugArticleID,
							"groups", debugGroups,
							"error", err,
						)
					}
				}()
			}
		}
	} else {
		log.Printf("warning: no usenet providers configured; streaming will be disabled")
	}

	// Initialize NZB system with queue and metadata
	nzbSystemConfig := integration.NzbConfig{
		QueueDatabasePath:   settings.Database.Path,
		MetadataRootPath:    streamRoot,
		Password:            "", // Not used
		Salt:                "", // Not used
		MaxProcessorWorkers: 2,
		MaxDownloadWorkers:  settings.Streaming.MaxDownloadWorkers,
	}

	nzbSystem, err := integration.NewNzbSystem(nzbSystemConfig, poolManager, configAdapter.GetConfigGetter())
	if err != nil {
		log.Fatalf("failed to initialize NZB system: %v", err)
	}
	defer nzbSystem.Close()

	// Create WebDAV handler if enabled
	var webdavHandler http.Handler
	if settings.WebDAV.Enabled {
		// Generate WebDAV password if not set
		if strings.TrimSpace(settings.WebDAV.Password) == "" {
			webdavPass, err := utils.GenerateAPIKey()
			if err != nil {
				log.Fatalf("failed to generate WebDAV password: %v", err)
			}
			settings.WebDAV.Password = webdavPass
			if err := cfgManager.Save(settings); err != nil {
				log.Printf("warning: failed to save WebDAV password: %v", err)
			}
			fmt.Printf("🔐 WebDAV credentials: %s / %s\n", settings.WebDAV.Username, settings.WebDAV.Password)
		}

		webdavConfig := &webdav.Config{
			Prefix: settings.WebDAV.Prefix,
			User:   settings.WebDAV.Username,
			Pass:   settings.WebDAV.Password,
		}

		// Get database for user repository
		db := nzbSystem.Database()
		userRepo := database.NewUserRepository(db.Connection())

		handler, err := webdav.NewHandler(webdavConfig, nzbSystem.FileSystem(), nil, userRepo, configAdapter.GetConfigGetter())
		if err != nil {
			log.Fatalf("failed to create WebDAV handler: %v", err)
		}
		webdavHandler = handler.GetHTTPHandler()
		fmt.Printf("📁 WebDAV endpoint enabled at %s\n", settings.WebDAV.Prefix)
	}

	playbackService := playback.NewService(cfgManager, usenetService, nzbSystem, nzbSystem.MetadataReader())
	playbackHandler := handlers.NewPlaybackHandler(playbackService)
	// Prequeue handler will be created later after historyService is available
	var prequeueHandler *handlers.PrequeueHandler
	usenetHandler := handlers.NewUsenetHandler(usenetService)
	userService, err := users.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise users: %v", err)
	}
	usersHandler := handlers.NewUsersHandler(userService)
	debugHandler := handlers.NewDebugHandler(log.New(os.Stdout, "[debug] ", log.LstdFlags))

	watchlistService, err := watchlist.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise watchlist: %v", err)
	}
	watchlistHandler := handlers.NewWatchlistHandler(watchlistService, userService, *demoMode)

	userSettingsService, err := user_settings.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise user settings: %v", err)
	}
	userSettingsHandler := handlers.NewUserSettingsHandler(userSettingsService, userService, cfgManager)

	// Wire up user settings to services for per-user settings
	debridSearchService.SetUserSettingsProvider(userSettingsService)
	debridSearchService.SetIMDBResolver(metadataService) // Fallback IMDB ID resolution via TVDB
	indexerService.SetUserSettingsProvider(userSettingsService)
	metadataHandler.SetUserSettingsProvider(userSettingsService)

	historyService, err := history.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise watch history: %v", err)
	}
	// Wire up metadata service for continue watching generation
	historyService.SetMetadataService(metadataService)
	historyHandler := handlers.NewHistoryHandler(historyService, userService, *demoMode)

	// Create prequeue handler now that history service is available
	// Video prober and HLS creator are optional - we'll set them after videoHandler is created
	prequeueHandler = handlers.NewPrequeueHandler(indexerService, playbackService, historyService, nil, nil, *demoMode)

	if settings.Transmux.FFmpegPath == "" {
		settings.Transmux.FFmpegPath = "ffmpeg"
	}

	// Best-effort save so the config persists the defaults
	_ = cfgManager.Save(settings)

	// Create composite streaming provider that handles both usenet and debrid
	debridStreamingProvider := debrid.NewStreamingProvider(cfgManager)
	compositeProvider := debrid.NewCompositeProvider(debridStreamingProvider, nzbSystem)

	// Create video handler with composite provider
	videoHandler := handlers.NewVideoHandlerWithProvider(
		settings.Transmux.Enabled,
		settings.Transmux.FFmpegPath,
		settings.Transmux.FFprobePath,
		compositeProvider,
	)

	if videoHandler != nil && settings.WebDAV.Enabled {
		localBaseURL := fmt.Sprintf("http://127.0.0.1:%d", settings.Server.Port)
		videoHandler.ConfigureLocalWebDAVAccess(localBaseURL, settings.WebDAV.Prefix, settings.WebDAV.Username, settings.WebDAV.Password)
	}

	// Wire up prequeue handler with video prober, HLS creator, metadata prober, user settings, and config
	// This allows prequeue to detect Dolby Vision/HDR10, create HLS sessions, and select tracks with proper defaults
	if videoHandler != nil {
		prequeueHandler.SetVideoProber(videoHandler)
		prequeueHandler.SetHLSCreator(videoHandler)
		prequeueHandler.SetMetadataProber(videoHandler)
		prequeueHandler.SetUserSettingsService(userSettingsService)
		prequeueHandler.SetConfigManager(cfgManager)
		log.Printf("[main] Prequeue handler configured with video prober, HLS creator, metadata prober, user settings, and config")
	}

	liveHandler := handlers.NewLiveHandler(nil, settings.Transmux.Enabled, settings.Transmux.FFmpegPath, settings.Live.PlaylistCacheTTLHours)

	// Create debug video handler with MP4Box for DV/HDR testing
	debugVideoHandler := handlers.NewDebugVideoHandler("MP4Box", settings.Transmux.FFprobePath)

	// Create subtitles handler for external subtitle search
	subtitlesHandler := handlers.NewSubtitlesHandlerWithConfig(cfgManager)

	// Create PIN getter function for hot reload support
	getPIN := func() string {
		s, err := cfgManager.Load()
		if err != nil {
			return settings.Server.PIN // fallback to initial value on error
		}
		return s.Server.PIN
	}

	api.Register(
		r,
		settingsHandler,
		metadataHandler,
		indexerHandler,
		playbackHandler,
		prequeueHandler,
		usenetHandler,
		debridHandler,
		videoHandler,
		usersHandler,
		watchlistHandler,
		historyHandler,
		debugHandler,
		liveHandler,
		debugVideoHandler,
		userSettingsHandler,
		subtitlesHandler,
		getPIN,
	)

	// Register admin UI routes
	adminUIHandler := handlers.NewAdminUIHandler(configPath, videoHandler.GetHLSManager(), userService, userSettingsService, cfgManager, getPIN)
	adminUIHandler.SetMetadataService(metadataService)
	adminUIHandler.SetHistoryService(historyService)
	adminUIHandler.SetWatchlistService(watchlistService)

	// Login/logout routes (no auth required)
	r.HandleFunc("/admin/login", adminUIHandler.LoginPage).Methods(http.MethodGet)
	r.HandleFunc("/admin/login", adminUIHandler.LoginSubmit).Methods(http.MethodPost)
	r.HandleFunc("/admin/logout", adminUIHandler.Logout).Methods(http.MethodGet, http.MethodPost)

	// Protected admin routes (require PIN authentication)
	r.HandleFunc("/admin", adminUIHandler.RequireAuth(adminUIHandler.Dashboard)).Methods(http.MethodGet)
	r.HandleFunc("/admin/", adminUIHandler.RequireAuth(adminUIHandler.Dashboard)).Methods(http.MethodGet)
	r.HandleFunc("/admin/settings", adminUIHandler.RequireAuth(adminUIHandler.SettingsPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/status", adminUIHandler.RequireAuth(adminUIHandler.StatusPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/history", adminUIHandler.RequireAuth(adminUIHandler.HistoryPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/tools", adminUIHandler.RequireAuth(adminUIHandler.ToolsPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/schema", adminUIHandler.RequireAuth(adminUIHandler.GetSchema)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/status", adminUIHandler.RequireAuth(adminUIHandler.GetStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/streams", adminUIHandler.RequireAuth(adminUIHandler.GetStreams)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/debrid-status", adminUIHandler.RequireAuth(adminUIHandler.GetDebridStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.GetUserSettings)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.SaveUserSettings)).Methods(http.MethodPut)

	// Provider test endpoints
	r.HandleFunc("/admin/api/test/indexer", adminUIHandler.RequireAuth(adminUIHandler.TestIndexer)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/test/scraper", adminUIHandler.RequireAuth(adminUIHandler.TestScraper)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/test/usenet-provider", adminUIHandler.RequireAuth(adminUIHandler.TestUsenetProvider)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/test/debrid-provider", adminUIHandler.RequireAuth(adminUIHandler.TestDebridProvider)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/test/subtitles", adminUIHandler.RequireAuth(adminUIHandler.TestSubtitles)).Methods(http.MethodPost)

	// Profile management endpoints
	r.HandleFunc("/admin/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.GetProfiles)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.CreateProfile)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.RenameProfile)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.DeleteProfile)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/profiles/pin", adminUIHandler.RequireAuth(adminUIHandler.SetProfilePin)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/profiles/pin", adminUIHandler.RequireAuth(adminUIHandler.ClearProfilePin)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/profiles/color", adminUIHandler.RequireAuth(adminUIHandler.SetProfileColor)).Methods(http.MethodPut)

	// Cache management endpoints
	r.HandleFunc("/admin/api/cache/clear", adminUIHandler.RequireAuth(adminUIHandler.ClearMetadataCache)).Methods(http.MethodPost)

	// History endpoints (admin session auth, no PIN required)
	r.HandleFunc("/admin/api/history/watched", adminUIHandler.RequireAuth(adminUIHandler.GetWatchHistory)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/history/continue", adminUIHandler.RequireAuth(adminUIHandler.GetContinueWatching)).Methods(http.MethodGet)

	// Plex import endpoints
	r.HandleFunc("/admin/api/plex/pin", adminUIHandler.RequireAuth(adminUIHandler.PlexCreatePIN)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/plex/pin/{id}", adminUIHandler.RequireAuth(adminUIHandler.PlexCheckPIN)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/watchlist", adminUIHandler.RequireAuth(adminUIHandler.PlexGetWatchlist)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/import", adminUIHandler.RequireAuth(adminUIHandler.PlexImportWatchlist)).Methods(http.MethodPost)

	fmt.Println("📊 Admin dashboard available at /admin")

	// Redirect root to admin dashboard
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	}).Methods(http.MethodGet)

	// Mount WebDAV handler if enabled
	if webdavHandler != nil {
		r.PathPrefix(settings.WebDAV.Prefix + "/").Handler(webdavHandler)
		fmt.Printf("✅ WebDAV mounted at %s\n", settings.WebDAV.Prefix)
	}

	addr := fmt.Sprintf("%s:%d", settings.Server.Host, settings.Server.Port)
	fmt.Printf("Server starting on %s\n", addr)

	// Create HTTP server with timeouts
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // No write timeout for streaming
		IdleTimeout:  120 * time.Second,
	}

	// Setup graceful shutdown
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	// Start server in goroutine
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-shutdownChan
	log.Println("🛑 Shutdown signal received, cleaning up...")

	// Create shutdown context with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Stop NZB system workers first to cancel background processing
	log.Println("🧹 Stopping NZB system workers...")
	if err := nzbSystem.StopService(shutdownCtx); err != nil {
		log.Printf("NZB system shutdown error: %v", err)
	}

	// Cleanup video handler (includes HLS manager shutdown)
	if videoHandler != nil {
		log.Println("🧹 Cleaning up video handler...")
		videoHandler.Shutdown()
	}

	// Cleanup debug video handler (MP4Box sessions)
	if debugVideoHandler != nil {
		log.Println("🧹 Cleaning up debug video handler...")
		debugVideoHandler.Shutdown()
	}

	// Shutdown HTTP server gracefully
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	log.Println("✅ Shutdown complete")
}

type countingWriter struct {
	total int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	cw.total += int64(len(p))
	return len(p), nil
}

func warmUpUsenetArticle(ctx context.Context, manager pool.Manager, messageID string, groups []string) error {
	slog.Info("startup NNTP warmup begin",
		"article_id", messageID,
		"groups", groups,
	)

	cp, err := manager.GetPool()
	if err != nil {
		return fmt.Errorf("get pool: %w", err)
	}

	cw := &countingWriter{}
	writer := io.Writer(cw)

	start := time.Now()
	bytes, err := cp.Body(ctx, messageID, writer, groups)
	duration := time.Since(start)

	if err != nil {
		slog.Warn("startup NNTP warmup error",
			"article_id", messageID,
			"bytes", bytes,
			"counted", cw.total,
			"duration", duration,
			"error", err,
		)
		return err
	}

	slog.Info("startup NNTP warmup complete",
		"article_id", messageID,
		"bytes", bytes,
		"counted", cw.total,
		"duration", duration,
	)

	return nil
}
