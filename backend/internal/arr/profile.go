package arr

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// PropersMode mirrors *arr's "Propers and Repacks" media management setting.
type PropersMode string

const (
	// PropersDoNotPrefer ignores revisions when comparing qualities; repacks
	// are expected to be scored by custom formats (TRaSH recommendation).
	PropersDoNotPrefer PropersMode = "doNotPrefer"
	// PropersPreferAndUpgrade ranks higher revisions of the same quality first.
	PropersPreferAndUpgrade PropersMode = "preferAndUpgrade"
)

// ProfileItem is a quality or a group of equally ranked qualities. Profile
// items are ordered from most to least preferred (UI and TRaSH order).
type ProfileItem struct {
	Name      string   `json:"name"`
	Allowed   bool     `json:"allowed"`
	Qualities []string `json:"items,omitempty"`
}

// SizeLimit is a per-quality size definition in MB per minute of runtime.
type SizeLimit struct {
	Min       float64  `json:"min"`
	Preferred *float64 `json:"preferred,omitempty"`
	Max       *float64 `json:"max,omitempty"`
}

// Profile is a Sonarr/Radarr quality profile plus the custom format scores
// it assigns.
type Profile struct {
	App                   App                  `json:"app"`
	Name                  string               `json:"name"`
	Items                 []ProfileItem        `json:"items"`
	Cutoff                string               `json:"cutoff,omitempty"`
	UpgradeAllowed        bool                 `json:"upgradeAllowed,omitempty"`
	MinFormatScore        int                  `json:"minFormatScore"`
	CutoffFormatScore     int                  `json:"cutoffFormatScore,omitempty"`
	MinUpgradeFormatScore int                  `json:"minUpgradeFormatScore,omitempty"`
	Language              Language             `json:"language,omitempty"`
	FormatScores          map[string]int       `json:"formatScores"`
	Propers               PropersMode          `json:"propers,omitempty"`
	SizeLimits            map[string]SizeLimit `json:"sizeLimits,omitempty"`
	EnforceSizeLimits     bool                 `json:"enforceSizeLimits,omitempty"`
	PreferredProtocol     Protocol             `json:"preferredProtocol,omitempty"`
	// MultiLanguages are added to releases tagged MULTi, like an indexer's
	// "Multi Languages" setting in *arr.
	MultiLanguages []Language `json:"multiLanguages,omitempty"`
}

type qualityRank struct {
	item     int // index into Items, 0 = most preferred
	group    string
	allowed  bool
	position int // rank value, higher = better
}

// Evaluator evaluates and compares releases for one profile. It is safe for
// concurrent use.
type Evaluator struct {
	profile Profile
	formats *FormatSet
	ranks   map[string]qualityRank // lower-case quality name
	scores  map[string]int
	// Unsupported lists conditions of scored formats that cannot be
	// evaluated faithfully (reported in explanations).
	Unsupported map[string][]string
}

// ValidateProfile checks a profile's structure for an app.
func ValidateProfile(p Profile) error {
	if !p.App.Valid() {
		return fmt.Errorf("profile %q: invalid app %q", p.Name, p.App)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("profile name is required")
	}
	if len(p.Items) == 0 {
		return fmt.Errorf("profile %q: no qualities", p.Name)
	}
	seen := map[string]bool{}
	allowed := 0
	for _, item := range p.Items {
		names := item.Qualities
		if len(names) == 0 {
			names = []string{item.Name}
		}
		for _, n := range names {
			if _, ok := QualityByName(p.App, n); !ok {
				return fmt.Errorf("profile %q: unknown %s quality %q", p.Name, p.App, n)
			}
			key := strings.ToLower(n)
			if seen[key] {
				return fmt.Errorf("profile %q: quality %q listed twice", p.Name, n)
			}
			seen[key] = true
		}
		if item.Allowed {
			allowed++
		}
	}
	if allowed == 0 {
		return fmt.Errorf("profile %q: no quality is allowed", p.Name)
	}
	if p.Propers != "" && p.Propers != PropersDoNotPrefer && p.Propers != PropersPreferAndUpgrade {
		return fmt.Errorf("profile %q: invalid propers mode %q", p.Name, p.Propers)
	}
	return nil
}

