package jellyfinbridge

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/history"
)

// titleInfo is the bridge's normalized view of a movie or series, built from
// whichever mediastorm record is at hand (metadata Title, watchlist entry,
// progress row, ...).
type titleInfo struct {
	MediaType      string // movie | series
	ID             string // mediastorm title ID as given by the source
	Name           string
	Overview       string
	Year           int
	ExternalIDs    map[string]string
	Poster         string
	Backdrop       string
	Logo           string
	Genres         []string
	RuntimeMinutes int
	Certification  string
	ReleaseDate    string
	Rating         float64
	Status         string
	Network        string // series network, when the metadata knows it
	AddedAt        time.Time
	// trusted marks values from the metadata service (as opposed to
	// user-supplied watchlist/list records); only trusted values may replace
	// what the shared ID index already holds.
	trusted bool
}

func titleFromMetadata(t models.Title) titleInfo {
	info := titleInfo{
		MediaType:      normalizeMediaType(t.MediaType),
		ID:             t.ID,
		Name:           t.Name,
		Overview:       t.Overview,
		Year:           t.Year,
		ExternalIDs:    titleExternalIDs(t),
		Genres:         t.Genres,
		RuntimeMinutes: t.RuntimeMinutes,
		Certification:  t.Certification,
		ReleaseDate:    t.ReleaseDate,
		Status:         t.LifecycleStatus,
		Network:        strings.TrimSpace(t.Network),
		trusted:        true,
	}
	if t.Poster != nil {
		info.Poster = t.Poster.URL
	}
	if t.Backdrop != nil {
		info.Backdrop = t.Backdrop.URL
	}
	if t.Logo != nil {
		info.Logo = t.Logo.URL
	}
	for _, rating := range t.Ratings {
		if rating.Max > 0 && (rating.Source == "imdb" || rating.Source == "tmdb") {
			info.Rating = rating.Value / rating.Max * 10
			break
		}
	}
	return info
}

func titleFromWatchlist(wi models.WatchlistItem) titleInfo {
	return titleInfo{
		MediaType:      normalizeMediaType(wi.MediaType),
		ID:             wi.ID,
		Name:           wi.Name,
		Overview:       wi.Overview,
		Year:           wi.Year,
		ExternalIDs:    wi.ExternalIDs,
		Poster:         firstNonEmpty(wi.PosterURL, wi.TextPosterURL),
		Backdrop:       firstNonEmpty(wi.BackdropURL, wi.TextBackdropURL),
		Genres:         wi.Genres,
		RuntimeMinutes: wi.RuntimeMinutes,
		ReleaseDate:    wi.ReleaseDate,
		Status:         wi.LifecycleStatus,
		AddedAt:        wi.AddedAt,
	}
}

func titleExternalIDs(t models.Title) map[string]string {
	ids := map[string]string{}
	if t.TMDBID > 0 {
		ids["tmdb"] = strconv.FormatInt(t.TMDBID, 10)
	}
	if t.TVDBID > 0 {
		ids["tvdb"] = strconv.FormatInt(t.TVDBID, 10)
	}
	if t.IMDBID != "" {
		ids["imdb"] = t.IMDBID
	}
	return ids
}

func normalizeMediaType(mt string) string {
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "series", "tv", "show", "tvshow":
		return "series"
	case "episode":
		return "episode"
	default:
		return "movie"
	}
}

// source returns the canonical mediastorm ID the bridge keys this title by.
func (t titleInfo) source() string {
	if t.MediaType == "series" {
		return mediaidentity.CanonicalSeriesID(t.ID, t.ExternalIDs)
	}
	return mediaidentity.CanonicalTitleID("movie", t.ID, t.ExternalIDs)
}

func (t titleInfo) refKind() string {
	if t.MediaType == "series" {
		return refSeries
	}
	return refMovie
}

func (t titleInfo) context() refContext {
	return refContext{
		Name:        t.Name,
		Year:        t.Year,
		TitleID:     t.ID,
		ExternalIDs: t.ExternalIDs,
		Poster:      t.Poster,
		Backdrop:    t.Backdrop,
		Logo:        t.Logo,
	}
}

