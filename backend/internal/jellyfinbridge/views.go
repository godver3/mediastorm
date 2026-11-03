package jellyfinbridge

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/services/kids"
)

// Virtual libraries. mediastorm has no persistent library for on-demand
// content, so each profile gets views built from what it already curates.
const (
	viewMovies         = "movies"
	viewShows          = "shows"
	viewTrendingMovies = "trending-movies"
	viewTrendingShows  = "trending-shows"
	viewLists          = "lists"
	viewLiveTV         = "livetv"
)

type viewDef struct {
	Key            string
	Name           string
	CollectionType string
	Type           string
}

func (s *Server) viewDefs(profile models.User) []viewDef {
	defs := []viewDef{
		{viewMovies, "My Movies", dto.CollectionMovies, dto.KindCollectionFolder},
		{viewShows, "My Shows", dto.CollectionTVShows, dto.KindCollectionFolder},
	}
	// Curated kids profiles only see what was curated for them.
	if !isCuratedKidsProfile(profile) {
		defs = append(defs,
			viewDef{viewTrendingMovies, "Trending Movies", dto.CollectionMovies, dto.KindCollectionFolder},
			viewDef{viewTrendingShows, "Trending Shows", dto.CollectionTVShows, dto.KindCollectionFolder},
		)
	}
	if s.deps.CustomLists != nil {
		defs = append(defs, viewDef{viewLists, "Lists", dto.CollectionBoxSets, dto.KindCollectionFolder})
	}
	if s.deps.Handlers.LiveChannels != nil && !profile.IsKidsProfile {
		defs = append(defs, viewDef{viewLiveTV, "Live TV", dto.CollectionLiveTV, dto.KindUserView})
	}
	return defs
}

func (s *Server) viewItem(profile models.User, def viewDef) dto.BaseItemDto {
	id := s.ids.put(refView, profile.ID+":"+def.Key, refContext{Name: def.Name, ProfileID: profile.ID, ViewKey: def.Key})
	return dto.BaseItemDto{
		Name:                    def.Name,
		ServerID:                s.serverID,
		ID:                      id,
		Etag:                    imageTag(def.Key),
		Type:                    def.Type,
		CollectionType:          def.CollectionType,
		IsFolder:                boolPtr(true),
		MediaType:               dto.MediaTypeUnknown,
		LocationType:            dto.LocationFileSystem,
		PlayAccess:              "Full",
		DisplayPreferencesID:    id,
		SortName:                strings.ToLower(def.Name),
		UserData:                &dto.UserItemData{Key: id, ItemID: id},
		PrimaryImageAspectRatio: floatPtr(16.0 / 9.0),
	}
}

// visibleViewDefs lists the profile's views, local libraries included.
func (s *Server) visibleViewDefs(r *http.Request, p *principal) []viewDef {
	defs := s.viewDefs(p.Profile)
	defs = append(defs, localViewDefs(s.localLibraries(r.Context(), p))...)
	out := defs[:0]
	for _, def := range defs {
		// Only show Live TV when the profile actually has a channel source.
		if def.Key == viewLiveTV && len(s.liveChannels(r.Context(), p)) == 0 {
			continue
		}
		out = append(out, def)
	}
	return out
}

func (s *Server) handleUserViews(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	defs := s.visibleViewDefs(r, p)
	items := make([]dto.BaseItemDto, 0, len(defs))
	for _, def := range defs {
		items = append(items, s.viewItem(p.Profile, def))
	}
	writeJSON(w, http.StatusOK, dto.NewQueryResult(items, len(items), 0))
}

// libraryViewDefs are the views Jellyfin would list as libraries: Live TV
// is not one.
func (s *Server) libraryViewDefs(r *http.Request, p *principal) []viewDef {
	var out []viewDef
	for _, def := range s.visibleViewDefs(r, p) {
		if def.CollectionType != dto.CollectionLiveTV {
			out = append(out, def)
		}
	}
	return out
}