// NewEvaluator compiles a profile and the custom formats it may score.
// Formats that fail to compile are reported and ignored.
func NewEvaluator(p Profile, formats []CustomFormat) (*Evaluator, []error) {
	if err := ValidateProfile(p); err != nil {
		return nil, []error{err}
	}
	e := &Evaluator{profile: p, ranks: map[string]qualityRank{}, scores: map[string]int{}, Unsupported: map[string][]string{}}
	n := len(p.Items)
	for i, item := range p.Items {
		names := item.Qualities
		group := ""
		if len(names) == 0 {
			names = []string{item.Name}
		} else {
			group = item.Name
		}
		for _, name := range names {
			e.ranks[strings.ToLower(name)] = qualityRank{item: i, group: group, allowed: item.Allowed, position: n - i}
		}
	}
	// Only formats with a non-zero score influence ranking; evaluating just
	// those keeps per-release cost proportional to the profile, not the
	// catalogue.
	var scored []CustomFormat
	for _, cf := range formats {
		if v, ok := p.FormatScores[cf.Key()]; ok && v != 0 {
			scored = append(scored, cf)
			e.scores[cf.Key()] = v
		}
	}
	set, errs := NewFormatSet(p.App, scored)
	e.formats = set
	for _, c := range set.Formats() {
		// A penalty must survive a regex timeout (see timeoutMatches).
		c.timeoutMatches = e.scores[c.Format.Key()] < 0
		if len(c.Unsupported) > 0 {
			e.Unsupported[c.Format.Name] = c.Unsupported
		}
	}
	return e, errs
}

// Profile returns the evaluator's profile.
func (e *Evaluator) Profile() Profile { return e.profile }

// MatchedFormat is a custom format that matched a release.
type MatchedFormat struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Evaluation is the outcome of evaluating one release.
type Evaluation struct {
	Release      Release         `json:"release"`
	Allowed      bool            `json:"allowed"`
	QualityRank  int             `json:"qualityRank"`
	QualityGroup string          `json:"qualityGroup,omitempty"`
	Score        int             `json:"score"`
	Formats      []MatchedFormat `json:"formats,omitempty"`
	Rejections   []string        `json:"rejections,omitempty"`
}

// ReleaseContext supplies facts about the requested title that *arr knows
// from its library: original language and runtime.
type ReleaseContext struct {
	OriginalLanguage Language
	RuntimeMinutes   int
	// ProvidedLanguages are languages reported by the indexer or scraper; when
	// present they replace parsed languages, like *arr's indexer languages.
	ProvidedLanguages []Language
	IsAnime           bool
	// EpisodeTitles are the names of the episodes a Sonarr release is for:
	// a language word in one ("The Chinese Woman") is not the release's.
	EpisodeTitles []string
}

// withoutEpisodeTitleLanguages re-parses a Sonarr release's languages
// without the episode title in it when that title names a language, like
// Sonarr's AggregateLanguages.
func withoutEpisodeTitleLanguages(r Release, titles []string) Release {
	if r.App != AppSonarr {
		return r
	}
	for _, et := range titles {
		norm := normalizeTitleWords(et)
		if norm == "" || len(parseLanguages(releaseTokens(et, nil))) == 0 {
			continue
		}
		words := normalizeTitleWords(r.Title)
		if i := strings.Index(" "+words+" ", " "+norm+" "); i >= 0 {
			rest := words[:i] + words[min(len(words), i+len(norm)):]
			r.Languages = parseLanguages(releaseTokens(rest, nil))
			if len(r.Languages) == 0 {
				r.Languages = []Language{LanguageUnknown}
			}
			return r
		}
	}
	return r
}

// normalizeTitleWords lowercases a title and separates its words by single
// spaces.
func normalizeTitleWords(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(c rune) bool {
		return c == '.' || c == '_' || c == ' ' || c == '-' || c == ',' || c == ':' || c == '\'' || c == '!' || c == '?'
	}), " ")
}

// Prepare finalizes a parsed release for evaluation: aggregates languages
// with the original language, applies multi-language settings and records
// the runtime.
func (e *Evaluator) Prepare(r Release, ctx ReleaseContext) Release {
	// Like *arr's AggregateLanguages: languages the indexer provides replace
	// the parsed ones, and a MULTi release adds the profile's multi
	// languages on top of either.
	if len(ctx.ProvidedLanguages) > 0 {
		r.Languages = append([]Language(nil), ctx.ProvidedLanguages...)
	} else {
		r = withoutEpisodeTitleLanguages(r, ctx.EpisodeTitles)
	}
	if len(e.profile.MultiLanguages) > 0 && reMultiTag.MatchString(r.Title) {
		langs := append([]Language(nil), r.Languages...)
		if len(langs) == 1 && langs[0] == LanguageUnknown {
			langs = nil
		}
		for _, l := range e.profile.MultiLanguages {
			if !containsLanguage(langs, l) {
				langs = append(langs, l)
			}
		}
		r.Languages = langs
	}
	r.OriginalLanguage = ctx.OriginalLanguage
	r.Languages = FinalizeLanguages(r.Languages, nil, ctx.OriginalLanguage)
	if ctx.RuntimeMinutes > 0 {
		r.RuntimeMinutes = ctx.RuntimeMinutes
	}
	r.IsAnime = ctx.IsAnime
	return r
}