// titleFromRef rebuilds a titleInfo from a stored ref.
func titleFromRef(ref itemRef) titleInfo {
	mediaType := "movie"
	if ref.Kind == refSeries {
		mediaType = "series"
	}
	// The ref's source is its canonical identity (the GUID is derived from
	// it); IDs it encodes beat stored context, which other accounts' records
	// may have filled in.
	return titleInfo{
		MediaType:   mediaType,
		ID:          ref.Source,
		Name:        ref.Ctx.Name,
		Year:        ref.Ctx.Year,
		ExternalIDs: titleLevelIDs(mediaidentity.MergeExternalIDs(externalIDsFromSource(mediaType, ref.Source), ref.Ctx.ExternalIDs)),
		Poster:      ref.Ctx.Poster,
		Backdrop:    ref.Ctx.Backdrop,
		Logo:        ref.Ctx.Logo,
	}
}

// userState lazily loads a profile's watchlist, progress and watch history
// once per request, so building a page of items costs three lookups total.
type userState struct {
	profileID string
	s         *Server
	once      sync.Once
	favorites map[string]bool
	progress  map[string]models.PlaybackProgress
	watched   map[string]bool

	seriesOnce sync.Once
	series     *seriesWatch
	gen        uint64 // invalidation count the state was read at
}

func (s *Server) newUserState(profileID string) *userState {
	return &userState{profileID: profileID, s: s}
}

// userStateTTL bounds how stale favorite/played/progress flags may be when
// listing items; writes through the bridge invalidate immediately.
const userStateTTL = 10 * time.Second

type userStateEntry struct {
	at        time.Time
	gen       uint64
	favorites map[string]bool
	progress  map[string]models.PlaybackProgress
	watched   map[string]bool
}

type userStateCache struct {
	mu      sync.Mutex
	entries map[string]userStateEntry
	// gen counts invalidations per profile. A load that started before a
	// write must not cache what it read: it would bring the old state back
	// for the TTL (a list request racing a "mark played").
	gen map[string]uint64
}

// stateGen is the profile's current invalidation count.
func (s *Server) stateGen(profileID string) uint64 {
	s.states.mu.Lock()
	defer s.states.mu.Unlock()
	return s.states.gen[profileID]
}

func (s *Server) invalidateUserState(profileID string) {
	s.states.mu.Lock()
	delete(s.states.entries, profileID)
	if s.states.gen == nil {
		s.states.gen = map[string]uint64{}
	}
	s.states.gen[profileID]++
	s.states.mu.Unlock()
	s.seriesCounts.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), profileID+"|") {
			s.seriesCounts.Delete(k)
		}
		return true
	})
}

func (u *userState) load() {
	u.once.Do(func() {
		u.s.states.mu.Lock()
		if e, ok := u.s.states.entries[u.profileID]; ok && time.Since(e.at) < userStateTTL {
			u.s.states.mu.Unlock()
			u.favorites, u.progress, u.watched, u.gen = e.favorites, e.progress, e.watched, e.gen
			return
		}
		u.gen = u.s.states.gen[u.profileID]
		u.s.states.mu.Unlock()
		defer func() {
			u.s.states.mu.Lock()
			if u.s.states.entries == nil {
				u.s.states.entries = map[string]userStateEntry{}
			}
			// Written meanwhile: what this load read may be stale, so it is
			// used for this request only.
			if u.s.states.gen[u.profileID] == u.gen {
				u.s.states.entries[u.profileID] = userStateEntry{at: time.Now(), favorites: u.favorites, progress: u.progress, watched: u.watched, gen: u.gen}
			}
			u.s.states.mu.Unlock()
		}()
		u.favorites = map[string]bool{}
		u.progress = map[string]models.PlaybackProgress{}
		u.watched = map[string]bool{}
		if u.s.deps.Watchlist != nil {
			if items, err := u.s.deps.Watchlist.List(u.profileID); err == nil {
				for _, wi := range items {
					u.favorites[titleFromWatchlist(wi).key()] = true
				}
			}
		}
		if u.s.deps.History == nil {
			return
		}
		if rows, err := u.s.deps.History.ListPlaybackProgress(u.profileID); err == nil {
			for _, p := range rows {
				for _, key := range historyKeys(p.MediaType, p.ItemID, p.ExternalIDs, p.SeasonNumber, p.EpisodeNumber) {
					u.progress[key] = p
				}
			}
		}
		if rows, err := u.s.deps.History.ListWatchHistory(u.profileID); err == nil {
			for _, h := range rows {
				if h.Watched {
					for _, key := range historyKeys(h.MediaType, h.ItemID, h.ExternalIDs, h.SeasonNumber, h.EpisodeNumber) {
						u.watched[key] = true
					}
				}
			}
		}
	})
}

