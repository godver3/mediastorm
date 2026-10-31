package jellyfinbridge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/services/playback"
)

// Local (and Plex/Jellyfin-synced) libraries are real files with a fixed
// stream path, so they become ordinary Jellyfin libraries. Everything goes
// through mediastorm's library handlers in-process, which apply per-library
// access policy for the caller's account and profile.

const (
	refLocalSeries  = "localseries"
	refLocalSeason  = "localseason"
	localViewPrefix = "lib:"
)

type localLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // movie | show | other
}

type localGroupCache struct {
	mu      sync.Mutex
	entries map[string]localGroupEntry
}

type localGroupEntry struct {
	at     time.Time
	groups []models.LocalMediaItemGroup
}

var localGroups = &localGroupCache{entries: map[string]localGroupEntry{}}

func (s *Server) localLibraries(ctx context.Context, p *principal) []localLibrary {
	if s.deps.Handlers.LocalLibraries == nil {
		return nil
	}
	q := url.Values{"profileId": {p.Profile.ID}}
	var libs []localLibrary
	if _, err := callJSON(s.deps.Handlers.LocalLibraries, internalRequest(ctx, p, http.MethodGet, "/api/library/libraries?"+q.Encode(), nil, nil), &libs); err != nil {
		return nil
	}
	out := libs[:0]
	for _, lib := range libs {
		if lib.Type == "movie" || lib.Type == "show" {
			out = append(out, lib)
		}
	}
	return out
}

func (s *Server) localLibraryGroups(ctx context.Context, p *principal, libraryID string) []models.LocalMediaItemGroup {
	key := p.Profile.ID + "|" + libraryID
	localGroups.mu.Lock()
	if e, ok := localGroups.entries[key]; ok && time.Since(e.at) < viewCacheTTL {
		localGroups.mu.Unlock()
		return e.groups
	}
	localGroups.mu.Unlock()
	if s.deps.Handlers.LocalGroups == nil {
		return nil
	}
	q := url.Values{"profileId": {p.Profile.ID}, "limit": {"100000"}, "offset": {"0"}}
	var res models.LocalMediaGroupListResult
	req := internalRequest(ctx, p, http.MethodGet, "/api/library/libraries/"+url.PathEscape(libraryID)+"/groups?"+q.Encode(), nil, map[string]string{"libraryID": libraryID})
	if _, err := callJSON(s.deps.Handlers.LocalGroups, req, &res); err != nil {
		return nil
	}
	localGroups.mu.Lock()
	for k, e := range localGroups.entries {
		if time.Since(e.at) > viewCacheTTL {
			delete(localGroups.entries, k)
		}
	}
	localGroups.entries[key] = localGroupEntry{at: time.Now(), groups: res.Groups}
	localGroups.mu.Unlock()
	return res.Groups
}

func (s *Server) findLocalGroup(ctx context.Context, p *principal, libraryID, groupID string) (models.LocalMediaItemGroup, bool) {
	for _, g := range s.localLibraryGroups(ctx, p, libraryID) {
		if g.ID == groupID {
			return g, true
		}
	}
	return models.LocalMediaItemGroup{}, false
}

func groupImage(primary, text *models.Image) string {
	if primary != nil && primary.URL != "" {
		return primary.URL
	}
	if text != nil {
		return text.URL
	}
	return ""
}

func groupExternalIDs(g models.LocalMediaItemGroup) map[string]string {
	ids := map[string]string{}
	if g.TMDBID > 0 {
		ids["tmdb"] = strconv.FormatInt(g.TMDBID, 10)
	}
	if g.TVDBID > 0 {
		ids["tvdb"] = strconv.FormatInt(g.TVDBID, 10)
	}
	if g.IMDBID != "" {
		ids["imdb"] = g.IMDBID
	}
	return ids
}

