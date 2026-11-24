package indexer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/models"
	"novastream/services/qualityprofiles"
	"novastream/utils/filter"
	"novastream/utils/language"
)

// QualityPresetProvider returns compiled quality presets.
type QualityPresetProvider interface {
	Active(ctx context.Context, id string) (*qualityprofiles.Active, error)
}

// SetQualityPresetProvider enables quality presets.
func (s *Service) SetQualityPresetProvider(p QualityPresetProvider) {
	s.presets = p
}

// TitlePresetProvider returns the preset a profile last picked for a title
// on a device ("" when none).
type TitlePresetProvider interface {
	TitlePreset(ctx context.Context, userID, clientID, contentID string) (string, error)
}

// SetTitlePresetProvider enables the per-title preset memory.
func (s *Service) SetTitlePresetProvider(p TitlePresetProvider) {
	s.titlePresets = p
}

// titlePreset returns the preset the profile last picked for the search's
// title on the search's device. It counts only while it could still be
// picked: the server offers it, the profile's picker list allows it and it
// exists.
func (s *Service) titlePreset(opts SearchOptions, settings config.Settings) (QualityPresetSelection, bool) {
	titleID := strings.TrimSpace(opts.PresetTitleID)
	if titleID == "" {
		titleID = strings.TrimSpace(opts.TitleID)
	}
	if s.titlePresets == nil || opts.UserID == "" || titleID == "" {
		return QualityPresetSelection{}, false
	}
	if u, ok := s.presets.(interface{ InUse(context.Context) bool }); s.presets == nil || (ok && !u.InUse(context.Background())) {
		// No presets: nothing to remember (and no lookup per search).
		return QualityPresetSelection{}, false
	}
	id, err := s.titlePresets.TitlePreset(context.Background(), opts.UserID, opts.ClientID, titleID)
	if err != nil || strings.TrimSpace(id) == "" {
		return QualityPresetSelection{}, false
	}
	id = strings.TrimSpace(id)
	if !settings.QualityPresets.AllowsRequestPreset(id) || !s.profileAllowsPreset(opts.UserID, id, settings) || !s.presetFitsMedia(id, opts.MediaType) {
		return QualityPresetSelection{}, false
	}
	return QualityPresetSelection{PresetID: normalizePresetID(id), Source: "title"}, true
}

// presetFitsMedia reports whether a remembered preset still exists and is
// for the search's app: one of the other app would only fall back to the
// simple ranking, so the next level decides instead.
func (s *Service) presetFitsMedia(id, mediaType string) bool {
	id = normalizePresetID(id)
	if id == "" || s.presets == nil {
		return true
	}
	active, err := s.presets.Active(context.Background(), id)
	if errors.Is(err, qualityprofiles.ErrNotFound) {
		return false
	}
	return err != nil || strings.TrimSpace(mediaType) == "" || active.App == arr.AppForMediaType(mediaType)
}

func normalizePresetID(v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, config.QualityPresetSimple) {
		return ""
	}
	return v
}

func isMovieSearch(opts SearchOptions) bool {
	return strings.EqualFold(strings.TrimSpace(opts.MediaType), "movie")
}

// QualityPresetSelection explains which preset a search uses.
type QualityPresetSelection struct {
	PresetID string `json:"presetId"`
	// Source is request, title, device, profile, global or none; anime
	// slots add "-anime" (device-anime, profile-anime, global-anime).
	Source string `json:"source"`
}

// SelectQualityPreset resolves the preset for a search: the request (when
// allowed), then the preset last picked for the title, then, for anime titles, the anime slots of the device, the
// profile and the server, then the normal slots of the device, the profile
// and the server for the media type. An empty PresetID means the simple
// ranking settings.
func (s *Service) SelectQualityPreset(opts SearchOptions, settings config.Settings) QualityPresetSelection {
	if settings.QualityPresets.Disabled {
		return QualityPresetSelection{Source: "disabled"}
	}
	if req := strings.TrimSpace(opts.QualityPreset); settings.QualityPresets.AllowsRequestPreset(req) && s.profileAllowsPreset(opts.UserID, req, settings) {
		return QualityPresetSelection{PresetID: normalizePresetID(req), Source: "request"}
	}
	if sel, ok := s.titlePreset(opts, settings); ok {
		return sel
	}
	// Anime looks for an anime preset at every level before any normal
	// one, so a device's normal series preset does not override the anime
	// preset for anime.
	if opts.IsAnime {
		if sel, ok := s.selectPresetSlot(opts, settings, true); ok {
			return sel
		}
	}
	if sel, ok := s.selectPresetSlot(opts, settings, false); ok {
		return sel
	}
	return QualityPresetSelection{Source: "none"}
}