func (t titleInfo) key() string { return t.MediaType + ":" + t.source() }

// historyKeys are the user data keys a history row answers to. mediastorm
// stores an episode under one canonical series identity (TMDB when it knows
// one), while the bridge keys an episode by the series identity of the item
// it built, which can be the TVDB one when the TMDB ID is unknown there. An
// episode row therefore also answers to the TMDB and TVDB forms of its ID.
func historyKeys(mediaType, itemID string, ext map[string]string, season, episode int) []string {
	keys := []string{progressKey(mediaType, itemID, ext)}
	if normalizeMediaType(mediaType) != "episode" || episode <= 0 || season < 0 {
		return keys
	}
	for _, series := range []string{
		prefixedID("tmdb:tv:", ext["tmdb"]),
		prefixedID("tvdb:series:", ext["tvdb"]),
	} {
		if series == "" {
			continue
		}
		if key := "episode:" + strings.ToLower(mediaidentity.EpisodeID(series, season, episode)); key != keys[0] {
			keys = append(keys, key)
		}
	}
	return keys
}

func prefixedID(prefix, id string) string {
	if id = strings.TrimSpace(id); id == "" {
		return ""
	}
	return prefix + id
}

func progressKey(mediaType, itemID string, ext map[string]string) string {
	switch normalizeMediaType(mediaType) {
	case "episode":
		return "episode:" + strings.ToLower(strings.TrimSpace(itemID))
	case "series":
		return "series:" + mediaidentity.CanonicalSeriesID(itemID, ext)
	default:
		return "movie:" + mediaidentity.CanonicalTitleID("movie", itemID, ext)
	}
}

func (u *userState) userData(key, itemID string, runtimeTicks int64) *dto.UserItemData {
	u.load()
	data := &dto.UserItemData{Key: key, ItemID: itemID, IsFavorite: u.favorites[key]}
	if u.watched[key] {
		data.Played = true
		data.PlayCount = 1
	}
	if p, ok := u.progress[key]; ok && !p.HiddenFromContinueWatching {
		pct := p.PercentWatched
		if p.Position > 0 && pct < 90 {
			data.PlaybackPositionTicks = secondsToTicks(p.Position)
			data.PlayedPercentage = floatPtr(pct)
		}
		if pct >= 90 {
			data.Played = true
			data.PlayCount = max(data.PlayCount, 1)
		}
		updated := p.UpdatedAt.UTC()
		data.LastPlayedDate = &updated
	}
	return data
}

// --- item builders -------------------------------------------------------

func (s *Server) imageFields(item *dto.BaseItemDto, poster, backdrop, logo string) {
	tags := map[string]string{}
	if poster != "" {
		tags[dto.ImagePrimary] = imageTag(poster)
	}
	if logo != "" {
		tags[dto.ImageLogo] = imageTag(logo)
	}
	if backdrop != "" {
		tags[dto.ImageThumb] = imageTag(backdrop)
		item.BackdropImageTags = []string{imageTag(backdrop)}
	} else {
		item.BackdropImageTags = nil
	}
	item.ImageTags = tags
}

func (s *Server) storedImageFields(item *dto.BaseItemDto) {
	if ref, ok := s.ids.get(item.ID); ok {
		s.imageFields(item, ref.Ctx.Poster, ref.Ctx.Backdrop, ref.Ctx.Logo)
	}
}