// Evaluate scores a prepared release and decides whether it is acceptable.
func (e *Evaluator) Evaluate(r Release) Evaluation {
	ev := Evaluation{Release: r, Allowed: true}
	rank, ok := e.ranks[strings.ToLower(r.Quality.Name)]
	if !ok || !rank.allowed {
		ev.Allowed = false
		ev.Rejections = append(ev.Rejections, fmt.Sprintf("quality %s is not wanted in profile %q", r.Quality.Name, e.profile.Name))
	}
	if ok {
		ev.QualityRank = rank.position
		ev.QualityGroup = rank.group
	}
	for _, key := range e.formats.MatchKeys(&r, nil) {
		c, _ := e.formats.Get(key)
		score := e.scores[key]
		ev.Score += score
		ev.Formats = append(ev.Formats, MatchedFormat{Key: key, Name: c.Format.Name, Score: score})
	}
	if ev.Score < e.profile.MinFormatScore {
		ev.Allowed = false
		ev.Rejections = append(ev.Rejections, fmt.Sprintf("custom format score %d is below the minimum %d", ev.Score, e.profile.MinFormatScore))
	}
	if reason := e.languageRejection(r); reason != "" {
		ev.Allowed = false
		ev.Rejections = append(ev.Rejections, reason)
	}
	if e.profile.EnforceSizeLimits {
		if reason := e.sizeRejection(r); reason != "" {
			ev.Allowed = false
			ev.Rejections = append(ev.Rejections, reason)
		}
	}
	return ev
}

// Explain returns per-condition results for every scored format.
func (e *Evaluator) Explain(r Release) []FormatResult {
	out := make([]FormatResult, 0, e.formats.Len())
	for _, c := range e.formats.Formats() {
		out = append(out, c.Explain(&r))
	}
	return out
}

// Score returns a format's score in the profile.
func (e *Evaluator) Score(key string) int { return e.scores[key] }

func (e *Evaluator) languageRejection(r Release) string {
	want := e.profile.Language
	if want == "" || want == LanguageAny || e.profile.App != AppRadarr {
		return ""
	}
	target := want
	if want == LanguageOriginal {
		target = r.OriginalLanguage
		if target == "" || target == LanguageUnknown {
			return ""
		}
	}
	if containsLanguage(r.Languages, target) {
		return ""
	}
	return fmt.Sprintf("language %s is required by profile %q", target, e.profile.Name)
}

func (e *Evaluator) sizeLimit(q Quality) (SizeLimit, bool) {
	if e.profile.SizeLimits != nil {
		if l, ok := e.profile.SizeLimits[q.Name]; ok {
			return l, true
		}
	}
	return SizeLimit{}, false
}

func (e *Evaluator) runtimeFor(r Release) int {
	runtime := r.RuntimeMinutes
	if runtime <= 0 {
		return 0
	}
	if e.profile.App == AppSonarr {
		switch {
		case r.EpisodeCount > 1:
			runtime *= r.EpisodeCount
		case r.FullSeason:
			// A season pack of unknown length: its size can't be judged.
			return 0
		}
	}
	return runtime
}

func (e *Evaluator) sizeRejection(r Release) string {
	limit, ok := e.sizeLimit(r.Quality)
	runtime := e.runtimeFor(r)
	if !ok || runtime <= 0 || r.Size <= 0 {
		return ""
	}
	sizeMB := float64(r.Size) / (1024 * 1024)
	if limit.Min > 0 && sizeMB < limit.Min*float64(runtime) {
		return fmt.Sprintf("%.0f MB is below the %s minimum of %.0f MB for %d minutes", sizeMB, r.Quality.Name, limit.Min*float64(runtime), runtime)
	}
	if limit.Max != nil && *limit.Max > 0 && sizeMB > *limit.Max*float64(runtime) {
		return fmt.Sprintf("%.0f MB is above the %s maximum of %.0f MB for %d minutes", sizeMB, r.Quality.Name, *limit.Max*float64(runtime), runtime)
	}
	return ""
}

// Compare orders two evaluations the way *arr's download decision comparer
// does. It returns a negative number when a should be preferred over b.
// Callers append their own tie-breakers when it returns 0.
func (e *Evaluator) Compare(a, b Evaluation) int {
	return e.CompareWithProtocol(a, b, ProtocolUnknown)
}