// handleVirtualFolders serves /Library/VirtualFolders. Jellyfin reserves it
// for administrators, but Infuse requests it while adding a server and gives
// up on a 404, so every user gets their own views as libraries.
func (s *Server) handleVirtualFolders(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	defs := s.libraryViewDefs(r, p)
	folders := make([]dto.VirtualFolderInfo, 0, len(defs))
	for _, def := range defs {
		item := s.viewItem(p.Profile, def)
		folders = append(folders, dto.VirtualFolderInfo{
			Name:               def.Name,
			Locations:          []string{"/mediastorm/" + def.Key},
			CollectionType:     def.CollectionType,
			LibraryOptions:     map[string]any{},
			ItemID:             item.ID,
			PrimaryImageItemID: item.ID,
			RefreshStatus:      "Idle",
		})
	}
	writeJSON(w, http.StatusOK, folders)
}

// handleMediaFolders serves /Library/MediaFolders (libraries as items).
func (s *Server) handleMediaFolders(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	defs := s.libraryViewDefs(r, p)
	items := make([]dto.BaseItemDto, 0, len(defs))
	for _, def := range defs {
		items = append(items, s.viewItem(p.Profile, def))
	}
	writeJSON(w, http.StatusOK, dto.NewQueryResult(items, len(items), 0))
}

// viewCache memoizes the (expensive) title list behind each view briefly so
// paging through a grid does not refetch trending data or watchlists.
type viewCache struct {
	mu      sync.Mutex
	entries map[string]viewCacheEntry
}

type viewCacheEntry struct {
	at     time.Time
	titles []titleInfo
}

const viewCacheTTL = 60 * time.Second

func newViewCache() *viewCache { return &viewCache{entries: map[string]viewCacheEntry{}} }

func (c *viewCache) get(key string) ([]titleInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > viewCacheTTL {
		return nil, false
	}
	return e.titles, true
}

func (c *viewCache) put(key string, titles []titleInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = viewCacheEntry{at: time.Now(), titles: titles}
}

// invalidate drops cached views for a profile (after favorites change).
func (c *viewCache) invalidate(profileID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if strings.HasPrefix(key, profileID+":") {
			delete(c.entries, key)
		}
	}
}

// viewTitles returns the titles shown in a view.
func (s *Server) viewTitles(ctx context.Context, profile models.User, viewKey string) []titleInfo {
	cacheKey := profile.ID + ":" + viewKey
	if titles, ok := s.views.get(cacheKey); ok {
		return titles
	}
	var titles []titleInfo
	switch viewKey {
	case viewMovies:
		titles = s.curatedTitles(profile, "movie")
	case viewShows:
		titles = s.curatedTitles(profile, "series")
	case viewTrendingMovies, viewTrendingShows:
		if isCuratedKidsProfile(profile) {
			break
		}
		mediaType := "movie"
		if viewKey == viewTrendingShows {
			mediaType = "series"
		}
		titles = s.trendingTitles(ctx, profile, mediaType)
	}
	titles = s.filterForProfile(ctx, profile, titles)
	// A cancelled request (clients abort on navigation) may have produced a
	// partial answer; never cache it. Empty results are cached like any other
	// (kids checks look views up per item).
	if ctx.Err() == nil {
		s.views.put(cacheKey, titles)
	}
	return titles
}

