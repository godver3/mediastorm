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
	"novastream/services/accounts"
	"novastream/services/calendar"
	"novastream/services/debrid"
	"novastream/services/epg"
	"novastream/services/history"
	"novastream/services/indexer"
	"novastream/services/invitations"
	"novastream/services/metadata"
	"novastream/services/playback"
	"novastream/services/plex"
	"novastream/services/sessions"
	"novastream/services/trakt"
	"novastream/services/usenet"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
	"novastream/services/clients"
	client_settings "novastream/services/client_settings"
	content_preferences "novastream/services/content_preferences"
	"novastream/services/backup"
	"novastream/services/scheduler"
	"novastream/services/watchlist"
	"novastream/utils"

	"github.com/gorilla/mux"
	"golang.org/x/time/rate"
	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {

	demoMode := flag.Bool("demo", false, "serve curated public domain metadata instead of live feeds")
	portOverride := flag.Int("port", 0, "override server port from config")
	flag.Parse()

	fmt.Println("🚀 mediastorm Backend Starting...")
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

	// Set up file logging with rotation
	if settings.Log.File != "" {
		// Ensure log directory exists
		logDir := filepath.Dir(settings.Log.File)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			log.Printf("Warning: could not create log directory %s: %v", logDir, err)
		} else {
			fileWriter := &lumberjack.Logger{
				Filename:   settings.Log.File,
				MaxSize:    settings.Log.MaxSize,
				MaxBackups: settings.Log.MaxBackups,
				MaxAge:     settings.Log.MaxAge,
				Compress:   settings.Log.Compress,
			}
			// Redirect standard log to both console and file
			multiWriter := io.MultiWriter(os.Stdout, fileWriter)
			log.SetOutput(multiWriter)
			log.SetFlags(log.LstdFlags | log.Lshortfile)
			log.Printf("Logging to file: %s", settings.Log.File)
		}
	}

	// Apply port override if specified
	if *portOverride > 0 {
		settings.Server.Port = *portOverride
	}

	// Construct router
	var r *mux.Router = utils.NewRouter()

	// Register API routes
	settingsHandler := handlers.NewSettingsHandlerWithDemoMode(cfgManager, *demoMode)
	mdblistCfg := metadata.MDBListConfig{
		APIKey:         settings.MDBList.APIKey,
		Enabled:        settings.MDBList.Enabled,
		EnabledRatings: settings.MDBList.EnabledRatings,
	}
	metadataService := metadata.NewService(settings.Metadata.TVDBAPIKey, settings.Metadata.TMDBAPIKey, settings.Metadata.Language, settings.Cache.Directory, settings.Cache.MetadataTTLHours, *demoMode, mdblistCfg, settings.Metadata.GeminiAPIKey)
	metadataHandler := handlers.NewMetadataHandler(metadataService, cfgManager)
	debridSearchService := debrid.NewSearchService(cfgManager)
	indexerService := indexer.NewService(cfgManager, metadataService, debridSearchService)
	indexerHandler := handlers.NewIndexerHandler(indexerService, *demoMode)
	indexerHandler.SetMetadataService(metadataService)      // Enable episode resolver for pack size filtering
	indexerHandler.SetMovieMetadataService(metadataService) // Enable movie anime detection for manual search
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

	// Generate Homepage API key if not set
	if strings.TrimSpace(settings.Server.HomepageAPIKey) == "" {
		homepageKey, err := utils.GenerateAPIKey()
		if err != nil {
			log.Fatalf("failed to generate Homepage API key: %v", err)
		}
		settings.Server.HomepageAPIKey = homepageKey
		if err := cfgManager.Save(settings); err != nil {
			log.Printf("warning: failed to save Homepage API key: %v", err)
		}
		fmt.Printf("🔐 Homepage API key: %s\n", settings.Server.HomepageAPIKey)
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

	// Initialize accounts, sessions, and invitations services
	accountsService, err := accounts.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise accounts: %v", err)
	}
	sessionsService, err := sessions.NewService(settings.Cache.Directory, 0) // Use default session duration (30 days)
	if err != nil {
		log.Fatalf("failed to initialise sessions: %v", err)
	}
	invitationsService, err := invitations.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise invitations: %v", err)
	}

	debugHandler := handlers.NewDebugHandler(log.New(os.Stdout, "[debug] ", log.LstdFlags))
	logsHandler := handlers.NewLogsHandler(log.New(os.Stdout, "[logs] ", log.LstdFlags), settings.Log.File)

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

	// Initialize content preferences service for per-content language preferences
	contentPreferencesService, err := content_preferences.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise content preferences: %v", err)
	}
	contentPreferencesHandler := handlers.NewContentPreferencesHandler(contentPreferencesService, userService)

	// Initialize clients service for device tracking
	clientsService, err := clients.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise clients: %v", err)
	}
	clientSettingsService, err := client_settings.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise client settings: %v", err)
	}
	clientsHandler := handlers.NewClientsHandler(clientsService, clientSettingsService)

	// Wire up user settings to services for per-user settings
	debridSearchService.SetUserSettingsProvider(userSettingsService)
	debridSearchService.SetIMDBResolver(metadataService) // Fallback IMDB ID resolution via TVDB
	indexerService.SetUserSettingsProvider(userSettingsService)
	metadataHandler.SetUserSettingsProvider(userSettingsService)

	// Wire up client settings to services for per-client settings cascade
	debridSearchService.SetClientSettingsProvider(clientSettingsService)
	indexerService.SetClientSettingsProvider(clientSettingsService)

	historyService, err := history.NewService(settings.Cache.Directory)
	if err != nil {
		log.Fatalf("failed to initialise watch history: %v", err)
	}
	// Wire up metadata service for continue watching generation
	historyService.SetMetadataService(metadataService)

	// Wire up Trakt scrobbler for syncing watch history
	traktClient := trakt.NewClient("", "") // Credentials are per-account now
	traktScrobbler := trakt.NewScrobbler(traktClient, cfgManager)
	traktScrobbler.SetUserService(userService) // For per-profile Trakt account lookup
	historyService.SetTraktScrobbler(traktScrobbler)

	// Wire up history service to metadata handler for hideWatched filtering
	metadataHandler.SetHistoryService(historyService)
	// Wire up history service to watchlist handler for watch state enrichment
	watchlistHandler.SetHistoryService(historyService)
	// Wire up users service to metadata handler for kids profile filtering
	metadataHandler.SetUsersService(userService)
	// Wire up watchlist service to metadata handler for AI recommendations
	metadataHandler.SetWatchlistService(watchlistService)

	historyHandler := handlers.NewHistoryHandler(historyService, userService, *demoMode)

	// Startup handler bundles multiple API calls for low-power devices
	startupHandler := handlers.NewStartupHandler(
		userSettingsService, watchlistService, historyService,
		metadataService, cfgManager, userService,
	)
	startupHandler.SetUsersProvider(userService)

	// Details bundle handler bundles details-page API calls for low-power devices
	detailsBundleHandler := handlers.NewDetailsBundleHandler(
		metadataService, historyService, contentPreferencesService, userService,
	)

	// Calendar service provides upcoming content from watchlist, history, and MDBList
	calendarService := calendar.New(metadataService, watchlistService, historyService, userSettingsService, userService)
	calendarHandler := handlers.NewCalendarHandler(calendarService, userService, *demoMode)

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
		settings.Transmux.HLSTempDirectory,
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
		prequeueHandler.SetFullProber(videoHandler) // Combined prober for single ffprobe call
		prequeueHandler.SetUserSettingsService(userSettingsService)
		prequeueHandler.SetContentPreferencesService(contentPreferencesService)
		prequeueHandler.SetClientSettingsService(clientSettingsService)
		prequeueHandler.SetConfigManager(cfgManager)
		prequeueHandler.SetMetadataService(metadataService) // For episode counting in pack size filtering
		prequeueHandler.SetMovieMetadataService(metadataService) // For movie anime detection

		// Wire up subtitle pre-extraction for direct streaming (SDR content)
		if subtitleMgr := videoHandler.GetSubtitleExtractManager(); subtitleMgr != nil {
			prequeueHandler.SetSubtitleExtractor(subtitleMgr)
			playbackHandler.SetSubtitleExtractor(subtitleMgr)
			playbackHandler.SetVideoProber(videoHandler) // For probing subtitle streams
			log.Printf("[main] Subtitle pre-extraction configured for prequeue and playback handlers")
		}
		log.Printf("[main] Prequeue handler configured with video prober, HLS creator, full prober, user settings, client settings, config, and metadata")

		// Configure video handler with user settings for HDR/DV policy checks
		videoHandler.SetUserSettingsService(userSettingsService)
		videoHandler.SetClientSettingsService(clientSettingsService)
		videoHandler.SetConfigManager(cfgManager)
	}

	liveHandler := handlers.NewLiveHandler(nil, settings.Transmux.Enabled, settings.Transmux.FFmpegPath, settings.Live.PlaylistCacheTTLHours, settings.Live.ProbeSizeMB, settings.Live.AnalyzeDurationSec, settings.Live.LowLatency, cfgManager, userSettingsService)

	// Create EPG service and handler for Electronic Program Guide
	epgService := epg.NewService(settings.Cache.Directory, cfgManager)
	epgHandler := handlers.NewEPGHandler(epgService)
	settingsHandler.SetEPGService(epgService) // Enable auto-refresh when new EPG sources are added

	// Create subtitles handler for external subtitle search
	subtitlesHandler := handlers.NewSubtitlesHandlerWithConfig(cfgManager)

	// Create image proxy handler for resizing and caching TMDB images
	imageHandler := handlers.NewImageHandler(settings.Cache.Directory)
	settingsHandler.SetImageHandler(imageHandler) // Enable clearing image cache

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
		logsHandler,
		liveHandler,
		epgHandler,
		userSettingsHandler,
		subtitlesHandler,
		clientsHandler,
		contentPreferencesHandler,
		imageHandler,
		startupHandler,
		detailsBundleHandler,
		calendarHandler,
		accountsService,
		sessionsService,
		userService,
		settings.Server.HomepageAPIKey,
	)

	// Register Trakt accounts API routes
	traktAccountsHandler := handlers.NewTraktAccountsHandler(cfgManager, traktClient, userService, accountsService)
	api.RegisterTraktRoutes(r, traktAccountsHandler, sessionsService)

	// Create Plex client and register Plex accounts handler
	plexClient := plex.NewClient(plex.GenerateClientID())
	plexAccountsHandler := handlers.NewPlexAccountsHandler(cfgManager, plexClient, userService, accountsService)

	// Create scheduler service for background tasks
	schedulerService := scheduler.NewService(cfgManager, plexClient, traktClient, watchlistService)
	schedulerService.SetEPGService(epgService)
	schedulerService.SetHistoryService(historyService)
	scheduledTasksHandler := handlers.NewScheduledTasksHandler(cfgManager, schedulerService)

	// Rate limiter for admin/account login (5/min per IP)
	adminLoginLimiter := api.NewIPRateLimiter(rate.Every(12*time.Second), 5)

	// Register admin UI routes
	adminUIHandler := handlers.NewAdminUIHandler(configPath, settings.Log.File, videoHandler.GetHLSManager(), userService, userSettingsService, cfgManager)
	adminUIHandler.SetMetadataService(metadataService)
	adminUIHandler.SetHistoryService(historyService)
	adminUIHandler.SetWatchlistService(watchlistService)
	adminUIHandler.SetAccountsService(accountsService)
	adminUIHandler.SetInvitationsService(invitationsService)
	adminUIHandler.SetSessionsService(sessionsService)
	adminUIHandler.SetClientsService(clientsService)
	adminUIHandler.SetClientSettingsService(clientSettingsService)
	adminUIHandler.SetCalendarService(calendarService)

	// Login/logout routes (no auth required)
	r.HandleFunc("/admin/login", adminUIHandler.LoginPage).Methods(http.MethodGet)
	r.HandleFunc("/admin/login", api.RateLimitHandlerFunc(adminLoginLimiter, adminUIHandler.LoginSubmit)).Methods(http.MethodPost)
	r.HandleFunc("/admin/logout", adminUIHandler.Logout).Methods(http.MethodGet, http.MethodPost)

	// Protected admin routes (require session authentication)
	r.HandleFunc("/admin", adminUIHandler.RequireAuth(adminUIHandler.StatusPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/", adminUIHandler.RequireAuth(adminUIHandler.StatusPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/settings", adminUIHandler.RequireAuth(adminUIHandler.SettingsPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/status", adminUIHandler.RequireAuth(adminUIHandler.StatusPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/history", adminUIHandler.RequireAuth(adminUIHandler.HistoryPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/tools", adminUIHandler.RequireAuth(adminUIHandler.ToolsPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/search", adminUIHandler.RequireAuth(adminUIHandler.SearchPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/accounts", adminUIHandler.RequireAuth(adminUIHandler.AccountsPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/kids-settings", adminUIHandler.RequireAuth(adminUIHandler.KidsSettingsPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/calendar", adminUIHandler.RequireAuth(adminUIHandler.CalendarPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/calendar", adminUIHandler.RequireAuth(adminUIHandler.GetCalendarData)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/schema", adminUIHandler.RequireAuth(adminUIHandler.GetSchema)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/status", adminUIHandler.RequireAuth(adminUIHandler.GetStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/streams", adminUIHandler.RequireAuth(adminUIHandler.GetStreams)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/streams/sse", adminUIHandler.RequireAuth(adminUIHandler.GetStreamsSSE)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/debrid-status", adminUIHandler.RequireAuth(adminUIHandler.GetDebridStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.GetUserSettings)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.SaveUserSettings)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.ResetUserSettings)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/settings/propagate", adminUIHandler.RequireAuth(adminUIHandler.PropagateSettings)).Methods(http.MethodPost)

	// Global settings endpoint (master only)
	r.HandleFunc("/admin/api/settings", adminUIHandler.RequireMasterAuth(settingsHandler.GetSettings)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/settings", adminUIHandler.RequireMasterAuth(settingsHandler.PutSettings)).Methods(http.MethodPut)

	// Search and metadata endpoints (for admin search page)
	r.HandleFunc("/admin/api/users", adminUIHandler.RequireAuth(usersHandler.List)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/search", adminUIHandler.RequireAuth(metadataHandler.Search)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/metadata/series/details", adminUIHandler.RequireAuth(metadataHandler.SeriesDetails)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/metadata/movie/details", adminUIHandler.RequireAuth(metadataHandler.MovieDetails)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/indexers/search", adminUIHandler.RequireAuth(indexerHandler.Search)).Methods(http.MethodGet)

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
	r.HandleFunc("/admin/api/profiles/kids", adminUIHandler.RequireAuth(adminUIHandler.SetKidsProfile)).Methods(http.MethodPut)
	// Content discovery endpoints (for admin kids-settings preview)
	r.HandleFunc("/admin/api/discover/new", adminUIHandler.RequireAuth(metadataHandler.DiscoverNew)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/lists/custom", adminUIHandler.RequireAuth(metadataHandler.CustomList)).Methods(http.MethodGet)
	// Kids profile settings endpoints (for admin kids-settings page)
	r.HandleFunc("/admin/api/users/{userID}/kids/mode", adminUIHandler.RequireAuth(usersHandler.SetKidsMode)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/users/{userID}/kids/rating", adminUIHandler.RequireAuth(usersHandler.SetKidsMaxRating)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/users/{userID}/kids/rating/movie", adminUIHandler.RequireAuth(usersHandler.SetKidsMaxMovieRating)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/users/{userID}/kids/rating/tv", adminUIHandler.RequireAuth(usersHandler.SetKidsMaxTVRating)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/users/{userID}/kids/lists", adminUIHandler.RequireAuth(usersHandler.SetKidsAllowedLists)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/users/{userID}/kids/lists", adminUIHandler.RequireAuth(usersHandler.AddKidsAllowedList)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/users/{userID}/kids/lists", adminUIHandler.RequireAuth(usersHandler.RemoveKidsAllowedList)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/profiles/icon", adminUIHandler.RequireAuth(adminUIHandler.SetProfileIcon)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/profiles/icon", adminUIHandler.RequireAuth(adminUIHandler.ClearProfileIcon)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/profiles/icon", adminUIHandler.RequireAuth(adminUIHandler.ServeProfileIcon)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/profiles/icon/upload", adminUIHandler.RequireAuth(adminUIHandler.UploadProfileIcon)).Methods(http.MethodPost)

	// Live TV endpoints for admin panel
	r.HandleFunc("/admin/api/live/categories", adminUIHandler.RequireAuth(liveHandler.GetCategories)).Methods(http.MethodGet)

	// User account management endpoints (master account only)
	r.HandleFunc("/admin/api/accounts", adminUIHandler.RequireAuth(adminUIHandler.GetUserAccounts)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/accounts", adminUIHandler.RequireAuth(adminUIHandler.CreateUserAccount)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/accounts", adminUIHandler.RequireAuth(adminUIHandler.RenameUserAccount)).Methods(http.MethodPatch)
	r.HandleFunc("/admin/api/accounts", adminUIHandler.RequireAuth(adminUIHandler.DeleteUserAccount)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/accounts/password", adminUIHandler.RequireAuth(adminUIHandler.ResetUserAccountPassword)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/accounts/default-password", adminUIHandler.RequireAuth(adminUIHandler.HasDefaultPassword)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/profiles/reassign", adminUIHandler.RequireAuth(adminUIHandler.ReassignProfile)).Methods(http.MethodPut)

	// Invitation link management endpoints (master account only)
	r.HandleFunc("/admin/api/invitations", adminUIHandler.RequireMasterAuth(adminUIHandler.ListInvitations)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/invitations", adminUIHandler.RequireMasterAuth(adminUIHandler.CreateInvitation)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/invitations", adminUIHandler.RequireMasterAuth(adminUIHandler.DeleteInvitation)).Methods(http.MethodDelete)

	// Public registration endpoints (no auth required)
	r.HandleFunc("/register", adminUIHandler.RegisterPage).Methods(http.MethodGet)
	r.HandleFunc("/api/register/validate", adminUIHandler.ValidateInvitation).Methods(http.MethodGet)
	r.HandleFunc("/api/register", adminUIHandler.RegisterWithInvitation).Methods(http.MethodPost)

	// Cache management endpoints
	r.HandleFunc("/admin/api/cache/clear", adminUIHandler.RequireAuth(adminUIHandler.ClearMetadataCache)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/cache/manager/status", adminUIHandler.RequireAuth(adminUIHandler.GetCacheManagerStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/cache/manager/refresh", adminUIHandler.RequireAuth(adminUIHandler.RefreshTrendingCache)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/calendar/worker/status", adminUIHandler.RequireAuth(adminUIHandler.GetCalendarWorkerStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/calendar/worker/refresh", adminUIHandler.RequireAuth(adminUIHandler.RefreshCalendar)).Methods(http.MethodPost)

	// History endpoints (admin session auth, no PIN required)
	r.HandleFunc("/admin/api/history/watched", adminUIHandler.RequireAuth(adminUIHandler.GetWatchHistory)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/history/continue", adminUIHandler.RequireAuth(adminUIHandler.GetContinueWatching)).Methods(http.MethodGet)

	// Plex integration endpoints
	r.HandleFunc("/admin/api/plex/status", adminUIHandler.RequireAuth(adminUIHandler.PlexGetStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/pin", adminUIHandler.RequireAuth(adminUIHandler.PlexCreatePIN)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/plex/pin/{id}", adminUIHandler.RequireAuth(adminUIHandler.PlexCheckPIN)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/disconnect", adminUIHandler.RequireAuth(adminUIHandler.PlexDisconnect)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/plex/watchlist", adminUIHandler.RequireAuth(adminUIHandler.PlexGetWatchlist)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/import", adminUIHandler.RequireAuth(adminUIHandler.PlexImportWatchlist)).Methods(http.MethodPost)

	// Trakt integration endpoints
	r.HandleFunc("/admin/api/trakt/status", adminUIHandler.RequireAuth(adminUIHandler.TraktGetStatus)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/credentials", adminUIHandler.RequireAuth(adminUIHandler.TraktSaveCredentials)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/auth/start", adminUIHandler.RequireAuth(adminUIHandler.TraktStartAuth)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/auth/check/{deviceCode}", adminUIHandler.RequireAuth(adminUIHandler.TraktCheckAuth)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/disconnect", adminUIHandler.RequireAuth(adminUIHandler.TraktDisconnect)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/scrobbling", adminUIHandler.RequireAuth(adminUIHandler.TraktSetScrobbling)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/watchlist", adminUIHandler.RequireAuth(adminUIHandler.TraktGetWatchlist)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/history", adminUIHandler.RequireAuth(adminUIHandler.TraktGetHistory)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/import/watchlist", adminUIHandler.RequireAuth(adminUIHandler.TraktImportWatchlist)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/import/history", adminUIHandler.RequireAuth(adminUIHandler.TraktImportHistory)).Methods(http.MethodPost)

	// Trakt multi-account management (admin routes)
	r.HandleFunc("/admin/api/trakt/accounts", adminUIHandler.RequireAuth(traktAccountsHandler.ListAccounts)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/accounts", adminUIHandler.RequireAuth(traktAccountsHandler.CreateAccount)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}", adminUIHandler.RequireAuth(traktAccountsHandler.GetAccount)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}", adminUIHandler.RequireAuth(traktAccountsHandler.UpdateAccount)).Methods(http.MethodPatch)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}", adminUIHandler.RequireAuth(traktAccountsHandler.DeleteAccount)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}/auth/start", adminUIHandler.RequireAuth(traktAccountsHandler.StartAuth)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}/auth/check/{deviceCode}", adminUIHandler.RequireAuth(traktAccountsHandler.CheckAuth)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}/disconnect", adminUIHandler.RequireAuth(traktAccountsHandler.Disconnect)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}/scrobbling", adminUIHandler.RequireAuth(traktAccountsHandler.SetScrobbling)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}/history", adminUIHandler.RequireAuth(traktAccountsHandler.GetHistory)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}/watchlist", adminUIHandler.RequireAuth(traktAccountsHandler.GetWatchlist)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/trakt/accounts/{accountID}/lists", adminUIHandler.RequireAuth(traktAccountsHandler.GetLists)).Methods(http.MethodGet)

	// Profile Trakt linking (admin routes)
	r.HandleFunc("/admin/api/users/{userID}/trakt", adminUIHandler.RequireAuth(usersHandler.SetTraktAccount)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/users/{userID}/trakt", adminUIHandler.RequireAuth(usersHandler.ClearTraktAccount)).Methods(http.MethodDelete)

	// Plex multi-account management (admin routes)
	r.HandleFunc("/admin/api/plex/accounts", adminUIHandler.RequireAuth(plexAccountsHandler.ListAccounts)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/accounts", adminUIHandler.RequireAuth(plexAccountsHandler.CreateAccount)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}", adminUIHandler.RequireAuth(plexAccountsHandler.DeleteAccount)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}/pin", adminUIHandler.RequireAuth(plexAccountsHandler.CreatePIN)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}/pin/{pinID}", adminUIHandler.RequireAuth(plexAccountsHandler.CheckPIN)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}/disconnect", adminUIHandler.RequireAuth(plexAccountsHandler.Disconnect)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}/history", adminUIHandler.RequireAuth(plexAccountsHandler.GetHistory)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}/servers", adminUIHandler.RequireAuth(plexAccountsHandler.GetServers)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}/users", adminUIHandler.RequireAuth(plexAccountsHandler.GetHomeUsers)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/accounts/{accountID}/watchlist", adminUIHandler.RequireAuth(plexAccountsHandler.GetWatchlist)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/plex/import/history", adminUIHandler.RequireAuth(adminUIHandler.PlexImportHistory)).Methods(http.MethodPost)

	// Profile Plex linking (admin routes)
	r.HandleFunc("/admin/api/users/{userID}/plex", adminUIHandler.RequireAuth(usersHandler.SetPlexAccount)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/users/{userID}/plex", adminUIHandler.RequireAuth(usersHandler.ClearPlexAccount)).Methods(http.MethodDelete)

	// Client device management (admin routes)
	r.HandleFunc("/admin/api/clients", adminUIHandler.RequireAuth(clientsHandler.List)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/clients/{clientID}", adminUIHandler.RequireAuth(clientsHandler.Get)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/clients/{clientID}", adminUIHandler.RequireAuth(clientsHandler.Update)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/clients/{clientID}", adminUIHandler.RequireAuth(clientsHandler.Delete)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/clients/{clientID}/settings", adminUIHandler.RequireAuth(clientsHandler.GetSettings)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/clients/{clientID}/settings", adminUIHandler.RequireAuth(clientsHandler.UpdateSettings)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/clients/{clientID}/settings", adminUIHandler.RequireAuth(clientsHandler.ResetSettings)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/clients/{clientID}/ping", adminUIHandler.RequireAuth(clientsHandler.Ping)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/clients/{clientID}/reassign", adminUIHandler.RequireAuth(clientsHandler.Reassign)).Methods(http.MethodPost)

	// Scheduled tasks routes (master account only)
	r.HandleFunc("/admin/api/scheduled-tasks", adminUIHandler.RequireMasterAuth(scheduledTasksHandler.ListTasks)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/scheduled-tasks", adminUIHandler.RequireMasterAuth(scheduledTasksHandler.CreateTask)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/scheduled-tasks/{taskID}", adminUIHandler.RequireMasterAuth(scheduledTasksHandler.UpdateTask)).Methods(http.MethodPut)
	r.HandleFunc("/admin/api/scheduled-tasks/{taskID}", adminUIHandler.RequireMasterAuth(scheduledTasksHandler.DeleteTask)).Methods(http.MethodDelete)
	r.HandleFunc("/admin/api/scheduled-tasks/{taskID}/run", adminUIHandler.RequireMasterAuth(scheduledTasksHandler.RunTaskNow)).Methods(http.MethodPost)
	r.HandleFunc("/admin/api/scheduled-tasks/{taskID}/toggle", adminUIHandler.RequireMasterAuth(scheduledTasksHandler.ToggleTask)).Methods(http.MethodPost)

	// Backup routes (master account only)
	backupService, err := backup.NewService(settings.Cache.Directory, cfgManager)
	if err != nil {
		log.Printf("warning: failed to initialize backup service: %v", err)
	} else {
		backupHandler := handlers.NewBackupHandler(backupService)
		schedulerService.SetBackupService(backupService)
		r.HandleFunc("/admin/backup", adminUIHandler.RequireMasterAuth(adminUIHandler.BackupPage)).Methods(http.MethodGet)
		r.HandleFunc("/admin/api/backups", adminUIHandler.RequireMasterAuth(backupHandler.ListBackups)).Methods(http.MethodGet)
		r.HandleFunc("/admin/api/backups", adminUIHandler.RequireMasterAuth(backupHandler.CreateBackup)).Methods(http.MethodPost)
		r.HandleFunc("/admin/api/backups/{filename}/download", adminUIHandler.RequireMasterAuth(backupHandler.DownloadBackup)).Methods(http.MethodGet)
		r.HandleFunc("/admin/api/backups/{filename}/restore", adminUIHandler.RequireMasterAuth(backupHandler.RestoreBackup)).Methods(http.MethodPost)
		r.HandleFunc("/admin/api/backups/{filename}", adminUIHandler.RequireMasterAuth(backupHandler.DeleteBackup)).Methods(http.MethodDelete)
		fmt.Println("💾 Backup management available at /admin/backup")
	}

	// Performance monitoring (admin-only)
	r.HandleFunc("/admin/performance", adminUIHandler.RequireMasterAuth(adminUIHandler.PerformancePage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/logs", adminUIHandler.RequireAuth(adminUIHandler.LogsPage)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/logs", adminUIHandler.RequireAuth(adminUIHandler.GetLogs)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/performance", adminUIHandler.RequireMasterAuth(adminUIHandler.GetPerformanceMetrics)).Methods(http.MethodGet)
	r.HandleFunc("/admin/api/performance/sse", adminUIHandler.RequireMasterAuth(adminUIHandler.GetPerformanceSSE)).Methods(http.MethodGet)

	fmt.Println("📊 Admin dashboard available at /admin")

	// Register account UI routes (for regular/non-master accounts)
	accountUIHandler := handlers.NewAccountUIHandler(accountsService, sessionsService, userService, userSettingsService, videoHandler.GetHLSManager(), cfgManager, traktClient)

	// Account login/logout routes (no auth required)
	r.HandleFunc("/account/login", accountUIHandler.LoginPage).Methods(http.MethodGet)
	r.HandleFunc("/account/login", api.RateLimitHandlerFunc(adminLoginLimiter, accountUIHandler.LoginSubmit)).Methods(http.MethodPost)
	r.HandleFunc("/account/logout", accountUIHandler.Logout).Methods(http.MethodGet, http.MethodPost)

	// Protected account routes - Pages (use adminUIHandler with unified templates)
	r.HandleFunc("/account", adminUIHandler.RequireAuth(adminUIHandler.StatusPage)).Methods(http.MethodGet)
	r.HandleFunc("/account/status", adminUIHandler.RequireAuth(adminUIHandler.StatusPage)).Methods(http.MethodGet)
	r.HandleFunc("/account/settings", adminUIHandler.RequireAuth(adminUIHandler.SettingsPage)).Methods(http.MethodGet)
	r.HandleFunc("/account/history", adminUIHandler.RequireAuth(adminUIHandler.HistoryPage)).Methods(http.MethodGet)
	r.HandleFunc("/account/tools", adminUIHandler.RequireAuth(adminUIHandler.ToolsPage)).Methods(http.MethodGet)
	r.HandleFunc("/account/accounts", adminUIHandler.RequireAuth(adminUIHandler.AccountsPage)).Methods(http.MethodGet) // Shows as "Profiles" for non-admin
	r.HandleFunc("/account/kids-settings", adminUIHandler.RequireAuth(adminUIHandler.KidsSettingsPage)).Methods(http.MethodGet)
	r.HandleFunc("/account/calendar", adminUIHandler.RequireAuth(adminUIHandler.CalendarPage)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/calendar", adminUIHandler.RequireAuth(adminUIHandler.GetCalendarData)).Methods(http.MethodGet)

	// Protected account routes - Status APIs
	r.HandleFunc("/account/api/status", adminUIHandler.RequireAuth(adminUIHandler.GetStatus)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/streams", adminUIHandler.RequireAuth(adminUIHandler.GetStreams)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/streams/sse", adminUIHandler.RequireAuth(adminUIHandler.GetStreamsSSE)).Methods(http.MethodGet)

	// Protected account routes - Profile APIs
	r.HandleFunc("/account/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.GetProfiles)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.CreateProfile)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.RenameProfile)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/profiles", adminUIHandler.RequireAuth(adminUIHandler.DeleteProfile)).Methods(http.MethodDelete)
	r.HandleFunc("/account/api/profiles/color", adminUIHandler.RequireAuth(adminUIHandler.SetProfileColor)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/profiles/icon", adminUIHandler.RequireAuth(adminUIHandler.SetProfileIcon)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/profiles/icon", adminUIHandler.RequireAuth(adminUIHandler.ClearProfileIcon)).Methods(http.MethodDelete)
	r.HandleFunc("/account/api/profiles/icon", adminUIHandler.RequireAuth(adminUIHandler.ServeProfileIcon)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/profiles/icon/upload", adminUIHandler.RequireAuth(adminUIHandler.UploadProfileIcon)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/profiles/pin", adminUIHandler.RequireAuth(adminUIHandler.SetProfilePin)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/profiles/pin", adminUIHandler.RequireAuth(adminUIHandler.ClearProfilePin)).Methods(http.MethodDelete)
	r.HandleFunc("/account/api/profiles/kids", adminUIHandler.RequireAuth(adminUIHandler.SetKidsProfile)).Methods(http.MethodPut)
	// Content discovery endpoints (for account kids-settings preview)
	r.HandleFunc("/account/api/discover/new", adminUIHandler.RequireAuth(metadataHandler.DiscoverNew)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/lists/custom", adminUIHandler.RequireAuth(metadataHandler.CustomList)).Methods(http.MethodGet)
	// Kids profile settings endpoints (for account kids-settings page)
	r.HandleFunc("/account/api/users/{userID}/kids/mode", adminUIHandler.RequireAuth(usersHandler.SetKidsMode)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/users/{userID}/kids/rating", adminUIHandler.RequireAuth(usersHandler.SetKidsMaxRating)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/users/{userID}/kids/rating/movie", adminUIHandler.RequireAuth(usersHandler.SetKidsMaxMovieRating)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/users/{userID}/kids/rating/tv", adminUIHandler.RequireAuth(usersHandler.SetKidsMaxTVRating)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/users/{userID}/kids/lists", adminUIHandler.RequireAuth(usersHandler.SetKidsAllowedLists)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/users/{userID}/kids/lists", adminUIHandler.RequireAuth(usersHandler.AddKidsAllowedList)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/users/{userID}/kids/lists", adminUIHandler.RequireAuth(usersHandler.RemoveKidsAllowedList)).Methods(http.MethodDelete)
	r.HandleFunc("/account/api/password", accountUIHandler.RequireAuth(accountUIHandler.ChangePassword)).Methods(http.MethodPut)

	// Protected account routes - User Settings API
	r.HandleFunc("/account/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.GetUserSettings)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.SaveUserSettings)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/user-settings", adminUIHandler.RequireAuth(adminUIHandler.ResetUserSettings)).Methods(http.MethodDelete)

	// Protected account routes - Client device management (same handlers as admin)
	r.HandleFunc("/account/api/clients", adminUIHandler.RequireAuth(clientsHandler.List)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/clients/{clientID}", adminUIHandler.RequireAuth(clientsHandler.Get)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/clients/{clientID}", adminUIHandler.RequireAuth(clientsHandler.Update)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/clients/{clientID}/settings", adminUIHandler.RequireAuth(clientsHandler.GetSettings)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/clients/{clientID}/settings", adminUIHandler.RequireAuth(clientsHandler.UpdateSettings)).Methods(http.MethodPut)
	r.HandleFunc("/account/api/clients/{clientID}/settings", adminUIHandler.RequireAuth(clientsHandler.ResetSettings)).Methods(http.MethodDelete)
	r.HandleFunc("/account/api/clients/{clientID}/ping", adminUIHandler.RequireAuth(clientsHandler.Ping)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/clients/{clientID}/reassign", adminUIHandler.RequireAuth(clientsHandler.Reassign)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/clients/{clientID}", adminUIHandler.RequireAuth(clientsHandler.Delete)).Methods(http.MethodDelete)

	// Protected account routes - History API
	r.HandleFunc("/account/api/history/watched", adminUIHandler.RequireAuth(adminUIHandler.GetWatchHistory)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/history/continue", adminUIHandler.RequireAuth(adminUIHandler.GetContinueWatching)).Methods(http.MethodGet)

	// Protected account routes - Trakt API (using account-scoped handler)
	r.HandleFunc("/account/api/trakt/accounts", accountUIHandler.RequireAuth(accountUIHandler.GetTraktAccounts)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/trakt/accounts", accountUIHandler.RequireAuth(accountUIHandler.CreateTraktAccount)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/trakt/accounts/delete", accountUIHandler.RequireAuth(accountUIHandler.DeleteTraktAccount)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/trakt/accounts/auth/start", accountUIHandler.RequireAuth(accountUIHandler.StartTraktAuth)).Methods(http.MethodPost)
	r.HandleFunc("/account/api/trakt/accounts/auth/check", accountUIHandler.RequireAuth(accountUIHandler.CheckTraktAuth)).Methods(http.MethodGet)
	r.HandleFunc("/account/api/trakt/accounts/disconnect", accountUIHandler.RequireAuth(accountUIHandler.DisconnectTraktAccount)).Methods(http.MethodPost)

	fmt.Println("👤 Account management available at /account")

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

	// Log warning if master account has default password
	if accountsService.HasDefaultPassword() {
		fmt.Println("")
		fmt.Println("╔══════════════════════════════════════════════════════════════════════╗")
		fmt.Println("║                      ⚠️  SECURITY WARNING ⚠️                          ║")
		fmt.Println("╠══════════════════════════════════════════════════════════════════════╣")
		fmt.Println("║                                                                      ║")
		fmt.Println("║   The master account 'admin' still has the DEFAULT PASSWORD.        ║")
		fmt.Println("║                                                                      ║")
		fmt.Println("║   Please change it immediately at:                                  ║")
		fmt.Println("║     → Admin UI → Accounts → Change Password                         ║")
		fmt.Println("║                                                                      ║")
		fmt.Println("║   Default credentials:  admin / admin                               ║")
		fmt.Println("║                                                                      ║")
		fmt.Println("╚══════════════════════════════════════════════════════════════════════╝")
		fmt.Println("")
	}

	// Create HTTP server with timeouts
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // No write timeout for streaming
		IdleTimeout:       120 * time.Second,
	}

	// Setup graceful shutdown
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	// Start scheduler service for background tasks
	if err := schedulerService.Start(context.Background()); err != nil {
		log.Printf("Warning: failed to start scheduler service: %v", err)
	}

	// Start background cache manager to warm trending data and custom lists
	// on startup and refresh periodically (every 2 hours)
	metadataService.SetCustomListInfoProvider(func() []metadata.CustomListInfo {
		seen := make(map[string]bool)
		var infos []metadata.CustomListInfo

		addList := func(u, name string) {
			u = strings.TrimSpace(u)
			if u != "" && !seen[u] {
				seen[u] = true
				infos = append(infos, metadata.CustomListInfo{URL: u, Name: name})
			}
		}

		// Global shelves
		if globalCfg, err := cfgManager.Load(); err == nil {
			for _, shelf := range globalCfg.HomeShelves.Shelves {
				if shelf.Type == "mdblist" && shelf.Enabled {
					addList(shelf.ListURL, shelf.Name)
				}
			}
		}

		// Per-user shelves
		for userID := range userSettingsService.GetUsersWithOverrides() {
			if us, err := userSettingsService.Get(userID); err == nil && us != nil {
				for _, shelf := range us.HomeShelves.Shelves {
					if shelf.Type == "mdblist" && shelf.Enabled {
						addList(shelf.ListURL, shelf.Name)
					}
				}
			}
		}

		return infos
	})
	metadataService.StartBackgroundCacheManager(2 * time.Hour)
	calendarService.StartBackgroundRefresh(4 * time.Hour)

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

	// Stop background cache manager
	metadataService.StopBackgroundCacheManager()

	// Stop scheduler service
	log.Println("🧹 Stopping scheduler service...")
	if err := schedulerService.Stop(shutdownCtx); err != nil {
		log.Printf("Scheduler shutdown error: %v", err)
	}

	// Stop calendar service background refresh
	calendarService.Stop()

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
