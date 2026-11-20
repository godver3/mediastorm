package jellyfinbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// --- similar items -------------------------------------------------------

const similarLimit = 12

// handleSimilar serves /Items/{id}/Similar ("More like this") from TMDB
// similar titles, with the same kids-profile rules as search and trending.
func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	empty := dto.NewQueryResult[dto.BaseItemDto](nil, 0, 0)
	ref, ok := s.ids.get(normalizeID(pathVar(r, "id")))
	if !ok || s.deps.Metadata == nil || isCuratedKidsProfile(p.Profile) || !s.kidsAllowsRef(r.Context(), p.Profile, ref) {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	var t titleInfo
	switch ref.Kind {
	case refMovie, refSeries:
		t = titleFromRef(ref)
	case refSeason, refEpisode:
		t = seriesInfoFromSeasonRef(ref)
	default:
		writeJSON(w, http.StatusOK, empty)
		return
	}
	tmdbID, _ := strconv.ParseInt(sourceExternalIDs(t)["tmdb"], 10, 64)
	if tmdbID <= 0 {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	similar, err := s.deps.Metadata.Similar(r.Context(), t.MediaType, tmdbID)
	if err != nil || len(similar) == 0 {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	limit := parseIntDefault(queryValue(r, "Limit"), similarLimit)
	if limit <= 0 || limit > 50 {
		limit = similarLimit
	}
	if len(similar) > limit {
		similar = similar[:limit]
	}
	if p.Profile.IsKidsProfile {
		similar = append([]models.Title(nil), similar...)
		s.deps.Metadata.EnrichTitleCertifications(r.Context(), similar)
	}
	titles := make([]titleInfo, 0, len(similar))
	for _, st := range similar {
		info := titleFromMetadata(st)
		info.MediaType = t.MediaType
		titles = append(titles, info)
	}
	titles = s.filterForProfile(r.Context(), p.Profile, titles)
	u := s.newUserState(p.Profile.ID)
	items := make([]dto.BaseItemDto, 0, len(titles))
	for _, info := range titles {
		items = append(items, s.titleItem(u, info))
	}
	writeJSON(w, http.StatusOK, dto.NewQueryResult(items, len(items), 0))
}

// --- upcoming episodes ---------------------------------------------------

const upcomingDays = 30

// handleUpcoming serves /Shows/Upcoming: episodes of the profile's own shows
// (watchlist and watch history) airing in the next weeks, from mediastorm's
// release calendar. The calendar is read from its cache only.
func (s *Server) handleUpcoming(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	q := itemQuery{StartIndex: parseIntDefault(queryValue(r, "StartIndex"), 0), Limit: parseIntDefault(queryValue(r, "Limit"), 0)}
	if s.deps.Calendar == nil || isCuratedKidsProfile(p.Profile) {
		writeJSON(w, http.StatusOK, s.page(nil, q))
		return
	}
	now := time.Now()
	type upcoming struct {
		series titleInfo
		ep     episodeInfo
		at     time.Time
	}
	var rows []upcoming
	for _, c := range s.deps.Calendar.GetForHomeShelf(p.Profile.ID, time.Local, 0, upcomingDays, 200) {
		if normalizeMediaType(c.MediaType) != "series" || c.SeasonNumber <= 0 || c.EpisodeNumber <= 0 ||
			(c.Source != "watchlist" && c.Source != "history") {
			continue
		}
		at := calendarAirTime(c)
		if at.IsZero() || at.Before(now.Add(-12*time.Hour)) {
			continue
		}
		rows = append(rows, upcoming{
			series: titleInfo{
				MediaType:   "series",
				Name:        c.Title,
				Year:        c.Year,
				ExternalIDs: c.ExternalIDs,
				Poster:      firstNonEmpty(c.PosterURL, c.TextPosterURL),
				Backdrop:    firstNonEmpty(c.BackdropURL, c.TextBackdropURL),
				Genres:      c.Genres,
			},
			ep: episodeInfo{
				Season:         c.SeasonNumber,
				Episode:        c.EpisodeNumber,
				Name:           c.EpisodeTitle,
				Overview:       c.EpisodeOverview,
				RuntimeMinutes: c.RuntimeMinutes,
				AirDate:        at.UTC().Format(time.RFC3339),
			},
			at: at,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })

	seriesTitles := make([]titleInfo, len(rows))
	for i, row := range rows {
		seriesTitles[i] = row.series
	}
	allowed := map[string]bool{}
	for _, t := range s.filterForProfile(r.Context(), p.Profile, seriesTitles) {
		allowed[t.key()] = true
	}
	u := s.newUserState(p.Profile.ID)
	items := []dto.BaseItemDto{}
	for _, row := range rows {
		if row.series.source() == "" || !allowed[row.series.key()] {
			continue
		}
		items = append(items, s.episodeItem(u, s.seriesRefFrom(row.series), row.ep))
	}
	writeJSON(w, http.StatusOK, s.page(items, q))
}

// calendarAirTime is the item's air time; the shelf query already converted
// AirDate/AirTime into the local zone.
func calendarAirTime(c models.CalendarItem) time.Time {
	day, err := time.ParseInLocation("2006-01-02", c.AirDate, time.Local)
	if err != nil {
		return time.Time{}
	}
	if c.AirTime != "" {
		if clock, err := time.ParseInLocation("2006-01-02 15:04", c.AirDate+" "+c.AirTime, time.Local); err == nil {
			return clock
		}
	}
	return day
}

// --- media segments (skip intro / credits) -------------------------------

// segmentTypes maps mediastorm skip-segment types onto Jellyfin's
// MediaSegmentType.
var segmentTypes = map[string]string{
	"intro":   "Intro",
	"recap":   "Recap",
	"outro":   "Outro",
	"preview": "Preview",
}

// mightHaveSegments reports whether a stream is worth a segment lookup:
// episodes (IntroDB/SkipDB are keyed by series and episode) and anything with
// embedded chapters. jellyfin-web only asks when HasSegments is set.
func mightHaveSegments(sess *playSession, probe *mediaProbe) bool {
	if _, ok := episodeSeries(sess.Ref); ok {
		return true
	}
	return probe != nil && len(probe.Chapters) > 0
}

// episodeSeries returns the series of an episode, streamed or from a local
// library (whose episodes carry the matched series IDs).
func episodeSeries(ref itemRef) (titleInfo, bool) {
	if ref.Ctx.Season <= 0 || ref.Ctx.Episode <= 0 {
		return titleInfo{}, false
	}
	switch {
	case ref.Kind == refEpisode:
		return seriesInfoFromSeasonRef(ref), true
	case ref.Kind == refLocal && ref.Ctx.ViewKey != "" && len(ref.Ctx.ExternalIDs) > 0:
		return titleInfo{MediaType: "series", ID: ref.Ctx.TitleID, ExternalIDs: ref.Ctx.ExternalIDs}, true
	}
	return titleInfo{}, false
}

// handleMediaSegments serves /MediaSegments/{id}. jellyfin-web asks with the
// MediaSourceId, Android TV with the item ID.
func (s *Server) handleMediaSegments(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	id := normalizeID(pathVar(r, "id"))
	sess, ok := s.playByMediaSource(id)
	if !ok {
		sess, ok = s.plays.latestForItem(p.Profile.ID, id, p.Client.DeviceID, "")
	}
	if !ok || sess.ProfileID != p.Profile.ID {
		writeJSON(w, http.StatusOK, dto.NewQueryResult[dto.MediaSegmentDto](nil, 0, 0))
		return
	}
	segments := s.mediaSegments(r.Context(), p, sess)
	if want := queryList(r, "includeSegmentTypes"); len(want) > 0 {
		filtered := segments[:0:0]
		for _, seg := range segments {
			if containsFold(want, seg.Type) {
				filtered = append(filtered, seg)
			}
		}
		segments = filtered
	}
	writeJSON(w, http.StatusOK, dto.NewQueryResult(segments, len(segments), 0))
}

// mediaSegments resolves the play session's skip segments once through
// mediastorm's resolver (IntroDB, then SkipDB, then chapter names).
func (s *Server) mediaSegments(ctx context.Context, p *principal, sess *playSession) []dto.MediaSegmentDto {
	sess.mu.Lock()
	cached, done := sess.segments, sess.segmentsDone
	status, probe, duration := sess.status, sess.probe, sess.duration
	sess.mu.Unlock()
	if done {
		return cached
	}
	if sess.Ref.Kind == refRecording {
		segments, found := s.recordingSegments(ctx, p, sess)
		if found {
			sess.mu.Lock()
			sess.segments, sess.segmentsDone = segments, true
			sess.mu.Unlock()
		}
		return segments
	}
	if s.deps.Handlers.SkipSegments == nil {
		return []dto.MediaSegmentDto{}
	}
	if probe == nil {
		probe = s.probeMedia(ctx, p, sess, status)
	}

	body := map[string]any{"duration": duration}
	if series, ok := episodeSeries(sess.Ref); ok {
		imdb := sourceExternalIDs(series)["imdb"]
		if imdb == "" && series.source() != "" {
			imdb = s.enrich(ctx, series).ExternalIDs["imdb"]
		}
		if imdb != "" {
			body["imdbId"] = imdb
			body["season"] = sess.Ref.Ctx.Season
			body["episode"] = sess.Ref.Ctx.Episode
		}
	}
	if probe != nil && len(probe.Chapters) > 0 {
		body["chapters"] = probe.Chapters
	}
	raw, _ := json.Marshal(body)
	req := internalRequest(ctx, p, http.MethodPost, "/api/video/segments", raw, nil)
	req.Header.Set("Content-Type", "application/json")
	var resp struct {
		Segments []struct {
			Type  string  `json:"type"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"segments"`
	}
	if _, err := callJSON(s.deps.Handlers.SkipSegments, req, &resp); err != nil {
		// Do not cache a failure: the next playback start may succeed.
		return []dto.MediaSegmentDto{}
	}
	segments := []dto.MediaSegmentDto{}
	for _, seg := range resp.Segments {
		kind, ok := segmentTypes[strings.ToLower(seg.Type)]
		if !ok || seg.End <= seg.Start {
			continue
		}
		segments = append(segments, dto.MediaSegmentDto{
			ID:         deriveID("segment", sess.ItemID+"/"+kind+"/"+strconv.FormatFloat(seg.Start, 'f', 3, 64)),
			ItemID:     sess.ItemID,
			Type:       kind,
			StartTicks: secondsToTicks(seg.Start),
			EndTicks:   secondsToTicks(seg.End),
		})
	}
	sess.mu.Lock()
	sess.segments, sess.segmentsDone = segments, true
	sess.mu.Unlock()
	return segments
}