// localMovieItem builds a Movie from a movie-library group (first file wins).
func (s *Server) localMovieItem(u *userState, libraryID string, g models.LocalMediaItemGroup) (dto.BaseItemDto, bool) {
	if len(g.Items) == 0 {
		return dto.BaseItemDto{}, false
	}
	file := g.Items[0]
	t := titleInfo{
		MediaType: "movie", ID: firstNonEmpty(file.MatchedTitleID, "localmedia:"+file.ID), Name: firstNonEmpty(g.Title, file.DetectedTitle, file.FileName),
		Overview: g.Overview, Year: g.Year, ExternalIDs: groupExternalIDs(g), Certification: g.Certification,
		Poster: groupImage(g.Poster, g.TextPoster), Backdrop: groupImage(g.Backdrop, g.TextBackdrop),
	}
	if g.LatestCreatedAt != nil {
		t.AddedAt = *g.LatestCreatedAt
	}
	if file.Probe != nil && file.Probe.DurationSeconds > 0 {
		t.RuntimeMinutes = int(file.Probe.DurationSeconds / 60)
	}
	ctx := t.context()
	ctx.Extra = libraryID
	id := s.ids.putTrusted(refLocal, file.ID, ctx)
	item := s.titleItem(u, t)
	// titleItem registered the catalog identity; point this item at the file.
	item.ID = id
	s.storedImageFields(&item)
	if item.UserData != nil {
		item.UserData.ItemID = id
	}
	item.Container = strings.TrimPrefix(strings.ToLower(fileExt(file.FileName)), ".")
	return item, true
}

func fileExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

func (s *Server) localSeriesInfo(g models.LocalMediaItemGroup) titleInfo {
	t := titleInfo{
		MediaType: "series", ID: "localseries:" + g.ID, Name: g.Title, Overview: g.Overview, Year: g.Year,
		ExternalIDs: groupExternalIDs(g), Certification: g.Certification,
		Poster: groupImage(g.Poster, g.TextPoster), Backdrop: groupImage(g.Backdrop, g.TextBackdrop),
	}
	if g.LatestCreatedAt != nil {
		t.AddedAt = *g.LatestCreatedAt
	}
	return t
}

// localSeriesItem builds a Series for a show-library group.
func (s *Server) localSeriesItem(u *userState, libraryID string, g models.LocalMediaItemGroup) dto.BaseItemDto {
	t := s.localSeriesInfo(g)
	ctx := t.context()
	ctx.Extra = libraryID
	ctx.ViewKey = g.ID
	id := s.ids.putTrusted(refLocalSeries, libraryID+"/"+g.ID, ctx)
	item := s.titleItem(u, t)
	item.ID = id
	s.storedImageFields(&item)
	item.DisplayPreferencesID = id
	item.ChildCount = intPtr(len(g.Seasons))
	if item.UserData != nil {
		item.UserData.ItemID = id
	}
	return item
}

func (s *Server) localSeriesRef(libraryID string, g models.LocalMediaItemGroup) seriesRef {
	t := s.localSeriesInfo(g)
	ctx := t.context()
	ctx.Extra = libraryID
	ctx.ViewKey = g.ID
	return seriesRef{Info: t, Src: libraryID + "/" + g.ID, GUID: s.ids.putTrusted(refLocalSeries, libraryID+"/"+g.ID, ctx)}
}

func (s *Server) localSeasonItems(u *userState, libraryID string, g models.LocalMediaItemGroup) []dto.BaseItemDto {
	series := s.localSeriesRef(libraryID, g)
	var out []dto.BaseItemDto
	for _, season := range g.Seasons {
		ctx := series.Info.context()
		ctx.Extra = libraryID
		ctx.ViewKey = g.ID
		ctx.Season = season.SeasonNumber
		ctx.SeriesName = series.Info.Name
		ctx.SeriesPoster = series.Info.Poster
		id := s.ids.putTrusted(refLocalSeason, series.Src+"/s"+strconv.Itoa(season.SeasonNumber), ctx)
		item := s.seasonItem(u, series, models.SeriesSeason{Number: season.SeasonNumber, EpisodeCount: len(season.Episodes)})
		item.ID = id
		s.storedImageFields(&item)
		item.DisplayPreferencesID = id
		out = append(out, item)
	}
	return out
}