// CompareWithProtocol is Compare preferring protocol when the profile names
// none. Releases of different protocols are always ordered by protocol
// (usenet first when neither names one, as *arr's default delay profile):
// they rank by seeders or by age within a protocol only, so comparing
// across protocols by size would not be transitive.
func (e *Evaluator) CompareWithProtocol(a, b Evaluation, protocol Protocol) int {
	// Quality position in the profile (groups rank equally).
	if c := cmpInt(b.QualityRank, a.QualityRank); c != 0 {
		return c
	}
	if e.profile.Propers == PropersPreferAndUpgrade {
		if c := b.Release.Revision.Compare(a.Release.Revision); c != 0 {
			return c
		}
	}
	if c := cmpInt(b.Score, a.Score); c != 0 {
		return c
	}
	p := e.profile.PreferredProtocol
	if p == ProtocolUnknown {
		p = protocol
	}
	if p == ProtocolUnknown {
		p = ProtocolUsenet
	}
	if ap, bp := a.Release.Protocol == p, b.Release.Protocol == p; ap != bp {
		if ap {
			return -1
		}
		return 1
	}
	if e.profile.App == AppSonarr {
		if a.Release.FullSeason != b.Release.FullSeason {
			if a.Release.FullSeason {
				return -1
			}
			return 1
		}
		ac, bc := a.Release.EpisodeCount, b.Release.EpisodeCount
		if ac != bc && ac > 0 && bc > 0 {
			// Anime prefers more episodes per release, everything else fewer.
			if a.Release.IsAnime && b.Release.IsAnime {
				return cmpInt(bc, ac)
			}
			return cmpInt(ac, bc)
		}
	}
	if a.Release.Protocol == ProtocolTorrent && b.Release.Protocol == ProtocolTorrent {
		if c := cmpFloat(logBucket(b.Release.Seeders), logBucket(a.Release.Seeders)); c != 0 {
			return c
		}
		if c := cmpFloat(logBucket(b.Release.Peers), logBucket(a.Release.Peers)); c != 0 {
			return c
		}
	}
	if a.Release.Protocol == ProtocolUsenet && b.Release.Protocol == ProtocolUsenet {
		if c := cmpFloat(ageBucket(e.profile.App, b.Release.PublishDate), ageBucket(e.profile.App, a.Release.PublishDate)); c != 0 {
			return c
		}
	}
	return cmpInt64(e.sizeRankValue(b.Release), e.sizeRankValue(a.Release))
}

const sizeRound = 200 * 1024 * 1024

// roundTo truncates toward zero, like *arr's Fluent.Round.
func roundTo(v, step int64) int64 {
	return v / step * step
}

// sizeRankValue is larger for better sizes: closest to the preferred size
// when one is defined and the runtime is known, otherwise the largest. As in
// *arr's comparer the preferred size is for the title's (episode) runtime,
// not scaled by episode count (the size limits are).
func (e *Evaluator) sizeRankValue(r Release) int64 {
	limit, ok := e.sizeLimit(r.Quality)
	runtime := r.RuntimeMinutes
	if ok && limit.Preferred != nil && runtime > 0 {
		preferred := int64(*limit.Preferred * float64(runtime) * 1024 * 1024)
		diff := r.Size - preferred
		if diff < 0 {
			diff = -diff
		}
		return -roundTo(diff, sizeRound)
	}
	return roundTo(r.Size, sizeRound)
}

func logBucket(v *int) float64 {
	if v == nil || *v <= 0 {
		return 0
	}
	return math.Round(math.Log10(float64(*v)))
}

// ageBucket rewards fresh usenet posts like *arr: <1h, <=24h, <=7d, then
// (Radarr only; Sonarr ranks older posts alike) by order of magnitude of
// days.
func ageBucket(app App, published time.Time) float64 {
	if published.IsZero() {
		return math.Inf(-1)
	}
	age := time.Since(published)
	switch {
	case age < time.Hour:
		return 1000
	case age <= 24*time.Hour:
		return 100
	case age <= 7*24*time.Hour:
		return 10
	}
	// *arr's Age is in whole days (TimeSpan.Days).
	days := math.Floor(age.Hours() / 24)
	if days <= 7 {
		return 10
	}
	if app == AppSonarr {
		return 1
	}
	return -math.Round(math.Log10(days))
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// SortEvaluations orders evaluations best first: allowed releases before
// rejected ones (like *arr's decision prioritization), each by Compare; tie
// keeps the input order.
func (e *Evaluator) SortEvaluations(evals []Evaluation) {
	sort.SliceStable(evals, func(i, j int) bool {
		if evals[i].Allowed != evals[j].Allowed {
			return evals[i].Allowed
		}
		return e.Compare(evals[i], evals[j]) < 0
	})
}

// QualityOrder returns the profile's qualities grouped as configured, most
// preferred first, for display.
func (e *Evaluator) QualityOrder() []ProfileItem {
	return append([]ProfileItem(nil), e.profile.Items...)
}
