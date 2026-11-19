package jellyfinbridge

import (
	"net/http"

	"github.com/gorilla/mux"
)

const (
	get  = http.MethodGet
	head = http.MethodHead
	post = http.MethodPost
	del  = http.MethodDelete
)

// routes registers the Jellyfin API surface. Paths are lowercase (see
// lowercasePath). Most endpoints exist in a modern form and a legacy
// /Users/{uid}/... form; both are registered because clients use both.
func (s *Server) routes(r *mux.Router) {
	auth := s.requireAuth
	h := func(path string, fn http.HandlerFunc, methods ...string) {
		r.HandleFunc(path, fn).Methods(methods...)
	}
	r.Use(s.versionAliases)

	// System, branding, discovery (anonymous).
	h("/system/info/public", s.handlePublicSystemInfo, get)
	// Crash reports: apps send them right after a crash, possibly before the
	// session is back, so no token is required; storage is capped.
	h("/clientlog/document", s.handleClientLog, post)
	h("/system/info", auth(s.handleSystemInfo), get)
	h("/system/ping", s.handlePing, get, post)
	h("/system/endpoint", auth(s.handleEndpointInfo), get)
	h("/branding/configuration", s.handleBrandingConfig, get)
	h("/branding/css", s.handleBrandingCSS, get)
	h("/branding/css.css", s.handleBrandingCSS, get)
	h("/quickconnect/enabled", s.handleQuickConnectEnabled, get)
	h("/quickconnect/initiate", s.handleQuickConnectInitiate, post)
	h("/quickconnect/connect", s.handleQuickConnectConnect, get)
	h("/quickconnect/authorize", auth(s.handleQuickConnectAuthorize), post)
	h("/playback/bitratetest", auth(s.handleBitrateTest), get)
	h("/socket", s.handleSocket, get)
	h("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/system/info/public", http.StatusFound)
	}, get)

	// Users and sessions.
	h("/users/public", s.handlePublicUsers, get)
	h("/users/authenticatebyname", s.handleAuthenticateByName, post)
	h("/users/authenticatewithquickconnect", s.handleAuthenticateWithQuickConnect, post)
	h("/users/me", auth(s.handleMe), get)
	h("/users", auth(s.handleUsers), get)
	h("/users/{uid}", auth(s.handleUserByID), get)
	h("/users/{uid}/configuration", auth(noContent), post)
	h("/users/configuration", auth(noContent), post)
	h("/sessions", auth(s.handleSessions), get)
	h("/sessions/logout", auth(s.handleLogout), post)
	h("/sessions/capabilities", auth(noContent), post)
	h("/sessions/capabilities/full", auth(noContent), post)
	h("/sessions/playing", auth(s.handlePlaybackReport("start")), post)
	h("/sessions/playing/progress", auth(s.handlePlaybackReport("progress")), post)
	h("/sessions/playing/stopped", auth(s.handlePlaybackReport("stopped")), post)
	h("/sessions/playing/ping", auth(s.handlePlaybackPing), post)
	h("/displaypreferences/{id}", auth(s.handleGetDisplayPrefs), get)
	h("/displaypreferences/{id}", auth(s.handleSetDisplayPrefs), post)

	// Views and items (modern and legacy user-scoped forms).
	// Fixed paths must precede /items/{id}: gorilla/mux matches in order.
	h("/items/filters", auth(s.handleItemFilters), get)
	h("/items/filters2", auth(s.handleItemFilters), get)
	h("/items/counts", auth(emptyObject), get)
	h("/items/suggestions", auth(emptyQueryResult), get)
	for _, prefix := range []string{"", "/users/{uid}"} {
		h(prefix+"/items/root", auth(s.handleRootFolder), get)
		h(prefix+"/items/latest", auth(s.handleLatest), get)
		h(prefix+"/items/resume", auth(s.handleResume), get)
		h(prefix+"/items", auth(s.handleItems), get)
		h(prefix+"/items/{id}", auth(s.handleItem), get)
		h(prefix+"/items/{id}", auth(s.handleDeleteItem), del)
		h(prefix+"/items/{id}/intros", auth(emptyQueryResult), get)
		h(prefix+"/items/{id}/localtrailers", auth(emptyArray), get)
		h(prefix+"/items/{id}/specialfeatures", auth(emptyArray), get)
		h(prefix+"/favoriteitems/{id}", auth(s.handleFavorite(true)), post)
		h(prefix+"/favoriteitems/{id}", auth(s.handleFavorite(false)), del)
		h(prefix+"/playeditems/{id}", auth(s.handlePlayed(true)), post)
		h(prefix+"/playeditems/{id}", auth(s.handlePlayed(false)), del)
	}
	h("/userviews", auth(s.handleUserViews), get)
	h("/users/{uid}/views", auth(s.handleUserViews), get)
	h("/userviews/groupingoptions", auth(emptyArray), get)
	h("/users/{uid}/groupingoptions", auth(emptyArray), get)
	h("/library/virtualfolders", auth(s.handleVirtualFolders), get)
	h("/library/mediafolders", auth(s.handleMediaFolders), get)
	h("/library/physicalpaths", auth(emptyArray), get)
	h("/useritems/resume", auth(s.handleResume), get)
	h("/useritems/{id}/userdata", auth(s.handleUserData), get)
	h("/userfavoriteitems/{id}", auth(s.handleFavorite(true)), post)
	h("/userfavoriteitems/{id}", auth(s.handleFavorite(false)), del)
	h("/userplayeditems/{id}", auth(s.handlePlayed(true)), post)
	h("/userplayeditems/{id}", auth(s.handlePlayed(false)), del)
	h("/items/{id}/similar", auth(s.handleSimilar), get)
	h("/items/{id}/thememedia", auth(s.handleThemeMedia), get)
	h("/items/{id}/themesongs", auth(emptyQueryResult), get)
	h("/items/{id}/themevideos", auth(emptyQueryResult), get)
	h("/items/{id}/ancestors", auth(emptyArray), get)
	h("/items/{id}/criticreviews", auth(emptyQueryResult), get)
	h("/items/{id}/externalidinfos", auth(emptyArray), get)
	h("/items/{id}/images", auth(emptyArray), get)
	h("/items/{id}/images/{type}", s.handleImage, get, head)
	h("/items/{id}/images/{type}/{index}", s.handleImage, get, head)
	h("/movies/recommendations", auth(emptyArray), get)
	h("/shows/nextup", auth(s.handleNextUp), get)
	h("/shows/upcoming", auth(s.handleUpcoming), get)
	h("/shows/{id}/seasons", auth(s.handleSeasons), get)
	h("/shows/{id}/episodes", auth(s.handleEpisodes), get)
	h("/genres", auth(s.handleGenres), get)
	h("/musicgenres", auth(emptyQueryResult), get)
	h("/persons", auth(emptyQueryResult), get)
	h("/studios", auth(s.handleStudios), get)
	h("/artists", auth(emptyQueryResult), get)
	h("/artists/albumartists", auth(emptyQueryResult), get)
	h("/channels", auth(emptyQueryResult), get)
	h("/playlists", auth(emptyQueryResult), get)
	h("/playlists", auth(s.handleCreateList), post)
	h("/playlists/{id}", auth(s.handlePlaylist), get)
	h("/playlists/{id}/users", auth(s.handlePlaylistUsers), get)
	h("/playlists/{id}/users/{uid}", auth(s.handlePlaylistUser), get)
	h("/playlists/{id}/items", auth(s.handlePlaylistItems), get)
	h("/playlists/{id}/items", auth(s.handleListAdd), post)
	h("/playlists/{id}/items", auth(s.handleListRemove), del)
	h("/collections", auth(s.handleCreateList), post)
	h("/collections/{id}/items", auth(s.handleListAdd), post)
	h("/collections/{id}/items", auth(s.handleListRemove), del)
	h("/mediasegments/{id}", auth(s.handleMediaSegments), get)
	h("/videos/{id}/trickplay/{width}/{index:[0-9]+}.jpg", s.handleTrickplayTile, get)
	h("/videos/{id}/trickplay/{width}/tiles.m3u8", auth(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "No trickplay")
	}), get)
	h("/plugins", auth(emptyArray), get)
	h("/syncplay/list", auth(emptyArray), get)
	h("/items/{id}/collections", auth(emptyQueryResult), get)
	h("/web/configurationpages", auth(emptyArray), get)
	h("/system/configuration", auth(emptyObject), get)
	h("/localization/options", auth(emptyArray), get)
	h("/localization/countries", auth(emptyArray), get)
	h("/localization/cultures", auth(emptyArray), get)

	// Playback.
	h("/items/{id}/playbackinfo", auth(s.handlePlaybackInfo), get, post)
	h("/videos/{id}/master.m3u8", auth(s.handleMasterPlaylist), get, head)
	h("/videos/{id}/main.m3u8", auth(s.handleMasterPlaylist), get, head)
	h("/videos/{id}/hls/{sid}/{file}", auth(s.handleHLSFile), get, head)
	h("/videos/{id}/stream", s.handleStream, get, head)
	h("/videos/{id}/stream.{ext}", s.handleStream, get, head)
	h("/videos/{id}/{msid}/subtitles/{index}/stream.{fmt}", s.handleSubtitle, get)
	h("/videos/{id}/{msid}/subtitles/{index}/{ticks}/stream.{fmt}", s.handleSubtitle, get)
	h("/videos/activeencodings", auth(s.handleStopEncodings), del)
	h("/livestreams/close", auth(noContent), post)

	// Live TV.
	h("/livetv/info", auth(s.handleLiveTvInfo), get)
	h("/livetv/guideinfo", auth(s.handleGuideInfo), get)
	h("/livetv/channels", auth(s.handleChannels), get)
	h("/livetv/channels/{id}", auth(s.handleChannel), get)
	h("/livetv/programs", auth(s.handlePrograms), get, post)
	h("/livetv/programs/recommended", auth(s.handleRecommendedPrograms), get)
	h("/livetv/programs/{id}", auth(s.handleItem), get)
	h("/livetv/recordings", auth(emptyQueryResult), get)
	h("/livetv/recordings/folders", auth(emptyQueryResult), get)
	h("/livetv/recordings/series", auth(emptyQueryResult), get)
	h("/livetv/recordings/groups", auth(emptyQueryResult), get)
	h("/livetv/timers", auth(emptyQueryResult), get)
	h("/livetv/seriestimers", auth(emptyQueryResult), get)
}