// Errors for a qualityPreset a search asked for.
var (
	ErrQualityPresetUnknown    = errors.New("unknown quality preset")
	ErrQualityPresetNotAllowed = errors.New("this quality preset may not be requested for a single search")
	ErrQualityPresetWrongApp   = errors.New("this quality preset is for the other media type")
)

// CheckRequestedPreset tells a client up front why the preset it asked for
// would not be used, instead of silently ranking with another one.
func (s *Service) CheckRequestedPreset(ctx context.Context, opts SearchOptions) error {
	req := strings.TrimSpace(opts.QualityPreset)
	if req == "" {
		return nil
	}
	settings, err := s.cfg.Load()
	if err != nil {
		return nil // the search itself will report it
	}
	if settings.QualityPresets.Disabled {
		return nil // switched off: the search ignores it and ranks simply
	}
	if !strings.EqualFold(req, config.QualityPresetSimple) {
		if s.presets == nil {
			return ErrQualityPresetUnknown
		}
		active, err := s.presets.Active(ctx, req)
		if err != nil {
			return fmt.Errorf("%w %q", ErrQualityPresetUnknown, req)
		}
		// A Radarr preset cannot rank a series search (nor the other way
		// round): the search would fall back to the simple ranking.
		if opts.MediaType != "" && active.App != arr.AppForMediaType(opts.MediaType) {
			return fmt.Errorf("%w: %q is a %s preset", ErrQualityPresetWrongApp, active.Name, active.App)
		}
	}
	if !settings.QualityPresets.AllowsRequestPreset(req) || !s.profileAllowsPreset(opts.UserID, req, settings) {
		return ErrQualityPresetNotAllowed
	}
	return nil
}

// presetDeleted reports whether a device or profile choice names a preset
// that no longer exists. Such a choice is skipped so the next level (profile,
// then server) decides; deleting a preset clears only the server's choices.
func (s *Service) presetDeleted(id string) bool {
	id = normalizePresetID(id)
	if id == "" || s.presets == nil {
		return false
	}
	_, err := s.presets.Active(context.Background(), id)
	return errors.Is(err, qualityprofiles.ErrNotFound)
}

// profileAllowsPreset reports whether a profile's picker list permits a
// preset picked for a single search.
func (s *Service) profileAllowsPreset(userID, id string, settings config.Settings) bool {
	if userID == "" {
		return true
	}
	var sel *models.QualityPresetSelection
	if s.userSettings != nil {
		if us, err := s.userSettings.Get(userID); err == nil && us != nil {
			sel = us.QualityPresets
		}
	}
	return sel.AllowsPickerDefault(settings.QualityPresets.HiddenByDefault, id)
}

// selectPresetSlot walks device, profile and server for the normal or the
// anime slot of the search's media type.
func (s *Service) selectPresetSlot(opts SearchOptions, settings config.Settings, anime bool) (QualityPresetSelection, bool) {
	movie := isMovieSearch(opts)
	suffix := ""
	if anime {
		suffix = "-anime"
	}
	pick := func(normalMovie, normalSeries, animeMovie, animeSeries *string) *string {
		switch {
		case anime && movie:
			return animeMovie
		case anime:
			return animeSeries
		case movie:
			return normalMovie
		}
		return normalSeries
	}
	if opts.ClientID != "" && s.clientSettings != nil {
		if cs, err := s.clientSettings.Get(opts.ClientID, opts.UserID); err == nil && cs != nil {
			if v := pick(cs.QualityPresetMovie, cs.QualityPresetSeries, cs.QualityPresetAnimeMovie, cs.QualityPresetAnimeSeries); v != nil && !s.presetDeleted(*v) {
				return QualityPresetSelection{PresetID: normalizePresetID(*v), Source: "device" + suffix}, true
			}
		}
	}
	if opts.UserID != "" && s.userSettings != nil {
		if us, err := s.userSettings.Get(opts.UserID); err == nil && us != nil && us.QualityPresets != nil {
			q := us.QualityPresets
			if v := pick(q.MoviePreset, q.SeriesPreset, q.AnimeMoviePreset, q.AnimeSeriesPreset); v != nil && !s.presetDeleted(*v) {
				return QualityPresetSelection{PresetID: normalizePresetID(*v), Source: "profile" + suffix}, true
			}
		}
	}
	qp := settings.QualityPresets
	global := *pick(&qp.MoviePreset, &qp.SeriesPreset, &qp.AnimeMoviePreset, &qp.AnimeSeriesPreset)
	if strings.TrimSpace(global) == "" {
		return QualityPresetSelection{}, false
	}
	return QualityPresetSelection{PresetID: normalizePresetID(global), Source: "global" + suffix}, true
}

