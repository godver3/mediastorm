package jellyfinbridge

import (
	"context"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/internal/mediaidentity"
	"novastream/models"
)

const refPerson = "person"

// maxIDsPerQuery bounds /Items?Ids= lookups (each may hit metadata).
const maxIDsPerQuery = 200

// itemQuery is the parsed form of /Items query parameters.
type itemQuery struct {
	ParentID     string
	IncludeTypes []string
	ExcludeTypes []string
	Recursive    bool
	SearchTerm   string
	IDs          []string
	Filters      []string
	SortBy       []string
	SortOrder    string
	StartIndex   int
	Limit        int
	NameStarts   string
	NameLessThan string
	Genres       []string
	IsFavorite   *bool
	IsPlayed     *bool
	PersonIDs    []string
	GenreIDs     []string
	StudioIDs    []string
	Studios      []string
	SeriesStatus []string
	Years        []string
}

func parseItemQuery(r *http.Request) itemQuery {
	q := itemQuery{
		ParentID:     queryValue(r, "ParentId"),
		IncludeTypes: queryList(r, "IncludeItemTypes"),
		ExcludeTypes: queryList(r, "ExcludeItemTypes"),
		SearchTerm:   strings.TrimSpace(queryValue(r, "SearchTerm")),
		IDs:          queryList(r, "Ids"),
		Filters:      queryList(r, "Filters"),
		SortBy:       queryList(r, "SortBy"),
		SortOrder:    queryValue(r, "SortOrder"),
		StartIndex:   parseIntDefault(queryValue(r, "StartIndex"), 0),
		Limit:        parseIntDefault(queryValue(r, "Limit"), 0),
		NameStarts:   queryValue(r, "NameStartsWith"),
		NameLessThan: queryValue(r, "NameLessThan"),
		Genres:       splitPipes(queryList(r, "Genres")),
		PersonIDs:    queryList(r, "PersonIds"),
		GenreIDs:     queryList(r, "GenreIds"),
		StudioIDs:    queryList(r, "StudioIds"),
		SeriesStatus: queryList(r, "SeriesStatus"),
		Years:        queryList(r, "Years"),
	}
	q.Recursive, _ = queryBool(r, "Recursive")
	if v, ok := queryBool(r, "IsFavorite"); ok {
		q.IsFavorite = &v
	}
	if v, ok := queryBool(r, "IsPlayed"); ok {
		q.IsPlayed = &v
	}
	for _, f := range q.Filters {
		switch strings.ToLower(f) {
		case "isfavorite", "isfavoriteorlikes":
			q.IsFavorite = boolPtr(true)
		case "isplayed":
			q.IsPlayed = boolPtr(true)
		case "isunplayed":
			q.IsPlayed = boolPtr(false)
		}
	}
	if q.StartIndex < 0 {
		q.StartIndex = 0
	}
	if q.Limit < 0 {
		q.Limit = 0
	}
	return q
}

func splitPipes(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, "|") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func (q itemQuery) wants(kind string) bool {
	if len(q.ExcludeTypes) > 0 && containsFold(q.ExcludeTypes, kind) {
		return false
	}
	return len(q.IncludeTypes) == 0 || containsFold(q.IncludeTypes, kind)
}

// wantsTitles reports whether a query could return movies/series at all.
func (q itemQuery) titleMediaTypes() []string {
	var types []string
	if q.wants(dto.KindMovie) {
		types = append(types, "movie")
	}
	if q.wants(dto.KindSeries) {
		types = append(types, "series")
	}
	return types
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	q := parseItemQuery(r)
	items := s.queryItems(r.Context(), p, q)
	page := s.page(items, q)
	if wantsTrickplay(r) {
		s.withTrickplay(p, page.Items)
	}
	s.withStreamDetails(p, page.Items)
	withPlayableSources(page.Items)
	writeJSON(w, http.StatusOK, page)
}

// queryItems answers an /Items query (before sorting and paging).
func (s *Server) queryItems(ctx context.Context, p *principal, q itemQuery) []dto.BaseItemDto {
	u := s.newUserState(p.Profile.ID)
	if len(q.GenreIDs) > 0 {
		names := s.resolveGenreIDs(q.GenreIDs)
		if len(names) == 0 {
			return nil
		}
		q.Genres = append(q.Genres, names...)
	}
	if len(q.StudioIDs) > 0 {
		if q.Studios = s.resolveStudioIDs(q.StudioIDs); len(q.Studios) == 0 {
			return nil
		}
	}
	if len(q.PersonIDs) > 0 {
		return s.filterItems(u, q, s.personTitles(ctx, p, u, q))
	}

	if len(q.IDs) > 0 {
		if len(q.IDs) > maxIDsPerQuery {
			q.IDs = q.IDs[:maxIDsPerQuery]
		}
		var out []dto.BaseItemDto
		for _, id := range q.IDs {
			if item, ok := s.itemByID(ctx, p, u, s.itemIDOf(id), false); ok {
				if itemID, isVersion := s.versionItem(id); isVersion && itemID == item.ID {
					asVersion(&item, normalizeID(id))
				}
				out = append(out, item)
			}
		}
		return out
	}

	if q.ParentID != "" {
		return s.filterItems(u, q, s.childrenOf(ctx, p, u, q))
	}

	if q.SearchTerm != "" {
		var out []dto.BaseItemDto
		for _, mt := range q.titleMediaTypes() {
			for _, t := range s.searchTitles(ctx, p.Profile, q.SearchTerm, mt) {
				out = append(out, s.titleItem(u, t))
			}
		}
		if q.wants(dto.KindTvChannel) && len(q.IncludeTypes) > 0 {
			out = append(out, s.searchChannels(ctx, p, q.SearchTerm)...)
		}
		return s.filterItems(u, q, out)
	}

	// Library-wide queries without a parent: favorites, collections, or every
	// curated title of the requested types.
	var out []dto.BaseItemDto
	if q.wants(dto.KindBoxSet) && len(q.IncludeTypes) > 0 {
		out = append(out, s.listFolders(p.Profile)...)
	}
	if q.IsFavorite != nil && *q.IsFavorite {
		if s.deps.Watchlist != nil {
			if wl, err := s.deps.Watchlist.List(p.Profile.ID); err == nil {
				var titles []titleInfo
				for _, wi := range wl {
					if t := titleFromWatchlist(wi); q.wants(kindForMediaType(t.MediaType)) {
						titles = append(titles, t)
					}
				}
				for _, t := range s.filterForProfile(ctx, p.Profile, titles) {
					out = append(out, s.titleItem(u, t))
				}
			}
		}
		return s.filterItems(u, q, out)
	}
	for _, mt := range q.titleMediaTypes() {
		if len(q.IncludeTypes) == 0 {
			break // an unfiltered, parentless query would be "everything"; stay empty
		}
		view := viewMovies
		if mt == "series" {
			view = viewShows
		}
		for _, t := range s.viewTitles(ctx, p.Profile, view) {
			out = append(out, s.titleItem(u, t))
		}
	}
	return s.filterItems(u, q, out)
}

