package jellyfinbridge

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Quality preset versions: besides "Auto" and the individual releases, the
// version picker offers the quality presets the admin chose for pickers
// ("Auto · German HD"). Such a version plays like Auto (prequeue, and with
// Auto: Avoid Video Transcoding the walk down the releases), ranked by that
// preset instead of the one the profile and device use. mediastorm decides
// per title which presets fit (anime presets only on anime) and which one
// Auto uses, so the choices are fetched per item.

const (
	presetChoicesTTL = 2 * time.Minute
	// A failed lookup is cached too, but retried sooner.
	presetChoicesRetry = 15 * time.Second
	maxPresetChoices   = 5000
	// presetLookupWait bounds the preset lookup while listing an item's
	// versions.
	presetLookupWait = 3 * time.Second
	// presetRememberSettle covers mediastorm's background remember of a
	// picked preset: usually done within the first wait, at worst (a 10s
	// default lookup, then a write of up to 10s) within the second.
	presetRememberSettle     = 12 * time.Second
	presetRememberSettleLate = 25 * time.Second
	// presetsInUseTTL keeps whether presets are offered at all for a few
	// seconds: item requests ask several times, and asking reads settings.
	presetsInUseTTL = 5 * time.Second
)

// presetsInUse reports whether mediastorm offers quality presets in version
// pickers.
func (s *Server) presetsInUse() bool {
	if s.deps.QualityPresetsInUse == nil {
		return true
	}
	c := &s.presetChoices
	c.mu.Lock()
	if time.Since(c.inUseAt) < presetsInUseTTL {
		inUse := c.inUse
		c.mu.Unlock()
		return inUse
	}
	c.mu.Unlock()
	inUse := s.deps.QualityPresetsInUse()
	c.mu.Lock()
	c.inUse, c.inUseAt = inUse, time.Now()
	c.mu.Unlock()
	return inUse
}

// presetChoice is a quality preset offered in version pickers.
type presetChoice struct {
	ID        string `json:"id"` // preset ID or "simple"
	Name      string `json:"name"`
	MediaType string `json:"mediaType"` // movie, series, or "" for both
}

// presetSelection is the preset Auto uses for an item ("" = simple ranking).
type presetSelection struct {
	PresetID string `json:"presetId"`
	Name     string `json:"name"`
	Source   string `json:"source"`
}

// itemPresets are the picker choices and Auto's preset for one item.
type itemPresets struct {
	offered []presetChoice
	auto    presetSelection
	// def is Auto's preset without the one remembered for the title;
	// playing it clears the memory. Source is "" when unknown.
	def     presetSelection
	fetched time.Time
	gen     uint64 // ranking generation it was fetched under
}

type presetChoiceCache struct {
	mu    sync.Mutex
	items map[string]itemPresets // profile|device|item
	// inUse is presetsInUse's last answer, from inUseAt.
	inUse   bool
	inUseAt time.Time
}