// resolveQualityPreset loads the preset a search uses, or nil for the
// simple ranking. A missing or broken preset falls back to the simple
// ranking so search keeps working.
func (s *Service) resolveQualityPreset(ctx context.Context, opts SearchOptions, settings config.Settings) *qualityprofiles.Active {
	if s.presets == nil {
		return nil
	}
	sel := s.SelectQualityPreset(opts, settings)
	if sel.PresetID == "" {
		return nil
	}
	active, err := s.presets.Active(ctx, sel.PresetID)
	if err != nil {
		log.Printf("[quality-preset] %s preset %q unavailable, using simple ranking: %v", sel.Source, sel.PresetID, err)
		return nil
	}
	if want := arr.AppForMediaType(opts.MediaType); active.App != want {
		log.Printf("[quality-preset] preset %q is a %s preset but the search is for %s; using simple ranking", active.Name, active.App, opts.MediaType)
		return nil
	}
	if !active.Audience.Allows(opts.IsAnime) {
		log.Printf("[quality-preset] preset %q is for audience %q but the title is anime=%v; using simple ranking", active.Name, active.Audience, opts.IsAnime)
		return nil
	}
	return active
}

// presetChain is the preset a search ranks with, followed by its fallbacks
// (each tried when the one before passes nothing). simple says whether the
// simple ranking comes after the last of them. A fallback that is gone, is
// for the other app or does not fit the title ends the chain.
func (s *Service) presetChain(ctx context.Context, active *qualityprofiles.Active, opts SearchOptions) (chain []*qualityprofiles.Active, simple bool) {
	chain = []*qualityprofiles.Active{active}
	seen := map[string]bool{active.PresetID: true}
	for cur := active; len(chain) <= qualityprofiles.MaxFallbacks; {
		next := strings.TrimSpace(cur.Fallback)
		switch {
		case next == "":
			return chain, false
		case strings.EqualFold(next, qualityprofiles.SimpleFallback):
			return chain, true
		case seen[next] || s.presets == nil:
			return chain, false
		}
		a, err := s.presets.Active(ctx, next)
		if err != nil {
			log.Printf("[quality-preset] fallback of %q unavailable: %v", cur.Name, err)
			return chain, false
		}
		if a.App != active.App || !a.Audience.Allows(opts.IsAnime) {
			log.Printf("[quality-preset] fallback %q of %q does not fit this search (app %s, audience %q, anime=%v)", a.Name, cur.Name, a.App, a.Audience, opts.IsAnime)
			return chain, false
		}
		seen[a.PresetID] = true
		chain = append(chain, a)
		cur = a
	}
	return chain, false
}

// logPresetFallback reports that a fallback ranked a search.
func logPresetFallback(opts SearchOptions, from *qualityprofiles.Active, to string) {
	log.Printf("[quality-preset] %q passed nothing for query=%q user=%q; using fallback %s", from.Name, opts.Query, opts.UserID, to)
}