// curatedTitles merges watchlist, custom lists, in-progress and
// continue-watching titles of one media type, newest activity first.
func (s *Server) curatedTitles(profile models.User, mediaType string) []titleInfo {
	seen := map[string]int{}
	var out []titleInfo
	add := func(t titleInfo) {
		if t.MediaType != mediaType || strings.TrimSpace(t.Name) == "" {
			return
		}
		key := t.key()
		if idx, ok := seen[key]; ok {
			if out[idx].Poster == "" && t.Poster != "" {
				out[idx].Poster = t.Poster
			}
			if out[idx].AddedAt.Before(t.AddedAt) {
				out[idx].AddedAt = t.AddedAt
			}
			return
		}
		seen[key] = len(out)
		out = append(out, t)
	}

	if s.deps.Watchlist != nil {
		if items, err := s.deps.Watchlist.List(profile.ID); err == nil {
			for _, wi := range items {
				add(titleFromWatchlist(wi))
			}
		}
	}
	if s.deps.CustomLists != nil {
		if lists, err := s.deps.CustomLists.ListLists(profile.ID); err == nil {
			for _, list := range lists {
				items, err := s.deps.CustomLists.ListItems(profile.ID, list.ID)
				if err != nil {
					continue
				}
				for _, wi := range items {
					add(titleFromWatchlist(wi))
				}
			}
		}
	}
	if s.deps.History != nil {
		if mediaType == "movie" {
			if rows, err := s.deps.History.ListPlaybackProgress(profile.ID); err == nil {
				for _, p := range rows {
					if normalizeMediaType(p.MediaType) != "movie" || p.HiddenFromContinueWatching {
						continue
					}
					add(titleInfo{MediaType: "movie", ID: p.ItemID, Name: p.MovieName, Year: p.Year, ExternalIDs: p.ExternalIDs, AddedAt: p.UpdatedAt})
				}
			}
		} else {
			if rows, err := s.deps.History.ListContinueWatching(profile.ID); err == nil {
				for _, cw := range rows {
					if !isMovieWatchState(cw) {
						add(titleFromWatchState(cw))
					}
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AddedAt.After(out[j].AddedAt) })
	return out
}

// isMovieWatchState reports whether a continue-watching entry is a movie;
// mediastorm's continue-watching list mixes in-progress movies with series.
func isMovieWatchState(cw models.SeriesWatchState) bool {
	id := strings.ToLower(cw.SeriesID)
	if strings.Contains(id, ":movie:") {
		return true
	}
	return cw.NextEpisode == nil && cw.LastWatched.EpisodeNumber == 0 && cw.LastWatched.SeasonNumber == 0
}

func titleFromWatchState(cw models.SeriesWatchState) titleInfo {
	return titleInfo{
		MediaType:   "series",
		ID:          cw.SeriesID,
		Name:        cw.SeriesTitle,
		Overview:    cw.Overview,
		Year:        cw.Year,
		ExternalIDs: cw.ExternalIDs,
		Poster:      firstNonEmpty(cw.PosterURL, cw.TextPosterURL),
		Backdrop:    firstNonEmpty(cw.BackdropURL, cw.TextBackdropURL),
		Status:      cw.LifecycleStatus,
		AddedAt:     cw.UpdatedAt,
	}
}

func (s *Server) trendingTitles(ctx context.Context, profile models.User, mediaType string) []titleInfo {
	if s.deps.Metadata == nil {
		return nil
	}
	items, err := s.deps.Metadata.Trending(ctx, mediaType)
	if err != nil {
		return nil
	}
	if profile.IsKidsProfile {
		items = append([]models.TrendingItem(nil), items...)
		s.deps.Metadata.EnrichTrendingCertifications(ctx, items)
	}
	out := make([]titleInfo, 0, len(items))
	for _, it := range items {
		t := titleFromMetadata(it.Title)
		t.MediaType = mediaType
		out = append(out, t)
	}
	return out
}

func (s *Server) searchTitles(ctx context.Context, profile models.User, term, mediaType string) []titleInfo {
	if s.deps.Metadata == nil || strings.TrimSpace(term) == "" || isCuratedKidsProfile(profile) {
		return nil
	}
	results, err := s.deps.Metadata.Search(ctx, term, mediaType)
	if err != nil {
		return nil
	}
	if profile.IsKidsProfile {
		s.deps.Metadata.EnrichSearchCertifications(ctx, results)
	}
	out := make([]titleInfo, 0, len(results))
	for _, res := range results {
		t := titleFromMetadata(res.Title)
		if res.Title.MediaType == "person" {
			continue
		}
		if mediaType != "" {
			t.MediaType = normalizeMediaType(mediaType)
		}
		out = append(out, t)
	}
	return s.filterForProfile(ctx, profile, out)
}

// isCuratedKidsProfile reports a kids profile restricted to curated lists;
// like the main API, such profiles get no search and no discovery shelves.
func isCuratedKidsProfile(profile models.User) bool {
	return profile.IsKidsProfile && profile.KidsMode == "content_list"
}

// filterForProfile applies kids-profile rating limits with the main app's
// rules (services/kids): titles without a known certification are hidden.
func (s *Server) filterForProfile(ctx context.Context, profile models.User, titles []titleInfo) []titleInfo {
	if !profile.IsKidsProfile || profile.KidsMode != "rating" {
		return titles
	}
	movieLimit := firstNonEmpty(profile.KidsMaxMovieRating, profile.KidsMaxRating)
	tvLimit := firstNonEmpty(profile.KidsMaxTVRating, profile.KidsMaxRating)
	s.enrichCertifications(ctx, titles)
	out := titles[:0:0]
	for _, t := range titles {
		limit, mediaType := movieLimit, "movie"
		if t.MediaType == "series" {
			limit, mediaType = tvLimit, "series"
		}
		if kids.IsRatingAllowed(t.Certification, limit, mediaType) {
			out = append(out, t)
		}
	}
	return out
}

// enrichCertifications fills in missing content ratings (watchlist entries
// do not carry them).
func (s *Server) enrichCertifications(ctx context.Context, titles []titleInfo) {
	if s.deps.Metadata == nil {
		return
	}
	var missing []int
	var lookup []models.Title
	for i, t := range titles {
		if t.Certification != "" {
			continue
		}
		mt := models.Title{ID: t.ID, Name: t.Name, Year: t.Year, MediaType: t.MediaType, IMDBID: t.ExternalIDs["imdb"]}
		mt.TMDBID, _ = strconv.ParseInt(t.ExternalIDs["tmdb"], 10, 64)
		mt.TVDBID, _ = strconv.ParseInt(t.ExternalIDs["tvdb"], 10, 64)
		missing = append(missing, i)
		lookup = append(lookup, mt)
	}
	if len(lookup) == 0 {
		return
	}
	s.deps.Metadata.EnrichTitleCertifications(ctx, lookup)
	for j, i := range missing {
		titles[i].Certification = lookup[j].Certification
	}
}

// listItems returns the BoxSet folders for the profile's custom lists.
func (s *Server) listFolders(profile models.User) []dto.BaseItemDto {
	if s.deps.CustomLists == nil {
		return nil
	}
	lists, err := s.deps.CustomLists.ListLists(profile.ID)
	if err != nil {
		return nil
	}
	out := make([]dto.BaseItemDto, 0, len(lists))
	for _, list := range lists {
		items, _ := s.deps.CustomLists.ListItems(profile.ID, list.ID)
		poster := ""
		for _, wi := range items {
			if wi.PosterURL != "" {
				poster = wi.PosterURL
				break
			}
		}
		// This ref is profile-scoped; changes to the list's cover are safe to
		// replace and must match the image tag returned below.
		id := s.ids.putTrusted(refList, profile.ID+":"+list.ID, refContext{Name: list.Name, ProfileID: profile.ID, Extra: list.ID, Poster: poster})
		item := dto.BaseItemDto{
			Name:                    list.Name,
			ServerID:                s.serverID,
			ID:                      id,
			Type:                    dto.KindBoxSet,
			IsFolder:                boolPtr(true),
			MediaType:               dto.MediaTypeUnknown,
			LocationType:            dto.LocationFileSystem,
			ChildCount:              intPtr(len(items)),
			DisplayPreferencesID:    id,
			SortName:                sortName(list.Name),
			PlayAccess:              "Full",
			UserData:                &dto.UserItemData{Key: id, ItemID: id},
			PrimaryImageAspectRatio: floatPtr(2.0 / 3.0),
		}
		if poster != "" {
			item.ImageTags = map[string]string{dto.ImagePrimary: imageTag(poster)}
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) listTitles(profile models.User, listID string) []titleInfo {
	if s.deps.CustomLists == nil {
		return nil
	}
	items, err := s.deps.CustomLists.ListItems(profile.ID, listID)
	if err != nil {
		return nil
	}
	out := make([]titleInfo, 0, len(items))
	for _, wi := range items {
		out = append(out, titleFromWatchlist(wi))
	}
	return s.filterForProfile(context.Background(), profile, out)
}

// kidsAllowsRef applies kids-profile restrictions to an item reached by ID.
// Curated (content_list) profiles may only open curated titles; rating
// profiles may only open titles within their limits. Episodes and seasons
// follow their series.
func (s *Server) kidsAllowsRef(ctx context.Context, profile models.User, ref itemRef) bool {
	if !profile.IsKidsProfile {
		return true
	}
	var t titleInfo
	switch ref.Kind {
	case refMovie, refSeries:
		t = titleFromRef(ref)
	case refSeason, refEpisode:
		t = seriesInfoFromSeasonRef(ref)
	case refChannel, refProgram:
		return false // no Live TV for kids profiles
	default:
		return true // views, lists, local files are scoped by their own checks
	}
	if isCuratedKidsProfile(profile) {
		key := t.key()
		for _, view := range []string{viewMovies, viewShows} {
			for _, c := range s.viewTitles(ctx, profile, view) {
				if c.key() == key {
					return true
				}
			}
		}
		return false
	}
	return len(s.filterForProfile(ctx, profile, []titleInfo{t})) == 1
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