// presetsForItem returns the presets offered for an item and the one Auto
// uses, cached briefly per profile, device and item.
func (s *Server) presetsForItem(ctx context.Context, p *principal, itemID string, ref itemRef) itemPresets {
	if s.deps.Handlers.QualityPresets == nil || (ref.Kind != refMovie && ref.Kind != refEpisode) || !s.presetsInUse() {
		return itemPresets{}
	}
	key := deviceKey(p) + "|" + itemID
	c := &s.presetChoices
	c.mu.Lock()
	gen := s.rankingGeneration()
	if cached, ok := c.items[key]; ok && time.Since(cached.fetched) < presetChoicesTTL && cached.gen == gen {
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()

	q := url.Values{"userId": {p.Profile.ID}}
	if p.Client.DeviceID != "" {
		q.Set("clientId", "jellyfin-"+p.Client.DeviceID)
	}
	req, reqErr := s.prequeueRequest(ctx, p, ref)
	if reqErr == nil {
		q.Set("mediaType", req.MediaType)
		q.Set("titleId", req.TitleID)
		q.Set("name", req.TitleName)
		if req.Year > 0 {
			q.Set("year", strconv.Itoa(req.Year))
		}
		if req.ImdbID != "" {
			q.Set("imdbId", req.ImdbID)
		}
		// The same IDs the prequeue looks the library copy up by.
		if req.TmdbID != "" {
			q.Set("tmdbId", req.TmdbID)
		}
		if req.TvdbID != "" {
			q.Set("tvdbId", req.TvdbID)
		}
		if req.NoLibraryCopy {
			q.Set("noLibraryCopy", "1")
		}
		if req.EpisodeNumber > 0 {
			// "Library copy" is offered for the episode itself.
			q.Set("season", strconv.Itoa(req.SeasonNumber))
			q.Set("episode", strconv.Itoa(req.EpisodeNumber))
			if req.AbsoluteEpisodeNumber > 0 {
				q.Set("absolute", strconv.Itoa(req.AbsoluteEpisodeNumber))
			}
		}
	}
	var resp struct {
		Offered       []presetChoice  `json:"offered"`
		Movie         presetSelection `json:"movie"`
		Series        presetSelection `json:"series"`
		MovieDefault  presetSelection `json:"movieDefault"`
		SeriesDefault presetSelection `json:"seriesDefault"`
		AnimeReason   string          `json:"animeReason"`
	}
	var out itemPresets
	retryAt := time.Now().Add(presetChoicesRetry - presetChoicesTTL)
	if _, err := callJSON(s.deps.Handlers.QualityPresets, internalRequest(ctx, p, http.MethodGet, "/api/quality-presets?"+q.Encode(), nil, nil), &resp); err != nil {
		out = itemPresets{fetched: retryAt, gen: gen}
	} else {
		out = itemPresets{offered: resp.Offered, auto: resp.Series, def: resp.SeriesDefault, fetched: time.Now(), gen: gen}
		if ref.Kind == refMovie {
			out.auto, out.def = resp.Movie, resp.MovieDefault
		}
		if reqErr != nil || ctx.Err() != nil || resp.AnimeReason == "no metadata" {
			// Answered without the title or its metadata (a lookup ran out
			// of time): anime presets and the remembered one may be
			// missing. Used now, but asked again soon.
			out.fetched = retryAt
		}
	}
	c.mu.Lock()
	if c.items == nil || len(c.items) >= maxPresetChoices {
		c.items = map[string]itemPresets{}
	}
	c.items[key] = out
	c.mu.Unlock()
	return out
}

// presetVersions lists the offered presets that fit an item.
func (s *Server) presetVersions(ctx context.Context, p *principal, itemID string, ref itemRef) []presetChoice {
	mediaType := "series"
	if ref.Kind == refMovie {
		mediaType = "movie"
	}
	var out []presetChoice
	for _, c := range s.presetsForItem(ctx, p, itemID, ref).offered {
		if c.ID != "" && (c.MediaType == "" || c.MediaType == mediaType) {
			out = append(out, c)
		}
	}
	return out
}

// autoVersionLabel names the automatic version after the preset it uses;
// without any preset it keeps its original name.
func (s *Server) autoVersionLabel(ctx context.Context, p *principal, itemID string, ref itemRef) string {
	if !s.presetsInUse() {
		return "Auto (mediastorm)"
	}
	presets := s.presetsForItem(ctx, p, itemID, ref)
	if len(presets.offered) == 0 {
		// No preset versions to tell it apart from: the original name.
		return "Auto (mediastorm)"
	}
	if auto := presets.auto; auto.PresetID != "" && auto.Name != "" {
		return "Auto · " + auto.Name
	}
	return "Auto"
}

// forgetAutoRanking drops what the bridge cached for one title (titleKey;
// "" for every title) on one device (deviceKey): its preset choices and
// Auto's label, every pick for its items (so playing a preset version
// again resolves anew and mediastorm remembers that preset again) and its
// idle automatic play sessions; with releaseLists, Auto's release lists are
// also searched again (and served until then, so a release version picked
// from them still plays). It runs when a preset version is played, since
// mediastorm then remembers that preset for the title on that device and
// Auto ranks the title's items with it. Only play sessions created before
// pickedAt go (zero: all), so later passes leave an Auto session the user
// started after the pick alone.
func (s *Server) forgetAutoRanking(device, title string, pickedAt time.Time, releaseLists bool) {
	prefix := device + "|"
	// ofTitle reports whether a "device|item[|preset]" key is one of the
	// title's items on the device.
	ofTitle := func(key string) bool {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			return false
		}
		item, _, _ := strings.Cut(rest, "|")
		return title == "" || s.itemTitle(item) == title
	}
	s.presetChoices.mu.Lock()
	for key := range s.presetChoices.items {
		if ofTitle(key) {
			delete(s.presetChoices.items, key)
		}
	}
	s.presetChoices.mu.Unlock()
	s.autoPicks.mu.Lock()
	for key := range s.autoPicks.byKey {
		if ofTitle(key) {
			delete(s.autoPicks.byKey, key)
		}
	}
	s.autoPicks.mu.Unlock()
	if releaseLists {
		s.releases.mu.Lock()
		for key, list := range s.releases.lists {
			// A search still running may have started before mediastorm
			// remembered the preset: it is searched again once it is done.
			if strings.HasSuffix(key, "|") && ofTitle(key) {
				list.outdated = true
			}
		}
		s.releases.mu.Unlock()
	}
	// Automatic sessions resolved before (a prewarmed next episode) were
	// ranked with the old preset: drop those not playing.
	if s.plays == nil {
		return
	}
	s.plays.mu.Lock()
	var stale []*playSession
	for id, sess := range s.plays.byID {
		if sess.ProfileID+"|"+sess.DeviceID == device && sess.PresetID == "" && sess.ReleaseKey == "" &&
			(title == "" || s.itemTitle(sess.ItemID) == title) && !sess.playing() &&
			(pickedAt.IsZero() || sess.created.Before(pickedAt)) {
			stale = append(stale, sess)
			delete(s.plays.byID, id)
		}
	}
	s.plays.mu.Unlock()
	for _, sess := range stale {
		s.stopHLS(context.Background(), nil, sess)
	}
}