// scoreWithPresetChain scores with the search's preset and, while nothing
// passes, with its fallback presets. simple says that nothing passed and
// the simple ranking comes next.
func (s *Service) scoreWithPresetChain(active *qualityprofiles.Active, detailed []filter.FilteredResult, opts SearchOptions, servicePriority config.StreamingServicePriority) (passed, filtered []models.ScoredNZBResult, simple bool) {
	chain, toSimple := s.presetChain(context.Background(), active, opts)
	var firstFiltered []models.ScoredNZBResult
	for i, preset := range chain {
		if i > 0 {
			logPresetFallback(opts, chain[i-1], fmt.Sprintf("%q", preset.Name))
		}
		passed, filtered = s.scoreWithPreset(preset, detailed, opts, servicePriority)
		if len(passed) > 0 {
			return passed, filtered, false
		}
		if i == 0 {
			firstFiltered = filtered
		}
	}
	if toSimple {
		logPresetFallback(opts, chain[len(chain)-1], "the simple ranking")
	}
	// Nothing passed: the search's own preset says why.
	return nil, firstFiltered, toSimple
}

// rankWithPresetChain is rankWithPreset with the preset's fallbacks.
func (s *Service) rankWithPresetChain(ctx context.Context, active *qualityprofiles.Active, results []models.NZBResult, opts SearchOptions, servicePriority config.StreamingServicePriority) (ranked []models.NZBResult, simple bool) {
	chain, toSimple := s.presetChain(ctx, active, opts)
	for i, preset := range chain {
		if i > 0 {
			logPresetFallback(opts, chain[i-1], fmt.Sprintf("%q", preset.Name))
		}
		if ranked, _ = s.rankWithPreset(preset, results, opts, servicePriority); len(ranked) > 0 {
			return ranked, false
		}
	}
	if toSimple {
		logPresetFallback(opts, chain[len(chain)-1], "the simple ranking")
	}
	return nil, toSimple
}

// withoutPreset makes a search rank with the simple ranking, the last
// fallback of a preset that passed nothing.
func withoutPreset(opts SearchOptions) SearchOptions {
	opts.activePreset, opts.presetResolved = nil, true
	return opts
}

// withQualityPreset resolves the preset once and records it on the options.
func (s *Service) withQualityPreset(ctx context.Context, opts SearchOptions, settings config.Settings) SearchOptions {
	if !opts.presetResolved {
		opts.activePreset = s.resolveQualityPreset(ctx, opts, settings)
		opts.presetResolved = true
	}
	return opts
}

// presetCacheKey distinguishes cached rankings per preset version.
func presetCacheKey(a *qualityprofiles.Active) string {
	if a == nil {
		return ""
	}
	return "|preset:" + a.PresetID + "@" + strconv.FormatInt(a.UpdatedAt.UnixNano(), 36)
}

// withoutPreferences applies preset mode to a filter bundle: preference
// filters are dropped and only the adaptive device caps remain.
func (b effectiveFilterBundle) withoutPreferences() effectiveFilterBundle {
	out := effectiveFilterBundle{caps: b.caps}
	out.Default = b.Default.WithoutPreferences()
	b.caps.ApplyTo(&out.Default)
	out.Debrid, out.Usenet = out.Default, out.Default
	return out
}

func presetReleaseContext(opts SearchOptions) arr.ReleaseContext {
	return arr.ReleaseContext{
		OriginalLanguage: arr.LanguageFromCode(opts.OriginalLanguage),
		RuntimeMinutes:   opts.RuntimeMinutes,
		IsAnime:          opts.IsAnime,
		EpisodeTitles:    nonEmpty(opts.EpisodeTitle),
	}
}

// languagesFromAttribute converts an indexer or scraper language list
// (ISO codes, names or flag emojis, separated by , / | +) to languages.
func languagesFromAttribute(v string) []arr.Language {
	var out []arr.Language
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '/' || r == '|' || r == '+' || r == ';' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		l := arr.LanguageFromCode(part)
		if l == arr.LanguageUnknown {
			if code := language.NormalizeToCode(part); code != "" && code != "mul" {
				l = arr.LanguageFromCode(code)
			}
		}
		if l != arr.LanguageUnknown && l != "" {
			dup := false
			for _, x := range out {
				if x == l {
					dup = true
				}
			}
			if !dup {
				out = append(out, l)
			}
		}
	}
	return out
}

func onlyUnknown(langs []arr.Language) bool {
	return len(langs) == 0 || (len(langs) == 1 && langs[0] == arr.LanguageUnknown)
}