func (s *Server) localEpisodeItems(u *userState, libraryID string, g models.LocalMediaItemGroup, seasonNumber int) []dto.BaseItemDto {
	series := s.localSeriesRef(libraryID, g)
	// Direct episode requests must register the local season IDs they expose.
	s.localSeasonItems(nil, libraryID, g)
	var out []dto.BaseItemDto
	for _, season := range g.Seasons {
		if seasonNumber >= 0 && season.SeasonNumber != seasonNumber {
			continue
		}
		seasonID := deriveID(refLocalSeason, series.Src+"/s"+strconv.Itoa(season.SeasonNumber))
		for _, ep := range season.Episodes {
			if len(ep.Items) == 0 {
				continue
			}
			file := ep.Items[0]
			info := episodeInfo{Season: season.SeasonNumber, Episode: ep.EpisodeNumber, Name: ep.EpisodeTitle, Overview: ep.EpisodeOverview}
			if ep.EpisodeImage != nil {
				info.Still = ep.EpisodeImage.URL
			}
			if file.Probe != nil && file.Probe.DurationSeconds > 0 {
				info.RuntimeMinutes = int(file.Probe.DurationSeconds / 60)
			}
			item := s.episodeItem(u, series, info)
			ctx := series.Info.context()
			ctx.Extra = libraryID
			ctx.ViewKey = g.ID
			ctx.Season, ctx.Episode, ctx.Name, ctx.Thumb = info.Season, info.Episode, info.Name, info.Still
			ctx.SeriesName = series.Info.Name
			ctx.SeriesPoster = series.Info.Poster
			id := s.ids.putTrusted(refLocal, file.ID, ctx)
			item.ID = id
			if ref, ok := s.ids.get(id); ok {
				episodeImageFields(&item, ref)
			}
			item.SeasonID = seasonID
			item.ParentID = seasonID
			if item.UserData != nil {
				item.UserData.ItemID = id
			}
			out = append(out, item)
		}
	}
	return out
}

// localViewChildren lists a library's movies or shows.
func (s *Server) localViewChildren(ctx context.Context, p *principal, u *userState, libraryID string) []dto.BaseItemDto {
	var libType string
	for _, lib := range s.localLibraries(ctx, p) {
		if lib.ID == libraryID {
			libType = lib.Type
		}
	}
	if libType == "" {
		return nil
	}
	var out []dto.BaseItemDto
	for _, g := range s.localLibraryGroups(ctx, p, libraryID) {
		if libType == "show" {
			out = append(out, s.localSeriesItem(u, libraryID, g))
			continue
		}
		if item, ok := s.localMovieItem(u, libraryID, g); ok {
			out = append(out, item)
		}
	}
	return out
}

func localViewDefs(libs []localLibrary) []viewDef {
	defs := make([]viewDef, 0, len(libs))
	for _, lib := range libs {
		ct := dto.CollectionMovies
		if lib.Type == "show" {
			ct = dto.CollectionTVShows
		}
		defs = append(defs, viewDef{Key: localViewPrefix + lib.ID, Name: lib.Name, CollectionType: ct, Type: dto.KindCollectionFolder})
	}
	return defs
}

// localItemByRef rebuilds a local movie/episode/series/season item.
func (s *Server) localItemByRef(ctx context.Context, p *principal, u *userState, ref itemRef) (dto.BaseItemDto, bool) {
	libraryID := ref.Ctx.Extra
	switch ref.Kind {
	case refLocalSeries:
		if g, ok := s.findLocalGroup(ctx, p, libraryID, ref.Ctx.ViewKey); ok {
			return s.localSeriesItem(u, libraryID, g), true
		}
	case refLocalSeason:
		if g, ok := s.findLocalGroup(ctx, p, libraryID, ref.Ctx.ViewKey); ok {
			for _, item := range s.localSeasonItems(u, libraryID, g) {
				if item.IndexNumber != nil && *item.IndexNumber == ref.Ctx.Season {
					return item, true
				}
			}
		}
	case refLocal:
		groups := s.localLibraryGroups(ctx, p, libraryID)
		for _, g := range groups {
			if ref.Ctx.ViewKey != "" { // episode
				if g.ID != ref.Ctx.ViewKey {
					continue
				}
				for _, item := range s.localEpisodeItems(u, libraryID, g, ref.Ctx.Season) {
					if item.ID == deriveID(refLocal, ref.Source) {
						return item, true
					}
				}
				continue
			}
			if len(g.Items) > 0 && g.Items[0].ID == ref.Source {
				return s.localMovieItem(u, libraryID, g)
			}
		}
	}
	return dto.BaseItemDto{}, false
}