func kindForMediaType(mt string) string {
	if mt == "series" {
		return dto.KindSeries
	}
	return dto.KindMovie
}

// childrenOf lists the direct (or recursive) children of q.ParentID.
func (s *Server) childrenOf(ctx context.Context, p *principal, u *userState, q itemQuery) []dto.BaseItemDto {
	ref, ok := s.ids.get(q.ParentID)
	if !ok {
		return nil
	}
	switch ref.Kind {
	case refView:
		if ref.Ctx.ProfileID != p.Profile.ID {
			return nil
		}
		switch ref.Ctx.ViewKey {
		case viewLists:
			if q.Recursive && (q.wants(dto.KindMovie) || q.wants(dto.KindSeries)) && !q.wants(dto.KindBoxSet) {
				var out []dto.BaseItemDto
				for _, list := range s.listFolders(p.Profile) {
					if lref, ok := s.ids.get(list.ID); ok {
						for _, t := range s.listTitles(p.Profile, lref.Ctx.Extra) {
							out = append(out, s.titleItem(u, t))
						}
					}
				}
				return out
			}
			return s.listFolders(p.Profile)
		case viewLiveTV:
			return s.channelItems(ctx, p, true)
		default:
			if libraryID, ok := strings.CutPrefix(ref.Ctx.ViewKey, localViewPrefix); ok {
				items := s.localViewChildren(ctx, p, u, libraryID)
				if q.Recursive && containsFold(q.IncludeTypes, dto.KindEpisode) {
					var eps []dto.BaseItemDto
					for _, g := range s.localLibraryGroups(ctx, p, libraryID) {
						eps = append(eps, s.localEpisodeItems(u, libraryID, g, -1)...)
					}
					return eps
				}
				return items
			}
			// Clients that search per library (Plezy) send the library as
			// ParentId; the movie and show libraries search the whole
			// catalog, as a search without a library does.
			if mediaType := catalogSearchType(ref.Ctx.ViewKey); q.SearchTerm != "" && mediaType != "" {
				titles := s.searchTitles(ctx, p.Profile, q.SearchTerm, mediaType)
				out := make([]dto.BaseItemDto, 0, len(titles))
				for _, t := range titles {
					out = append(out, s.titleItem(u, t))
				}
				return out
			}
			titles := s.viewTitles(ctx, p.Profile, ref.Ctx.ViewKey)
			if q.SearchTerm != "" {
				titles = filterTitlesByName(titles, q.SearchTerm)
			}
			out := make([]dto.BaseItemDto, 0, len(titles))
			for _, t := range titles {
				out = append(out, s.titleItem(u, t))
			}
			return out
		}
	case refList:
		if ref.Ctx.ProfileID != p.Profile.ID {
			return nil
		}
		titles := s.listTitles(p.Profile, ref.Ctx.Extra)
		out := make([]dto.BaseItemDto, 0, len(titles))
		for _, t := range titles {
			out = append(out, s.titleItem(u, t))
		}
		return out
	case refSeries:
		series := s.seriesRefFrom(titleFromRef(ref))
		if q.Recursive || containsFold(q.IncludeTypes, dto.KindEpisode) {
			return s.seriesEpisodes(ctx, u, series, -1)
		}
		return s.seriesSeasons(ctx, u, series)
	case refSeason:
		series := s.seriesRefFrom(s.seriesWithArtwork(seriesInfoFromSeasonRef(ref)))
		series.Ordering = ref.Ctx.SeasonOrdering
		return s.seriesEpisodes(ctx, u, series, ref.Ctx.Season)
	case refLocalSeries:
		g, ok := s.findLocalGroup(ctx, p, ref.Ctx.Extra, ref.Ctx.ViewKey)
		if !ok {
			return nil
		}
		if q.Recursive || containsFold(q.IncludeTypes, dto.KindEpisode) {
			return s.localEpisodeItems(u, ref.Ctx.Extra, g, -1)
		}
		return s.localSeasonItems(u, ref.Ctx.Extra, g)
	case refLocalSeason:
		if g, ok := s.findLocalGroup(ctx, p, ref.Ctx.Extra, ref.Ctx.ViewKey); ok {
			return s.localEpisodeItems(u, ref.Ctx.Extra, g, ref.Ctx.Season)
		}
	}
	return nil
}