// itemTitle is the title an item belongs to for remembered presets: the
// movie, or an episode's series ("" when unknown).
func (s *Server) itemTitle(itemID string) string {
	ref, ok := s.ids.get(itemID)
	if !ok {
		return ""
	}
	return refTitle(ref)
}

// refTitle is itemTitle for a known ref.
func refTitle(ref itemRef) string {
	switch ref.Kind {
	case refMovie:
		return "movie:" + titleFromRef(ref).source()
	case refEpisode:
		// Specials (season 0) included: mediastorm remembers the preset
		// for the whole series.
		if series := seriesInfoFromSeasonRef(ref); series.ID != "" {
			return "series:" + series.source()
		}
	}
	return ""
}

// forgetAutoRankingAfterPick runs forgetAutoRanking now and again when
// mediastorm has remembered the picked preset (it does so in the
// background, after looking up the title's default): requests in between
// would cache Auto ranked with the old one.
func (s *Server) forgetAutoRankingAfterPick(device string, ref itemRef) {
	title := refTitle(ref)
	if title == "" {
		return
	}
	// Release lists are searched again once, after the last wait: each
	// search costs indexer requests, and until then the old order serves.
	pickedAt := time.Now()
	s.forgetAutoRanking(device, title, pickedAt, false)
	time.AfterFunc(presetRememberSettle, func() { s.forgetAutoRanking(device, title, pickedAt, false) })
	time.AfterFunc(presetRememberSettleLate, func() { s.forgetAutoRanking(device, title, pickedAt, true) })
}

// presetMediaSourceID is the media source ID of a preset version.
func (s *Server) presetMediaSourceID(p *principal, itemID, presetID string) string {
	return s.mediaSourceID(p.Profile.ID, p.Client.DeviceID, itemID+"|preset|"+presetID)
}

// presetByMediaSource finds the preset a media source ID stands for.
func (s *Server) presetByMediaSource(ctx context.Context, p *principal, itemID string, ref itemRef, mediaSourceID string) (presetChoice, bool) {
	// Clients may send the ID dashed.
	mediaSourceID = normalizeID(mediaSourceID)
	if mediaSourceID == "" {
		return presetChoice{}, false
	}
	for _, c := range s.presetVersions(ctx, p, itemID, ref) {
		if s.presetMediaSourceID(p, itemID, c.ID) == mediaSourceID {
			return c, true
		}
	}
	return presetChoice{}, false
}

// presetVersionLabel names a version that ranks with an explicitly picked
// preset. "Preset" sets it apart from Auto, which is named after the preset
// it uses ("Auto · German HD") and can be the same one. The preset the item
// gets without a remembered one is marked "(default)": playing it makes the
// title follow the defaults again.
func presetVersionLabel(c presetChoice, def presetSelection) string {
	label := "Preset · " + c.Name
	if c.ID == libraryPresetID {
		// Not a ranking: the file in the local library.
		label = c.Name
	}
	if def.Source != "" && presetIDsEqual(c.ID, def.PresetID) {
		label += " (default)"
	}
	return label
}

// libraryPresetID is mediastorm's "Library copy" choice: the title plays from a
// local library (config.QualityPresetLocal).
const libraryPresetID = "local"

// presetIDsEqual compares preset IDs; "simple" and "" both mean the simple
// ranking.
func presetIDsEqual(a, b string) bool {
	norm := func(id string) string {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "simple" {
			return ""
		}
		return id
	}
	return norm(a) == norm(b)
}