// presetRelease builds the evaluated release for a search result.
func presetRelease(active *qualityprofiles.Active, r models.NZBResult, rc arr.ReleaseContext) arr.Release {
	rel := arr.Parse(active.App, r.Title)
	rel.Size = r.SizeBytes
	switch r.ServiceType {
	case models.ServiceTypeUsenet:
		rel.Protocol = arr.ProtocolUsenet
	case models.ServiceTypeDebrid:
		rel.Protocol = arr.ProtocolTorrent
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.Attributes["seeders"])); err == nil {
		rel.Seeders = &v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.Attributes["peers"])); err == nil {
		rel.Peers = &v
	} else if v, err := strconv.Atoi(strings.TrimSpace(r.Attributes["leechers"])); err == nil {
		rel.Peers = &v
	}
	rel.PublishDate = r.PublishDate
	if r.EpisodeCount > 1 {
		rel.EpisodeCount = r.EpisodeCount
		// Size limits and the preferred size apply to the whole pack; some
		// scrapers report the size of the one file that plays.
		if r.SizePerFile {
			rel.Size = r.SizeBytes * int64(r.EpisodeCount)
		}
	}
	ctx := rc
	// Like *arr, languages an indexer reports replace the parsed ones;
	// scraper-derived language guesses only fill in when the release name
	// carries no language tag.
	if v := r.Attributes["language"]; v != "" {
		ctx.ProvidedLanguages = languagesFromAttribute(v)
	} else if onlyUnknown(rel.Languages) {
		if v := r.Attributes["languages"]; v != "" {
			ctx.ProvidedLanguages = languagesFromAttribute(v)
		}
	}
	return active.Evaluator.Prepare(rel, ctx)
}

type presetScored struct {
	result models.NZBResult
	eval   arr.Evaluation
}

// comparePresetResults keeps the fixed correctness checks first, then the
// preset's own order, then mediastorm's service priority and a
// deterministic tie-break. (The explicit audio language is applied
// afterwards by prioritizeExplicitAudio, as for the simple ranking.)
func comparePresetResults(a, b presetScored, active *qualityprofiles.Active, servicePriority config.StreamingServicePriority) int {
	if c := compareCountryMatch(a.result, b.result); c != 0 {
		return c
	}
	if c := compareEpisodeYearMatch(a.result, b.result); c != 0 {
		return c
	}
	// Without a protocol of the preset's own, mediastorm's service priority
	// orders usenet and debrid (torrent) results.
	protocol := arr.ProtocolUnknown
	switch servicePriority {
	case config.StreamingServicePriorityUsenet:
		protocol = arr.ProtocolUsenet
	case config.StreamingServicePriorityDebrid:
		protocol = arr.ProtocolTorrent
	}
	if c := active.Evaluator.CompareWithProtocol(a.eval, b.eval, protocol); c != 0 {
		return c
	}
	return compareDeterministicTieBreaker(a.result, b.result)
}

func annotatePresetResult(r models.NZBResult, active *qualityprofiles.Active, ev arr.Evaluation) models.NZBResult {
	attrs := make(map[string]string, len(r.Attributes)+4)
	for k, v := range r.Attributes {
		attrs[k] = v
	}
	attrs["qualityPreset"] = active.Name
	attrs["qualityPresetId"] = active.PresetID
	attrs["arrQuality"] = ev.Release.Quality.Name
	attrs["customFormatScore"] = strconv.Itoa(ev.Score)
	names := make([]string, 0, len(ev.Formats))
	for _, f := range ev.Formats {
		names = append(names, f.Name)
	}
	if len(names) > 0 {
		attrs["customFormats"] = strings.Join(names, ", ")
	} else {
		delete(attrs, "customFormats")
	}
	r.Attributes = attrs
	return r
}

// Score breakdown criteria of preset-ranked results. The first item of every
// preset breakdown is PresetBreakdownCriterion; the quality position and
// the total custom format score are what the preset ranks by.
const (
	PresetBreakdownCriterion   = "Quality Preset"
	PresetQualityCriterion     = "Quality"
	PresetFormatScoreCriterion = "Custom Format Score"
	// PresetRevisionCriterion ranks propers and repacks between quality and
	// score when the profile prefers them (RankValue: version*100 + real).
	PresetRevisionCriterion = "Revision"
)