func episodeImageFields(item *dto.BaseItemDto, ref itemRef) {
	item.ImageTags = imageTagsFor(imageURLFor(ref, "primary"))
	if ref.Ctx.Thumb != "" || ref.Ctx.Backdrop != "" {
		item.PrimaryImageAspectRatio = floatPtr(16.0 / 9.0)
	} else if ref.Ctx.Poster != "" {
		item.PrimaryImageAspectRatio = floatPtr(2.0 / 3.0)
	}
}

// titleItem builds a Movie or Series item and registers its GUID.
func (s *Server) titleItem(u *userState, t titleInfo) dto.BaseItemDto {
	src := t.source()
	put := s.ids.put
	if t.trusted {
		put = s.ids.putTrusted
	}
	id := put(t.refKind(), src, t.context())
	// Sparse list/history records only fill gaps in the shared index. The
	// tags must describe what the image endpoint actually serves from it.
	if ref, ok := s.ids.get(id); ok {
		t.Poster, t.Backdrop, t.Logo = ref.Ctx.Poster, ref.Ctx.Backdrop, ref.Ctx.Logo
	}
	item := dto.BaseItemDto{
		Name:           t.Name,
		ServerID:       s.serverID,
		ID:             id,
		Etag:           imageTag(src + t.Poster),
		SortName:       sortName(t.Name),
		Overview:       t.Overview,
		Genres:         t.Genres,
		GenreItems:     s.genreItems(t.Genres),
		Studios:        s.studioItems(t.Network),
		ProviderIDs:    providerIDs(t.ExternalIDs),
		PlayAccess:     "Full",
		LocationType:   dto.LocationFileSystem,
		CanDelete:      boolPtr(false),
		CanDownload:    boolPtr(false),
		OfficialRating: t.Certification,
	}
	if t.Year > 0 {
		item.ProductionYear = intPtr(t.Year)
	}
	if d := parseDate(t.ReleaseDate); d != nil {
		item.PremiereDate = d
	} else if t.Year > 0 {
		item.PremiereDate = timePtr(time.Date(t.Year, 1, 1, 0, 0, 0, 0, time.UTC))
	}
	if !t.AddedAt.IsZero() {
		item.DateCreated = timePtr(t.AddedAt.UTC())
	} else if item.PremiereDate != nil {
		item.DateCreated = item.PremiereDate
	}
	if t.Rating > 0 {
		item.CommunityRating = floatPtr(float64(int(t.Rating*10)) / 10)
	}
	s.imageFields(&item, t.Poster, t.Backdrop, t.Logo)
	item.PrimaryImageAspectRatio = floatPtr(2.0 / 3.0)
	var runtime int64
	if t.RuntimeMinutes > 0 {
		runtime = int64(t.RuntimeMinutes) * 60 * ticksPerSecond
	}
	if t.MediaType == "series" {
		item.Type = dto.KindSeries
		item.IsFolder = boolPtr(true)
		item.ChildCount = intPtr(1) // unknown until details load; Android TV hides Play at 0
		item.MediaType = dto.MediaTypeUnknown
		if t.Status != "" {
			item.Status = seriesStatus(t.Status)
		}
		item.DisplayPreferencesID = id
	} else {
		item.Type = dto.KindMovie
		item.IsFolder = boolPtr(false)
		item.MediaType = dto.MediaTypeVideo
		item.VideoType = "VideoFile"
		if runtime > 0 {
			item.RunTimeTicks = int64Ptr(runtime)
		}
	}
	if u != nil {
		item.UserData = u.userData(t.key(), id, runtime)
		if t.MediaType == "series" {
			s.seriesPlayState(u, seriesRef{Info: t, GUID: id, Src: src}, item.UserData)
		}
	}
	return item
}

func seriesStatus(status string) string {
	switch strings.ToLower(status) {
	case "ended", "canceled", "cancelled":
		return "Ended"
	default:
		return "Continuing"
	}
}

func providerIDs(ext map[string]string) map[string]string {
	if len(ext) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range ext {
		switch strings.ToLower(k) {
		case "tmdb":
			out["Tmdb"] = v
		case "tvdb":
			out["Tvdb"] = v
		case "imdb":
			out["Imdb"] = v
		}
	}
	return out
}