// catalogSearchType is the catalog media type a library searches ("" for
// libraries that only search their own titles).
func catalogSearchType(viewKey string) string {
	switch viewKey {
	case viewMovies:
		return "movie"
	case viewShows:
		return "series"
	}
	return ""
}

func filterTitlesByName(titles []titleInfo, term string) []titleInfo {
	term = strings.ToLower(term)
	var out []titleInfo
	for _, t := range titles {
		if strings.Contains(strings.ToLower(t.Name), term) {
			out = append(out, t)
		}
	}
	return out
}

// seriesInfoFromSeasonRef rebuilds the parent series from a season/episode ref.
func seriesInfoFromSeasonRef(ref itemRef) titleInfo {
	name := ref.Ctx.SeriesName
	if name == "" {
		name = ref.Ctx.Name
	}
	// The series identity is encoded in the ref's own source.
	id, ext := ref.Ctx.TitleID, ref.Ctx.ExternalIDs
	seriesSource := ""
	switch ref.Kind {
	case refSeason:
		if i := strings.Index(ref.Source, "/s"); i > 0 {
			seriesSource = ref.Source[:i]
		}
	case refEpisode:
		if series, _, _, ok := mediaidentity.ParseEpisodeID(ref.Source); ok {
			seriesSource = series
		}
	}
	if seriesSource != "" {
		id = seriesSource
		ext = mediaidentity.MergeExternalIDs(externalIDsFromSource("series", seriesSource), ext)
	}
	return titleInfo{
		MediaType:   "series",
		ID:          id,
		Name:        name,
		Year:        ref.Ctx.Year,
		ExternalIDs: titleLevelIDs(ext),
		// A child's own poster may be a season cover or episode still.
		// Legacy refs without SeriesPoster recover it via seriesWithArtwork.
		Poster:   ref.Ctx.SeriesPoster,
		Backdrop: ref.Ctx.Backdrop,
		Logo:     ref.Ctx.Logo,
	}
}

// seriesWithArtwork restores series artwork from its own ref when a
// season/episode ref only carried the episode still.
func (s *Server) seriesWithArtwork(t titleInfo) titleInfo {
	if ref, ok := s.ids.get(deriveID(refSeries, t.source())); ok {
		stored := titleFromRef(ref)
		t.Poster = firstNonEmpty(stored.Poster, t.Poster)
		t.Backdrop = firstNonEmpty(stored.Backdrop, t.Backdrop)
		t.Logo = firstNonEmpty(stored.Logo, t.Logo)
		t.Name = firstNonEmpty(stored.Name, t.Name)
	}
	return t
}

func (s *Server) seriesSeasons(ctx context.Context, u *userState, series seriesRef) []dto.BaseItemDto {
	details, err := s.seriesDetails(ctx, series.Info, series.Ordering)
	if err != nil || details == nil {
		return nil
	}
	series = s.refreshSeries(series, details.Title)
	series.Ordering = details.ActiveOrdering
	var out []dto.BaseItemDto
	for _, season := range details.Seasons {
		if len(airedEpisodes(season.Episodes)) == 0 {
			continue
		}
		out = append(out, s.seasonItem(u, series, season))
	}
	return out
}

// refreshSeries fills in artwork/name from fetched series details.
func (s *Server) refreshSeries(series seriesRef, title models.Title) seriesRef {
	rich := titleFromMetadata(title)
	info := series.Info
	info.Name = firstNonEmpty(rich.Name, info.Name)
	info.Poster = firstNonEmpty(rich.Poster, info.Poster)
	info.Backdrop = firstNonEmpty(rich.Backdrop, info.Backdrop)
	info.Logo = firstNonEmpty(rich.Logo, info.Logo)
	if rich.Year != 0 {
		info.Year = rich.Year
	}
	info.ExternalIDs = mediaidentity.MergeExternalIDs(rich.ExternalIDs, info.ExternalIDs)
	series.Info = info
	s.ids.putTrusted(refSeries, series.Src, info.context())
	return series
}

// airedEpisodes drops episodes that have not aired yet; they cannot be
// resolved to a stream, and Jellyfin hides missing episodes by default.
func airedEpisodes(eps []models.SeriesEpisode) []models.SeriesEpisode {
	now := time.Now()
	var out []models.SeriesEpisode
	for _, ep := range eps {
		if d := parseDate(firstNonEmpty(ep.AiredDateTimeUTC, ep.AiredDate)); d != nil && d.After(now) {
			continue
		}
		out = append(out, ep)
	}
	return out
}

// seriesEpisodes lists aired episodes of one season, or all regular seasons
// (season < 0), in airing order.
func (s *Server) seriesEpisodes(ctx context.Context, u *userState, series seriesRef, season int) []dto.BaseItemDto {
	details, err := s.seriesDetails(ctx, series.Info, series.Ordering)
	if err != nil || details == nil {
		return nil
	}
	series = s.refreshSeries(series, details.Title)
	series.Ordering = details.ActiveOrdering
	var out []dto.BaseItemDto
	for _, sn := range details.Seasons {
		if season >= 0 && sn.Number != season {
			continue
		}
		if season < 0 && sn.Number == 0 {
			continue
		}
		// Register the actual season cover even when episodes are requested
		// first (continue watching, recursive browsing, or a direct link).
		s.seasonItem(nil, series, sn)
		for _, ep := range airedEpisodes(sn.Episodes) {
			out = append(out, s.episodeItem(u, series, episodeFromMetadata(ep)))
		}
	}
	return out
}