// presetBreakdown explains a preset evaluation in the score breakdown.
func presetBreakdown(active *qualityprofiles.Active, ev arr.Evaluation) []models.ScoreBreakdownItem {
	group := ""
	if ev.QualityGroup != "" {
		group = " in group " + ev.QualityGroup
	}
	items := []models.ScoreBreakdownItem{
		{Criterion: PresetBreakdownCriterion, Reason: fmt.Sprintf("ranked by %s preset %q (quality first, then custom format score)", active.App, active.Name)},
		{Criterion: PresetQualityCriterion, RankValue: int64(ev.QualityRank), Reason: fmt.Sprintf("%s%s (position %d)", ev.Release.Quality.Name, group, ev.QualityRank)},
	}
	if active.Evaluator.Profile().Propers == arr.PropersPreferAndUpgrade {
		rev := ev.Release.Revision
		items = append(items, models.ScoreBreakdownItem{Criterion: PresetRevisionCriterion, RankValue: int64(rev.Version*100 + rev.Real),
			Reason: fmt.Sprintf("version %d (propers and repacks preferred)", rev.Version)})
	}
	for _, f := range ev.Formats {
		items = append(items, models.ScoreBreakdownItem{Criterion: "Custom Format: " + f.Name, Points: f.Score, RankValue: int64(f.Score), Reason: "matched"})
	}
	items = append(items, models.ScoreBreakdownItem{Criterion: PresetFormatScoreCriterion, Points: ev.Score, RankValue: int64(ev.Score),
		Reason: fmt.Sprintf("%d format(s) matched; minimum %d", len(ev.Formats), active.Evaluator.Profile().MinFormatScore)})
	langs := make([]string, 0, len(ev.Release.Languages))
	for _, l := range ev.Release.Languages {
		langs = append(langs, string(l))
	}
	items = append(items, models.ScoreBreakdownItem{Criterion: "Languages", Reason: strings.Join(langs, ", ")})
	if ev.Release.ReleaseGroup != "" {
		items = append(items, models.ScoreBreakdownItem{Criterion: "Release Group", Reason: ev.Release.ReleaseGroup})
	}
	for _, r := range ev.Rejections {
		items = append(items, models.ScoreBreakdownItem{Criterion: "Rejected", Reason: r})
	}
	return items
}

func presetRejectReason(ev arr.Evaluation) string {
	return "quality preset: " + strings.Join(ev.Rejections, "; ")
}

// rankWithPreset evaluates passed results, drops the ones the preset
// rejects and sorts the rest. It returns the kept results and the
// rejected evaluations.
func (s *Service) rankWithPreset(active *qualityprofiles.Active, results []models.NZBResult, opts SearchOptions, servicePriority config.StreamingServicePriority) ([]models.NZBResult, []presetScored) {
	kept, rejected := s.evaluatePreset(active, results, opts, servicePriority)
	out := make([]models.NZBResult, len(kept))
	for i, k := range kept {
		out[i] = k.result
	}
	return out, rejected
}

func (s *Service) evaluatePreset(active *qualityprofiles.Active, results []models.NZBResult, opts SearchOptions, servicePriority config.StreamingServicePriority) ([]presetScored, []presetScored) {
	rc := presetReleaseContext(opts)
	scored := make([]presetScored, len(results))
	forEachParallel(len(results), func(i int) {
		ev := active.Evaluator.Evaluate(presetRelease(active, results[i], rc))
		scored[i] = presetScored{result: annotatePresetResult(results[i], active, ev), eval: ev}
	})
	var kept, rejected []presetScored
	for _, ps := range scored {
		if ps.eval.Allowed {
			kept = append(kept, ps)
		} else {
			rejected = append(rejected, ps)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return comparePresetResults(kept[i], kept[j], active, servicePriority) < 0
	})
	log.Printf("[quality-preset] %q: %d of %d results wanted", active.Name, len(kept), len(results))
	return kept, rejected
}

// presetParallelMin is the result count from which evaluation is spread
// over several goroutines; parsing and custom format matching cost roughly
// half a millisecond per release.
const presetParallelMin = 32