// seriesRef identifies a series when building its seasons and episodes.
type seriesRef struct {
	Info     titleInfo
	GUID     string
	Src      string
	Ordering string
}

// Inherited tags address the parent's GUID, so use that parent's artwork.
func (s *Server) seriesArtwork(series seriesRef) seriesRef {
	if ref, ok := s.ids.get(series.GUID); ok {
		series.Info.Poster = ref.Ctx.Poster
		series.Info.Backdrop = ref.Ctx.Backdrop
		series.Info.Logo = ref.Ctx.Logo
	}
	return series
}

func (s *Server) seriesRefFrom(t titleInfo) seriesRef {
	t.ExternalIDs = titleLevelIDs(t.ExternalIDs)
	src := t.source()
	return seriesRef{Info: t, Src: src, GUID: s.ids.put(refSeries, src, t.context())}
}

func seasonSource(seriesSrc string, number int) string {
	return seriesSrc + "/s" + strconv.Itoa(number)
}

func seasonRefSource(series seriesRef, number int) string {
	source := seasonSource(series.Src, number)
	ordering := strings.ToLower(strings.TrimSpace(series.Ordering))
	if ordering != "" && ordering != "official" && ordering != "default" {
		source += "/ordering/" + url.PathEscape(ordering)
	}
	return source
}

func episodeSource(series seriesRef, season, episode int) string {
	if id := history.EpisodeItemID(series.Src, series.Info.ExternalIDs, season, episode); id != "" {
		return strings.ToLower(id)
	}
	return strings.ToLower(mediaidentity.EpisodeID(series.Src, season, episode))
}

func seasonName(number int, name string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	if number == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", number)
}

func (s *Server) seasonItem(u *userState, series seriesRef, season models.SeriesSeason) dto.BaseItemDto {
	series = s.seriesArtwork(series)
	poster := series.Info.Poster
	if season.Image != nil && season.Image.URL != "" {
		poster = season.Image.URL
	}
	ctx := series.Info.context()
	ctx.Season = season.Number
	ctx.SeriesName = series.Info.Name
	ctx.SeriesPoster = series.Info.Poster
	ctx.SeasonOrdering = series.Ordering
	ctx.Poster = poster
	id := s.ids.put(refSeason, seasonRefSource(series, season.Number), ctx)
	if season.Image != nil && season.Image.URL != "" {
		// Episodes can seed this ref with the series poster before seasons
		// are listed. Real season metadata must replace that fallback.
		s.ids.putTrusted(refSeason, seasonRefSource(series, season.Number), refContext{Poster: poster})
	}
	if ref, ok := s.ids.get(id); ok {
		poster = imageURLFor(ref, "primary")
	}
	item := dto.BaseItemDto{
		Name:                    seasonName(season.Number, season.Name),
		ServerID:                s.serverID,
		ID:                      id,
		Type:                    dto.KindSeason,
		IsFolder:                boolPtr(true),
		MediaType:               dto.MediaTypeUnknown,
		Overview:                season.Overview,
		IndexNumber:             intPtr(season.Number),
		ParentID:                series.GUID,
		SeriesID:                series.GUID,
		SeriesName:              series.Info.Name,
		SeriesPrimaryImageTag:   imageTag(series.Info.Poster),
		LocationType:            dto.LocationFileSystem,
		PlayAccess:              "Full",
		SortName:                fmt.Sprintf("%04d", season.Number),
		ChildCount:              intPtr(max(season.EpisodeCount, len(season.Episodes))),
		DisplayPreferencesID:    id,
		PrimaryImageAspectRatio: floatPtr(2.0 / 3.0),
	}
	if poster != "" {
		item.ImageTags = map[string]string{dto.ImagePrimary: imageTag(poster)}
	}
	if series.Info.Backdrop != "" {
		item.ParentBackdropItemID = series.GUID
		item.ParentBackdropImageTags = []string{imageTag(series.Info.Backdrop)}
	}
	if series.Info.Logo != "" {
		item.ParentLogoItemID = series.GUID
		item.ParentLogoImageTag = imageTag(series.Info.Logo)
	}
	if u != nil {
		item.UserData = &dto.UserItemData{Key: seasonSource(series.Src, season.Number), ItemID: id}
		s.seasonPlayState(u, series, season, item.UserData)
	}
	return item
}