// filterItems applies type, favorite, played, genre and name filters.
func (s *Server) filterItems(u *userState, q itemQuery, items []dto.BaseItemDto) []dto.BaseItemDto {
	out := items[:0:0]
	for _, item := range items {
		if (len(q.IncludeTypes) > 0 || len(q.ExcludeTypes) > 0) && !q.wants(item.Type) {
			continue
		}
		if q.IsFavorite != nil && (item.UserData == nil || item.UserData.IsFavorite != *q.IsFavorite) {
			continue
		}
		if q.IsPlayed != nil && (item.UserData == nil || item.UserData.Played != *q.IsPlayed) {
			continue
		}
		if containsFold(q.Filters, "IsResumable") && (item.UserData == nil || item.UserData.PlaybackPositionTicks <= 0) {
			continue
		}
		if len(q.Studios) > 0 && !anyFold(studioNames(item.Studios), q.Studios) {
			continue
		}
		if len(q.Genres) > 0 && !anyFold(item.Genres, q.Genres) {
			continue
		}
		if q.NameStarts != "" && !strings.HasPrefix(strings.ToLower(item.SortName), strings.ToLower(q.NameStarts)) {
			continue
		}
		if q.NameLessThan != "" && strings.ToLower(item.SortName) >= strings.ToLower(q.NameLessThan) {
			continue
		}
		if len(q.Years) > 0 && (item.ProductionYear == nil || !containsFold(q.Years, strconv.Itoa(*item.ProductionYear))) {
			continue
		}
		if len(q.SeriesStatus) > 0 && item.Type == dto.KindSeries && !containsFold(q.SeriesStatus, item.Status) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func studioNames(pairs []dto.NameGuidPair) []string {
	names := make([]string, len(pairs))
	for i, p := range pairs {
		names[i] = p.Name
	}
	return names
}

func anyFold(have, want []string) bool {
	for _, w := range want {
		if containsFold(have, w) {
			return true
		}
	}
	return false
}

// page sorts and slices items into a QueryResult.
func (s *Server) page(items []dto.BaseItemDto, q itemQuery) dto.QueryResult[dto.BaseItemDto] {
	sortItems(items, q.SortBy, q.SortOrder)
	total := len(items)
	start := q.StartIndex
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := total
	if q.Limit > 0 && q.Limit < total-start {
		end = start + q.Limit
	}
	return dto.NewQueryResult(items[start:end], total, start)
}

func sortItems(items []dto.BaseItemDto, sortBy []string, order string) {
	if len(sortBy) == 0 {
		return
	}
	desc := strings.EqualFold(order, "Descending")
	primary := strings.ToLower(sortBy[0])
	if primary == "random" {
		rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
		return
	}
	less := func(a, b dto.BaseItemDto) bool {
		switch primary {
		case "datecreated", "datelastcontentadded":
			return timeOf(a.DateCreated).Before(timeOf(b.DateCreated))
		case "premieredate", "productionyear", "startdate":
			return timeOf(a.PremiereDate).Before(timeOf(b.PremiereDate))
		case "communityrating", "criticrating":
			return floatOf(a.CommunityRating) < floatOf(b.CommunityRating)
		case "dateplayed":
			return timeOf(userDataPlayed(a)).Before(timeOf(userDataPlayed(b)))
		case "runtime":
			return int64Of(a.RunTimeTicks) < int64Of(b.RunTimeTicks)
		case "indexnumber", "parentindexnumber":
			return a.SortName < b.SortName
		default: // SortName, Name, SeriesSortName
			return a.SortName < b.SortName
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if desc {
			return less(items[j], items[i])
		}
		return less(items[i], items[j])
	})
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func floatOf(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func int64Of(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func userDataPlayed(item dto.BaseItemDto) *time.Time {
	if item.UserData == nil {
		return nil
	}
	return item.UserData.LastPlayedDate
}

// --- single item ---------------------------------------------------------

func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	u := s.newUserState(p.Profile.ID)
	id := pathVar(r, "id")
	start := time.Now()
	item, ok := s.itemByID(r.Context(), p, u, id, true)
	if !ok {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	if s.settings().PrequeueOnDetails && (item.Type == dto.KindMovie || item.Type == dto.KindEpisode) {
		s.prewarm(p, item.ID)
	}
	if ref, ok := s.ids.get(item.ID); ok {
		s.itemVersions(p, &item, ref)
	}
	if item.Type == dto.KindSeries {
		go s.prefetchNextUp(p, item.ID)
	}
	if item.Type == dto.KindMovie {
		// Movies keep the chapters of this response for playback; give a
		// stream resolving for them the rest of the details wait.
		s.awaitChapters(r.Context(), p.Profile.ID, item.ID, releaseDetailsWait-time.Since(start))
	}
	items := []dto.BaseItemDto{item}
	s.withTrickplay(p, items)
	s.withStreamDetails(p, items)
	withPlayableSources(items)
	if v := requestedVersion(r); v != "" {
		asVersion(&items[0], v)
	}
	writeJSON(w, http.StatusOK, items[0])
}

// itemByID builds any item from its GUID. detailed fetches full metadata.
func (s *Server) itemByID(ctx context.Context, p *principal, u *userState, id string, detailed bool) (dto.BaseItemDto, bool) {
	ref, ok := s.ids.get(id)
	if !ok {
		if profile, ok := s.profileByUserID(id); ok && profile.ID == p.Profile.ID {
			return s.userRootFolder(p), true
		}
		return dto.BaseItemDto{}, false
	}
	// IDs are deterministic, so a kids profile could address any title
	// directly; apply the same restrictions as the listings.
	if !s.kidsAllowsRef(ctx, p.Profile, ref) {
		return dto.BaseItemDto{}, false
	}
	switch ref.Kind {
	case refView:
		if ref.Ctx.ProfileID != p.Profile.ID {
			return dto.BaseItemDto{}, false
		}
		defs := s.viewDefs(p.Profile)
		if strings.HasPrefix(ref.Ctx.ViewKey, localViewPrefix) {
			defs = localViewDefs(s.localLibraries(ctx, p))
		}
		for _, def := range defs {
			if def.Key == ref.Ctx.ViewKey {
				return s.viewItem(p.Profile, def), true
			}
		}
	case refList:
		if ref.Ctx.ProfileID != p.Profile.ID {
			return dto.BaseItemDto{}, false
		}
		for _, list := range s.listFolders(p.Profile) {
			if list.ID == normalizeID(id) {
				return list, true
			}
		}
	case refMovie, refSeries:
		t := titleFromRef(ref)
		if detailed {
			t = s.enrich(ctx, t)
		}
		item := s.titleItem(u, t)
		if detailed {
			item.People = s.people(ctx, t)
		}
		return item, true
	case refSeason:
		series := s.seriesRefFrom(s.seriesWithArtwork(seriesInfoFromSeasonRef(ref)))
		series.Ordering = ref.Ctx.SeasonOrdering
		if detailed {
			if details, err := s.seriesDetails(ctx, series.Info, series.Ordering); err == nil && details != nil {
				series = s.refreshSeries(series, details.Title)
				series.Ordering = details.ActiveOrdering
				for _, season := range details.Seasons {
					if season.Number == ref.Ctx.Season {
						return s.seasonItem(u, series, season), true
					}
				}
			}
		}
		return s.seasonItem(u, series, models.SeriesSeason{Number: ref.Ctx.Season}), true
	case refEpisode:
		series := s.seriesRefFrom(s.seriesWithArtwork(seriesInfoFromSeasonRef(ref)))
		series.Ordering = ref.Ctx.SeasonOrdering
		if detailed {
			if details, err := s.seriesDetails(ctx, series.Info, series.Ordering); err == nil && details != nil {
				series = s.refreshSeries(series, details.Title)
				series.Ordering = details.ActiveOrdering
				for _, season := range details.Seasons {
					if season.Number != ref.Ctx.Season {
						continue
					}
					for _, ep := range season.Episodes {
						if ep.EpisodeNumber == ref.Ctx.Episode {
							s.seasonItem(nil, series, season)
							return s.episodeItem(u, series, episodeFromMetadata(ep)), true
						}
					}
				}
			}
		}
		return s.episodeItem(u, series, episodeInfo{Season: ref.Ctx.Season, Episode: ref.Ctx.Episode, Name: ref.Ctx.Name, Still: ref.Ctx.Thumb}), true
	case refLocal, refLocalSeries, refLocalSeason:
		return s.localItemByRef(ctx, p, u, ref)
	case refChannel:
		return s.channelByRef(ctx, p, ref)
	case refProgram:
		return s.programByRef(ctx, p, ref)
	case refPerson:
		return s.personItem(ctx, id, ref, detailed), true
	case refGenre:
		return s.genreItem(id, ref), true
	case refStudio:
		return s.studioItem(id, ref), true
	}
	return dto.BaseItemDto{}, false
}

func imageTagsFor(poster string) map[string]string {
	if poster == "" {
		return nil
	}
	return map[string]string{dto.ImagePrimary: imageTag(poster)}
}

// people returns the top-billed cast as Jellyfin people.
func (s *Server) people(ctx context.Context, t titleInfo) []dto.Person {
	if s.deps.Metadata == nil {
		return nil
	}
	var full *models.Title
	if t.MediaType == "series" {
		full, _ = s.deps.Metadata.SeriesInfo(ctx, t.seriesQuery())
	} else {
		full, _ = s.deps.Metadata.MovieDetails(ctx, t.movieQuery())
	}
	if full == nil || full.Credits == nil {
		return nil
	}
	var out []dto.Person
	for i, member := range full.Credits.Cast {
		if i >= 20 {
			break
		}
		id := s.ids.putTrusted(refPerson, "tmdb:person:"+strconv.FormatInt(member.ID, 10), refContext{Name: member.Name, Poster: member.ProfileURL})
		ref, _ := s.ids.get(id)
		out = append(out, dto.Person{Name: member.Name, ID: id, Role: member.Character, Type: "Actor", PrimaryImageTag: imageTag(imageURLFor(ref, "primary"))})
	}
	return out
}

// userRootFolder answers /Items/Root and lookups of the user's own GUID.
func (s *Server) userRootFolder(p *principal) dto.BaseItemDto {
	id := s.userID(p.Profile)
	return dto.BaseItemDto{
		Name: "Media Folders", ServerID: s.serverID, ID: id, Type: "UserRootFolder",
		IsFolder: boolPtr(true), LocationType: dto.LocationFileSystem, DisplayPreferencesID: id,
	}
}

func (s *Server) handleRootFolder(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.userRootFolder(principalFrom(r)))
}

// --- shows ---------------------------------------------------------------

func (s *Server) handleSeasons(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ref, ok := s.ids.get(pathVar(r, "id"))
	if ok && !s.kidsAllowsRef(r.Context(), p.Profile, ref) {
		ok = false
	}
	if ok && ref.Kind == refLocalSeries {
		var items []dto.BaseItemDto
		if g, found := s.findLocalGroup(r.Context(), p, ref.Ctx.Extra, ref.Ctx.ViewKey); found {
			items = s.localSeasonItems(s.newUserState(p.Profile.ID), ref.Ctx.Extra, g)
		}
		writeJSON(w, http.StatusOK, dto.NewQueryResult(items, len(items), 0))
		return
	}
	if !ok || ref.Kind != refSeries {
		writeJSON(w, http.StatusOK, dto.NewQueryResult[dto.BaseItemDto](nil, 0, 0))
		return
	}
	items := s.seriesSeasons(r.Context(), s.newUserState(p.Profile.ID), s.seriesRefFrom(titleFromRef(ref)))
	writeJSON(w, http.StatusOK, dto.NewQueryResult(items, len(items), 0))
}

func (s *Server) handleEpisodes(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ref, ok := s.ids.get(pathVar(r, "id"))
	if ok && !s.kidsAllowsRef(r.Context(), p.Profile, ref) {
		ok = false
	}
	if !ok || (ref.Kind != refSeries && ref.Kind != refLocalSeries) {
		writeJSON(w, http.StatusOK, dto.NewQueryResult[dto.BaseItemDto](nil, 0, 0))
		return
	}
	season := -1
	if seasonID := queryValue(r, "SeasonId"); seasonID != "" {
		if sref, ok := s.ids.get(seasonID); ok && (sref.Kind == refSeason || sref.Kind == refLocalSeason) {
			season = sref.Ctx.Season
		}
	} else if n := queryValue(r, "Season"); n != "" {
		season = parseIntDefault(n, -1)
	}
	u := s.newUserState(p.Profile.ID)
	var items []dto.BaseItemDto
	if ref.Kind == refLocalSeries {
		if g, found := s.findLocalGroup(r.Context(), p, ref.Ctx.Extra, ref.Ctx.ViewKey); found {
			items = s.localEpisodeItems(u, ref.Ctx.Extra, g, season)
		}
	} else {
		items = s.seriesEpisodes(r.Context(), u, s.seriesRefFrom(titleFromRef(ref)), season)
	}
	// Android TV queues a picked version with its version item as StartItemId;
	// the queue then starts with that version item.
	version, versionOf := "", ""
	if start := normalizeID(queryValue(r, "StartItemId")); start != "" {
		if itemID, ok := s.versionItem(start); ok {
			version, versionOf, start = start, itemID, itemID
		}
		for i, item := range items {
			if item.ID == start {
				items = items[i:]
				break
			}
		}
	}
	if isMissing, ok := queryBool(r, "IsMissing"); ok && isMissing {
		items = nil
	}
	q := itemQuery{StartIndex: parseIntDefault(queryValue(r, "StartIndex"), 0), Limit: parseIntDefault(queryValue(r, "Limit"), 0)}
	result := s.page(items, q)
	s.withStreamDetails(p, result.Items)
	withPlayableSources(result.Items)
	if version != "" {
		for i := range result.Items {
			if result.Items[i].ID == versionOf {
				asVersion(&result.Items[i], version)
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleNextUp lists the next episode of every series in progress.
func (s *Server) handleNextUp(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	u := s.newUserState(p.Profile.ID)
	seriesFilter := normalizeID(queryValue(r, "SeriesId"))
	var items []dto.BaseItemDto
	if s.deps.History != nil {
		rows, _ := s.deps.History.ListContinueWatching(p.Profile.ID)
		for _, cw := range rows {
			if cw.NextEpisode == nil || cw.NextEpisode.SeasonNumber <= 0 {
				continue
			}
			series := s.seriesRefFrom(titleFromWatchState(cw))
			if seriesFilter != "" && series.GUID != seriesFilter {
				continue
			}
			items = append(items, s.episodeItem(u, series, episodeFromReference(*cw.NextEpisode)))
		}
	}
	q := itemQuery{StartIndex: parseIntDefault(queryValue(r, "StartIndex"), 0), Limit: parseIntDefault(queryValue(r, "Limit"), 0)}
	result := s.page(items, q)
	s.withStreamDetails(p, result.Items)
	withPlayableSources(result.Items)
	writeJSON(w, http.StatusOK, result)
}

// handleResume lists partially watched movies and episodes.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	// Only video resumes exist; the web home also asks for Audio and Book rows.
	if types := queryList(r, "MediaTypes"); len(types) > 0 && !containsFold(types, "Video") {
		writeJSON(w, http.StatusOK, dto.NewQueryResult[dto.BaseItemDto](nil, 0, 0))
		return
	}
	items := s.resumeItems(r.Context(), p, queryValue(r, "ParentId"))
	q := itemQuery{
		StartIndex:   parseIntDefault(queryValue(r, "StartIndex"), 0),
		Limit:        parseIntDefault(queryValue(r, "Limit"), 0),
		IncludeTypes: queryList(r, "IncludeItemTypes"),
		ExcludeTypes: queryList(r, "ExcludeItemTypes"),
	}
	items = s.filterItems(s.newUserState(p.Profile.ID), q, items)
	result := s.page(items, q)
	s.withStreamDetails(p, result.Items)
	withPlayableSources(result.Items)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resumeItems(_ context.Context, p *principal, parentID string) []dto.BaseItemDto {
	if s.deps.History == nil {
		return nil
	}
	wantMovies, wantEpisodes := true, true
	if ref, ok := s.ids.get(parentID); ok && ref.Kind == refView {
		wantMovies = ref.Ctx.ViewKey == viewMovies || ref.Ctx.ViewKey == viewTrendingMovies
		wantEpisodes = ref.Ctx.ViewKey == viewShows || ref.Ctx.ViewKey == viewTrendingShows
	}
	rows, err := s.deps.History.ListPlaybackProgress(p.Profile.ID)
	if err != nil {
		return nil
	}
	seriesArt := map[string]titleInfo{}
	if wantEpisodes {
		if cws, err := s.deps.History.ListContinueWatching(p.Profile.ID); err == nil {
			for _, cw := range cws {
				t := titleFromWatchState(cw)
				seriesArt[t.source()] = t
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].UpdatedAt.After(rows[j].UpdatedAt) })
	u := s.newUserState(p.Profile.ID)
	var out []dto.BaseItemDto
	for _, row := range rows {
		if row.HiddenFromContinueWatching || row.Position <= 0 || row.PercentWatched >= 90 {
			continue
		}
		switch normalizeMediaType(row.MediaType) {
		case "movie":
			if !wantMovies {
				continue
			}
			t := titleInfo{MediaType: "movie", ID: row.ItemID, Name: row.MovieName, Year: row.Year, ExternalIDs: row.ExternalIDs, AddedAt: row.UpdatedAt}
			if ref, ok := s.ids.get(deriveID(refMovie, t.source())); ok {
				stored := titleFromRef(ref)
				t.Poster, t.Backdrop, t.Logo = stored.Poster, stored.Backdrop, stored.Logo
				t.Name = firstNonEmpty(t.Name, stored.Name)
			}
			if t.Name == "" {
				continue
			}
			out = append(out, s.titleItem(u, t))
		case "episode":
			if !wantEpisodes || row.SeasonNumber <= 0 {
				continue
			}
			st := titleInfo{MediaType: "series", ID: row.SeriesID, Name: row.SeriesName, ExternalIDs: seriesOnlyIDs(row.ExternalIDs)}
			if art, ok := seriesArt[st.source()]; ok {
				st = art
			}
			st = s.seriesWithArtwork(st)
			item := s.episodeItem(u, s.seriesRefFrom(st), episodeInfo{Season: row.SeasonNumber, Episode: row.EpisodeNumber, Name: row.EpisodeName})
			out = append(out, item)
		}
	}
	return out
}

// seriesOnlyIDs drops episode-level external IDs from a progress row.
func seriesOnlyIDs(ext map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range ext {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "episode") {
			continue
		}
		out[k] = v
	}
	return out
}

// handleLatest returns the newest items of a view as a plain array.
func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	u := s.newUserState(p.Profile.ID)
	limit := parseIntDefault(queryValue(r, "Limit"), 16)
	if limit <= 0 || limit > 500 {
		limit = 16
	}
	var titles []titleInfo
	if ref, ok := s.ids.get(queryValue(r, "ParentId")); ok && ref.Kind == refView && ref.Ctx.ProfileID == p.Profile.ID {
		if libraryID, ok := strings.CutPrefix(ref.Ctx.ViewKey, localViewPrefix); ok {
			items := s.localViewChildren(r.Context(), p, u, libraryID)
			sortItems(items, []string{"DateCreated"}, "Descending")
			if len(items) > limit {
				items = items[:limit]
			}
			writeJSON(w, http.StatusOK, items)
			return
		}
		switch ref.Ctx.ViewKey {
		case viewLists, viewLiveTV:
		default:
			titles = s.viewTitles(r.Context(), p.Profile, ref.Ctx.ViewKey)
		}
	} else if queryValue(r, "ParentId") == "" {
		titles = append(titles, s.viewTitles(r.Context(), p.Profile, viewMovies)...)
		titles = append(titles, s.viewTitles(r.Context(), p.Profile, viewShows)...)
	}
	types := queryList(r, "IncludeItemTypes")
	out := make([]dto.BaseItemDto, 0, limit)
	for _, t := range titles {
		if len(out) >= limit {
			break
		}
		if len(types) > 0 && !containsFold(types, kindForMediaType(t.MediaType)) {
			continue
		}
		out = append(out, s.titleItem(u, t))
	}
	writeJSON(w, http.StatusOK, out)
}

// --- user data -----------------------------------------------------------

func (s *Server) handleFavorite(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		ref, ok := s.ids.get(pathVar(r, "id"))
		if ok && !s.kidsAllowsRef(r.Context(), p.Profile, ref) {
			writeError(w, http.StatusNotFound, "Item not found")
			return
		}
		if !ok || (ref.Kind != refMovie && ref.Kind != refSeries) || s.deps.Watchlist == nil {
			s.writeUserData(w, r, pathVar(r, "id"))
			return
		}
		t := titleFromRef(ref)
		var err error
		if add {
			// The profile's own watchlist gets metadata-derived identity, not
			// values another account put into the shared index.
			trusted := s.trustedTitle(r.Context(), t.MediaType, ref.Source, t)
			_, err = s.deps.Watchlist.AddOrUpdate(p.Profile.ID, models.WatchlistUpsert{
				ID: t.source(), MediaType: t.MediaType, Name: trusted.Name, Year: trusted.Year,
				PosterURL: firstNonEmpty(trusted.Poster, t.Poster), BackdropURL: firstNonEmpty(trusted.Backdrop, t.Backdrop),
				ExternalIDs: sourceExternalIDs(trusted),
			})
		} else {
			_, err = s.deps.Watchlist.Remove(p.Profile.ID, t.MediaType, t.source())
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.views.invalidate(p.Profile.ID)
		s.invalidateUserState(p.Profile.ID)
		s.writeUserData(w, r, pathVar(r, "id"))
		s.notifyUserData(r.Context(), p, pathVar(r, "id"))
	}
}

func (s *Server) handlePlayed(played bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		ref, ok := s.ids.get(pathVar(r, "id"))
		if ok && !s.kidsAllowsRef(r.Context(), p.Profile, ref) {
			writeError(w, http.StatusNotFound, "Item not found")
			return
		}
		if ok && s.deps.History != nil {
			s.markPlayed(r.Context(), p, ref, played)
			s.views.invalidate(p.Profile.ID)
			s.invalidateUserState(p.Profile.ID)
		}
		s.writeUserData(w, r, pathVar(r, "id"))
		if ok {
			s.notifyUserData(r.Context(), p, pathVar(r, "id"))
		}
	}
}

func (s *Server) markPlayed(ctx context.Context, p *principal, ref itemRef, played bool) {
	update := func(u models.WatchHistoryUpdate) {
		u.Watched = &played
		_, _ = s.deps.History.UpdateWatchHistory(p.Profile.ID, u)
	}
	switch ref.Kind {
	case refMovie:
		t := titleFromRef(ref)
		update(models.WatchHistoryUpdate{MediaType: "movie", ItemID: t.source(), Name: t.Name, Year: t.Year, ExternalIDs: sourceExternalIDs(t)})
	case refLocal:
		if mediaType, id, ok := localProgressIdentity(ref); ok {
			u := models.WatchHistoryUpdate{MediaType: mediaType, ItemID: id, Name: ref.Ctx.Name, Year: ref.Ctx.Year, ExternalIDs: ref.Ctx.ExternalIDs}
			if mediaType == "episode" {
				u.SeasonNumber, u.EpisodeNumber, u.SeriesName = ref.Ctx.Season, ref.Ctx.Episode, ref.Ctx.SeriesName
			}
			update(u)
		}
	case refEpisode:
		series := seriesInfoFromSeasonRef(ref)
		update(models.WatchHistoryUpdate{
			MediaType: "episode", ItemID: ref.Source, Name: ref.Ctx.Name, ExternalIDs: sourceExternalIDs(series),
			SeasonNumber: ref.Ctx.Season, EpisodeNumber: ref.Ctx.Episode, SeriesID: series.source(), SeriesName: series.Name,
		})
	case refSeason, refSeries:
		var series titleInfo
		season := -1
		if ref.Kind == refSeries {
			series = titleFromRef(ref)
		} else {
			series = seriesInfoFromSeasonRef(ref)
			season = ref.Ctx.Season
		}
		sr := s.seriesRefFrom(series)
		for _, item := range s.seriesEpisodes(ctx, nil, sr, season) {
			if eref, ok := s.ids.get(item.ID); ok {
				s.markPlayed(ctx, p, eref, played)
			}
		}
	}
}

func (s *Server) writeUserData(w http.ResponseWriter, r *http.Request, id string) {
	p := principalFrom(r)
	writeJSON(w, http.StatusOK, s.userDataFor(r.Context(), p, s.newUserState(p.Profile.ID), id))
}

// userDataFor is an item's current user data (favorite, played, position).
func (s *Server) userDataFor(ctx context.Context, p *principal, u *userState, id string) dto.UserItemData {
	if item, ok := s.itemByID(ctx, p, u, id, false); ok && item.UserData != nil {
		data := *item.UserData
		data.ItemID = normalizeID(id)
		return data
	}
	return dto.UserItemData{Key: normalizeID(id), ItemID: normalizeID(id)}
}

func (s *Server) handleUserData(w http.ResponseWriter, r *http.Request) {
	s.writeUserData(w, r, pathVar(r, "id"))
}

// --- empty-but-valid stubs ----------------------------------------------

func emptyQueryResult(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, dto.NewQueryResult[dto.BaseItemDto](nil, 0, 0))
}

func emptyArray(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func emptyObject(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleThemeMedia(w http.ResponseWriter, r *http.Request) {
	owner := normalizeID(pathVar(r, "id"))
	empty := map[string]any{"OwnerId": owner, "Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0}
	writeJSON(w, http.StatusOK, map[string]any{
		"ThemeVideosResult":     empty,
		"ThemeSongsResult":      empty,
		"SoundtrackSongsResult": empty,
	})
}

func (s *Server) handleItemFilters(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	q := parseItemQuery(r)
	genres := map[string]bool{}
	years := map[int]bool{}
	if q.ParentID != "" {
		for _, item := range s.childrenOf(r.Context(), p, s.newUserState(p.Profile.ID), q) {
			for _, g := range item.Genres {
				genres[g] = true
			}
			if item.ProductionYear != nil {
				years[*item.ProductionYear] = true
			}
		}
	}
	var genreList []string
	for g := range genres {
		genreList = append(genreList, g)
	}
	sort.Strings(genreList)
	var yearList []int
	for y := range years {
		yearList = append(yearList, y)
	}
	sort.Ints(yearList)
	genreItems := make([]dto.NameGuidPair, 0, len(genreList))
	for _, g := range genreList {
		genreItems = append(genreItems, dto.NameGuidPair{Name: g, ID: deriveID("genre", strings.ToLower(g))})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Genres": genreItems, "Tags": []string{}, "OfficialRatings": []string{}, "Years": yearList,
	})
}