// --- playback ----------------------------------------------------------------

type localPlaybackResponse struct {
	StreamPath      string  `json:"streamPath"`
	DurationSeconds float64 `json:"durationSeconds"`
	DisplayName     string  `json:"displayName"`
	FileName        string  `json:"fileName"`
}

func (s *Server) localPlaybackInfo(w http.ResponseWriter, r *http.Request, p *principal, itemID string, ref itemRef, req playbackInfoRequest) {
	q := url.Values{"profileId": {p.Profile.ID}, "profileName": {p.Profile.Name}}
	var resp localPlaybackResponse
	callReq := internalRequest(r.Context(), p, http.MethodGet, "/api/library/items/"+url.PathEscape(ref.Source)+"/playback?"+q.Encode(), nil, map[string]string{"itemID": ref.Source})
	if _, err := callJSON(s.deps.Handlers.LocalItemPlayback, callReq, &resp); err != nil || resp.StreamPath == "" {
		writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{}, ErrorCode: "NoCompatibleStream"})
		return
	}
	status := &playback.PrequeueStatusResponse{
		Status:                playback.PrequeueStatusReady,
		StreamPath:            resp.StreamPath,
		DisplayName:           firstNonEmpty(resp.FileName, resp.DisplayName),
		Duration:              resp.DurationSeconds,
		SelectedAudioTrack:    -1,
		SelectedSubtitleTrack: -1,
	}
	sess := &playSession{
		ID:            randomHex(16),
		MediaSourceID: s.mediaSourceID(p.Profile.ID, p.Client.DeviceID, itemID),
		ItemID:        itemID,
		ProfileID:     p.Profile.ID,
		DeviceID:      p.Client.DeviceID,
		Ref:           ref,
		owner:         p,
		status:        status,
		duration:      resp.DurationSeconds,
		hls:           map[string]string{},
		hlsFiles:      map[string]string{},
		hlsOwners:     map[string]bool{},
	}
	s.plays.add(sess)
	s.startThumbnails(p, sess, status)
	source := s.vodMediaSource(r, p, sess, status, req)
	writeJSON(w, http.StatusOK, dto.PlaybackInfoResponse{MediaSources: []dto.MediaSourceInfo{source}, PlaySessionID: sess.ID})
}

// localProgressIdentity maps a local item to the catalog identity mediastorm
// tracks progress under, when the file was matched to metadata.
func localProgressIdentity(ref itemRef) (mediaType, itemID string, ok bool) {
	titleID := ref.Ctx.TitleID
	if ref.Ctx.ViewKey != "" { // episode of a local show
		if len(ref.Ctx.ExternalIDs) == 0 {
			return "", "", false
		}
		series := titleInfo{MediaType: "series", ID: titleID, ExternalIDs: ref.Ctx.ExternalIDs}
		return "episode", strings.ToLower(fmt.Sprintf("%s:s%02de%02d", series.source(), ref.Ctx.Season, ref.Ctx.Episode)), true
	}
	// Unmatched files and Plex/Jellyfin library items carry a localmedia: ID;
	// the latter still have catalog external IDs to track progress under.
	id := titleInfo{MediaType: "movie", ID: titleID, ExternalIDs: ref.Ctx.ExternalIDs}.source()
	if strings.HasPrefix(id, "localmedia:") {
		return "", "", false
	}
	return "movie", id, true
}