// episodeInfo is the normalized episode shape (from metadata or history).
type episodeInfo struct {
	Season         int
	Episode        int
	Name           string
	Overview       string
	Still          string
	RuntimeMinutes int
	AirDate        string
	// FromMetadata marks data from the metadata service (episode lists,
	// continue watching): its title and still replace what an earlier listing
	// stored, such as a "TBA" title from before the episode aired.
	FromMetadata bool
}

func episodeFromMetadata(ep models.SeriesEpisode) episodeInfo {
	info := episodeInfo{
		Season:         ep.SeasonNumber,
		Episode:        ep.EpisodeNumber,
		Name:           ep.Name,
		Overview:       ep.Overview,
		RuntimeMinutes: ep.Runtime,
		AirDate:        firstNonEmpty(ep.AiredDateTimeUTC, ep.AiredDate),
		FromMetadata:   true,
	}
	if ep.Image != nil {
		info.Still = ep.Image.URL
	}
	return info
}

func episodeFromReference(ref models.EpisodeReference) episodeInfo {
	info := episodeInfo{
		Season:         ref.SeasonNumber,
		Episode:        ref.EpisodeNumber,
		Name:           ref.Title,
		Overview:       ref.Overview,
		RuntimeMinutes: ref.RuntimeMinutes,
		AirDate:        firstNonEmpty(ref.AirDateTimeUTC, ref.AirDate),
		FromMetadata:   true,
	}
	if ref.Image != nil {
		info.Still = ref.Image.URL
	}
	return info
}

func (s *Server) episodeItem(u *userState, series seriesRef, ep episodeInfo) dto.BaseItemDto {
	series = s.seriesArtwork(series)
	src := episodeSource(series, ep.Season, ep.Episode)
	ctx := series.Info.context()
	ctx.SeriesName = series.Info.Name
	ctx.SeriesPoster = series.Info.Poster
	ctx.Season = ep.Season
	ctx.SeasonOrdering = series.Ordering
	ctx.Episode = ep.Episode
	ctx.Name = ep.Name
	ctx.Thumb = ep.Still
	ctx.Poster = firstNonEmpty(ep.Still, series.Info.Poster)
	id := s.ids.put(refEpisode, src, ctx)
	if ep.FromMetadata {
		s.ids.refreshEpisode(id, ep.Name, ep.Still)
	}
	seasonID := s.ids.put(refSeason, seasonRefSource(series, ep.Season), refContext{
		Name: seasonName(ep.Season, ""), TitleID: series.Info.ID, ExternalIDs: series.Info.ExternalIDs,
		SeriesName: series.Info.Name, Season: ep.Season, Poster: series.Info.Poster, SeriesPoster: series.Info.Poster, Backdrop: series.Info.Backdrop, SeasonOrdering: series.Ordering,
	})
	// Progress and history rows carry neither the still nor a current title
	// ("TBA" when written before the episode aired); what an earlier
	// listing stored for the episode fills in.
	name := ep.Name
	if stored, ok := s.ids.get(id); ok {
		if (name == "" || placeholderName(name)) && stored.Ctx.Name != "" && !placeholderName(stored.Ctx.Name) {
			name = stored.Ctx.Name
		}
	}
	if name == "" {
		name = fmt.Sprintf("Episode %d", ep.Episode)
	}
	item := dto.BaseItemDto{
		Name:                  name,
		ServerID:              s.serverID,
		ID:                    id,
		Type:                  dto.KindEpisode,
		IsFolder:              boolPtr(false),
		MediaType:             dto.MediaTypeVideo,
		VideoType:             "VideoFile",
		Overview:              ep.Overview,
		IndexNumber:           intPtr(ep.Episode),
		ParentIndexNumber:     intPtr(ep.Season),
		ParentID:              seasonID,
		SeasonID:              seasonID,
		SeasonName:            seasonName(ep.Season, ""),
		SeriesID:              series.GUID,
		SeriesName:            series.Info.Name,
		SeriesPrimaryImageTag: imageTag(series.Info.Poster),
		LocationType:          dto.LocationFileSystem,
		PlayAccess:            "Full",
		CanDelete:             boolPtr(false),
		CanDownload:           boolPtr(false),
		SortName:              fmt.Sprintf("%04d%04d", ep.Season, ep.Episode),
		ProviderIDs:           providerIDs(series.Info.ExternalIDs),
	}
	if d := parseDate(ep.AirDate); d != nil {
		item.PremiereDate = d
		item.DateCreated = d
		if y := d.Year(); y > 0 {
			item.ProductionYear = intPtr(y)
		}
	}
	var runtime int64
	if ep.RuntimeMinutes > 0 {
		runtime = int64(ep.RuntimeMinutes) * 60 * ticksPerSecond
		item.RunTimeTicks = int64Ptr(runtime)
	}
	if ref, ok := s.ids.get(id); ok {
		episodeImageFields(&item, ref)
	}
	if series.Info.Backdrop != "" {
		item.ParentBackdropItemID = series.GUID
		item.ParentBackdropImageTags = []string{imageTag(series.Info.Backdrop)}
		item.ParentThumbItemID = series.GUID
		item.ParentThumbImageTag = imageTag(series.Info.Backdrop)
	}
	if series.Info.Logo != "" {
		item.ParentLogoItemID = series.GUID
		item.ParentLogoImageTag = imageTag(series.Info.Logo)
	}
	if u != nil {
		item.UserData = u.userData("episode:"+src, id, runtime)
	}
	return item
}