// forEachParallel calls fn for every index in [0, n), on up to GOMAXPROCS
// goroutines when n is large enough to be worth it.
func forEachParallel(n int, fn func(i int)) {
	workers := runtime.GOMAXPROCS(0)
	if n < presetParallelMin || workers < 2 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	if workers > n/8 {
		workers = n / 8
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// scoreWithPreset turns detailed filter output into scored results ranked
// by the preset: filter rejections and preset rejections become filtered
// results, the rest is sorted by the preset.
func (s *Service) scoreWithPreset(active *qualityprofiles.Active, detailed []filter.FilteredResult, opts SearchOptions, servicePriority config.StreamingServicePriority) (passed, filtered []models.ScoredNZBResult) {
	var candidates []models.NZBResult
	for _, fr := range detailed {
		if fr.Passed {
			candidates = append(candidates, fr.Result)
			continue
		}
		filtered = append(filtered, models.ScoredNZBResult{NZBResult: fr.Result, FilterStatus: "filtered", FilterReason: fr.RejectReason})
	}
	kept, rejected := s.evaluatePreset(active, candidates, opts, servicePriority)
	for _, k := range kept {
		sr := models.ScoredNZBResult{NZBResult: k.result, FilterStatus: "passed", TotalScore: k.eval.Score}
		if opts.IncludeScoreBreakdown {
			sr.ScoreBreakdown = presetBreakdown(active, k.eval)
		}
		passed = append(passed, sr)
	}
	for _, r := range rejected {
		sr := models.ScoredNZBResult{NZBResult: r.result, FilterStatus: "filtered", FilterReason: presetRejectReason(r.eval), TotalScore: r.eval.Score}
		if opts.IncludeScoreBreakdown {
			sr.ScoreBreakdown = presetBreakdown(active, r.eval)
		}
		filtered = append(filtered, sr)
	}
	return passed, filtered
}

// capPerResolution keeps at most max results per resolution (0 = no cap).
func capPerResolution(results []models.ScoredNZBResult, max int) []models.ScoredNZBResult {
	if max <= 0 {
		return results
	}
	counts := map[int]int{}
	limited := make([]models.ScoredNZBResult, 0, len(results))
	for _, r := range results {
		res := extractResolutionFromResult(r.NZBResult)
		if counts[res] < max {
			limited = append(limited, r)
			counts[res]++
		}
	}
	return limited
}

// presetFallbackFilters are a split search's own filters, kept for a source
// whose preset chain ends in the simple ranking.
type presetFallbackFilters struct {
	bundle effectiveFilterBundle
	titles []string
}

// withAnimeFilterOut is bundle with the anime language filter-out terms a
// search without a preset adds (copied, not shared).
func withAnimeFilterOut(bundle effectiveFilterBundle, opts SearchOptions, animeSettings models.AnimeFilteringSettings) effectiveFilterBundle {
	if !opts.IsAnime || !models.BoolVal(animeSettings.AnimeLanguageEnabled, false) {
		return bundle
	}
	langCode := "eng"
	if animeSettings.AnimePreferredLanguage != nil && *animeSettings.AnimePreferredLanguage != "" {
		langCode = *animeSettings.AnimePreferredLanguage
	}
	_, _, terms := filter.GetAnimeLanguageTerms(langCode)
	if len(terms) == 0 {
		return bundle
	}
	add := func(list []string) []string { return append(append([]string(nil), list...), terms...) }
	bundle.Default.FilterOutTerms = add(bundle.Default.FilterOutTerms)
	bundle.Debrid.FilterOutTerms = add(bundle.Debrid.FilterOutTerms)
	bundle.Usenet.FilterOutTerms = add(bundle.Usenet.FilterOutTerms)
	return bundle
}

// nonEmpty is a one-element list of s, or nil when s is blank.
func nonEmpty(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{s}
}

// rankPresetPassed finishes a preset ranking's passed results: the explicit
// audio language comes first (in every mode), then the per-resolution cap,
// so a release with that audio is not capped away.
func rankPresetPassed(passed []models.ScoredNZBResult, audio string, maxPerResolution int) []models.ScoredNZBResult {
	prioritizeExplicitAudioScored(passed, audio)
	return capPerResolution(passed, maxPerResolution)
}