// --- metadata lookups ----------------------------------------------------

func (t titleInfo) seriesQuery() models.SeriesDetailsQuery {
	q := models.SeriesDetailsQuery{TitleID: t.ID, Name: t.Name, Year: t.Year, IMDBID: t.ExternalIDs["imdb"]}
	q.TMDBID, _ = strconv.ParseInt(t.ExternalIDs["tmdb"], 10, 64)
	q.TVDBID, _ = strconv.ParseInt(t.ExternalIDs["tvdb"], 10, 64)
	return q
}

func (t titleInfo) movieQuery() models.MovieDetailsQuery {
	q := models.MovieDetailsQuery{TitleID: t.ID, Name: t.Name, Year: t.Year, IMDBID: t.ExternalIDs["imdb"]}
	q.TMDBID, _ = strconv.ParseInt(t.ExternalIDs["tmdb"], 10, 64)
	q.TVDBID, _ = strconv.ParseInt(t.ExternalIDs["tvdb"], 10, 64)
	return q
}

// enrich fetches full metadata for a title, keeping the input on failure.
func (s *Server) enrich(ctx context.Context, t titleInfo) titleInfo {
	if s.deps.Metadata == nil {
		return t
	}
	var full *models.Title
	var err error
	if t.MediaType == "series" {
		full, err = s.deps.Metadata.SeriesInfo(ctx, t.seriesQuery())
	} else {
		full, err = s.deps.Metadata.MovieDetails(ctx, t.movieQuery())
	}
	if err != nil || full == nil {
		return t
	}
	rich := titleFromMetadata(*full)
	rich.MediaType = t.MediaType
	if rich.ID == "" {
		rich.ID = t.ID
	}
	rich.ExternalIDs = mediaidentity.MergeExternalIDs(rich.ExternalIDs, t.ExternalIDs) // metadata wins
	rich.AddedAt = t.AddedAt
	// Metadata lookups can come back sparse (e.g. no TMDB key); never let
	// them erase what the listing source already knew.
	rich.Name = firstNonEmpty(rich.Name, t.Name)
	rich.Overview = firstNonEmpty(rich.Overview, t.Overview)
	rich.Poster = firstNonEmpty(rich.Poster, t.Poster)
	rich.Backdrop = firstNonEmpty(rich.Backdrop, t.Backdrop)
	rich.Logo = firstNonEmpty(rich.Logo, t.Logo)
	rich.ReleaseDate = firstNonEmpty(rich.ReleaseDate, t.ReleaseDate)
	rich.Certification = firstNonEmpty(rich.Certification, t.Certification)
	rich.Status = firstNonEmpty(rich.Status, t.Status)
	rich.Network = firstNonEmpty(rich.Network, t.Network)
	if rich.Year == 0 {
		rich.Year = t.Year
	}
	if rich.RuntimeMinutes == 0 {
		rich.RuntimeMinutes = t.RuntimeMinutes
	}
	if len(rich.Genres) == 0 {
		rich.Genres = t.Genres
	}
	if rich.Rating == 0 {
		rich.Rating = t.Rating
	}
	return rich
}

func (s *Server) seriesDetails(ctx context.Context, t titleInfo, ordering ...string) (*models.SeriesDetails, error) {
	if s.deps.Metadata == nil {
		return nil, fmt.Errorf("metadata unavailable")
	}
	query := t.seriesQuery()
	if len(ordering) > 0 {
		query.SeasonType = ordering[0]
	}
	return s.deps.Metadata.SeriesDetails(ctx, query)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// trustedTitle resolves a title's identity from its canonical source ID via
// the metadata service; fallback (shared ref context) only fills gaps when
// the lookup fails.
func (s *Server) trustedTitle(ctx context.Context, mediaType, source string, fallback titleInfo) titleInfo {
	t := titleInfo{MediaType: mediaType, ID: source, ExternalIDs: externalIDsFromSource(mediaType, source)}
	if len(t.ExternalIDs) == 0 {
		t.ExternalIDs = fallback.ExternalIDs
	}
	if source == "" {
		t.ID = fallback.ID
	}
	rich := s.enrich(ctx, t)
	if rich.Name == "" {
		rich.Name = fallback.Name
	}
	if rich.Year == 0 {
		rich.Year = fallback.Year
	}
	return rich
}

// externalIDsFromSource derives provider IDs from a canonical title ID such
// as "tmdb:movie:603", "tvdb:series:121361" or "imdb:tt0944947".
func externalIDsFromSource(mediaType, source string) map[string]string {
	source = strings.ToLower(strings.TrimSpace(source))
	ids := map[string]string{}
	switch {
	case strings.HasPrefix(source, "tmdb:movie:"), strings.HasPrefix(source, "tmdb:tv:"):
		ids["tmdb"] = source[strings.LastIndex(source, ":")+1:]
	case strings.HasPrefix(source, "tvdb:movie:"), strings.HasPrefix(source, "tvdb:series:"):
		ids["tvdb"] = source[strings.LastIndex(source, ":")+1:]
	case strings.HasPrefix(source, "imdb:"):
		ids["imdb"] = strings.TrimPrefix(source, "imdb:")
	case strings.HasPrefix(source, "tt"):
		ids["imdb"] = source
	}
	return ids
}

// sourceExternalIDs returns the title's provider IDs with the ones encoded
// in its canonical source ID taking precedence over stored context.
func sourceExternalIDs(t titleInfo) map[string]string {
	fromID := externalIDsFromSource(t.MediaType, t.ID)
	return titleLevelIDs(mediaidentity.MergeExternalIDs(fromID, mediaidentity.MergeExternalIDs(externalIDsFromSource(t.MediaType, t.source()), t.ExternalIDs)))
}

// titleLevelIDs drops episode-scoped IDs (episodeTmdb, episodeTvdb,
// absoluteEpisode, ...) from a title's external IDs. History rows carry them
// for their episode; copied into a series they would make every episode of
// it look like that one to mediastorm, which then stores a different
// episode's watched flag in that episode's row.
func titleLevelIDs(ext map[string]string) map[string]string {
	if len(ext) == 0 {
		return ext
	}
	out := make(map[string]string, len(ext))
	for key, value := range ext {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "episode") || strings.HasPrefix(lower, "absoluteepisode") {
			continue
		}
		out[key] = value
	}
	return out
}
