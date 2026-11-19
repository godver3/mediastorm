package arr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dlclark/regexp2"
)

// Specification implementation names, as used by Sonarr/Radarr and TRaSH.
const (
	SpecReleaseTitle    = "ReleaseTitleSpecification"
	SpecReleaseGroup    = "ReleaseGroupSpecification"
	SpecEdition         = "EditionSpecification"
	SpecLanguage        = "LanguageSpecification"
	SpecSource          = "SourceSpecification"
	SpecResolution      = "ResolutionSpecification"
	SpecQualityModifier = "QualityModifierSpecification"
	SpecIndexerFlag     = "IndexerFlagSpecification"
	SpecReleaseType     = "ReleaseTypeSpecification"
	SpecSize            = "SizeSpecification"
	SpecYear            = "YearSpecification"
)

// SpecImplementations lists every supported implementation per app.
var SpecImplementations = map[App][]string{
	AppSonarr: {SpecReleaseTitle, SpecLanguage, SpecIndexerFlag, SpecSource, SpecResolution, SpecSize, SpecReleaseGroup, SpecReleaseType},
	AppRadarr: {SpecReleaseTitle, SpecEdition, SpecLanguage, SpecIndexerFlag, SpecSource, SpecResolution, SpecQualityModifier, SpecSize, SpecReleaseGroup, SpecYear},
}

// RegexTimeout bounds a single regex evaluation. .NET-style patterns can
// backtrack catastrophically. A condition that times out is reported and
// resolved conservatively (see CompiledFormat.timeoutMatches).
var RegexTimeout = 100 * time.Millisecond

// CustomFormat is a Sonarr/Radarr custom format. The JSON form accepts both
// the TRaSH-Guides layout (fields as an object) and the *arr API layout
// (fields as a [{name,value}] array) and always writes the TRaSH layout.
type CustomFormat struct {
	TrashID                         string          `json:"trash_id,omitempty"`
	Name                            string          `json:"name"`
	TrashScores                     map[string]int  `json:"trash_scores,omitempty"`
	TrashDescription                string          `json:"trash_description,omitempty"`
	IncludeCustomFormatWhenRenaming bool            `json:"includeCustomFormatWhenRenaming,omitempty"`
	Specifications                  []Specification `json:"specifications"`
}

// Key returns the stable identity of a format: its trash_id when present,
// otherwise its lower-cased name.
func (cf CustomFormat) Key() string {
	if cf.TrashID != "" {
		return cf.TrashID
	}
	return "name:" + strings.ToLower(strings.TrimSpace(cf.Name))
}

// Specification is one condition of a custom format.
type Specification struct {
	Name           string         `json:"name"`
	Implementation string         `json:"implementation"`
	Negate         bool           `json:"negate"`
	Required       bool           `json:"required"`
	Fields         map[string]any `json:"fields"`
}

type specificationJSON struct {
	Name           string          `json:"name"`
	Implementation string          `json:"implementation"`
	Negate         bool            `json:"negate"`
	Required       bool            `json:"required"`
	Fields         json.RawMessage `json:"fields"`
}

// UnmarshalJSON accepts fields as an object or as an *arr field array.
func (s *Specification) UnmarshalJSON(data []byte) error {
	var raw specificationJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Name = raw.Name
	s.Implementation = raw.Implementation
	s.Negate = raw.Negate
	s.Required = raw.Required
	s.Fields = map[string]any{}
	trimmed := bytes.TrimSpace(raw.Fields)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	dec := func(target any) error {
		d := json.NewDecoder(bytes.NewReader(trimmed))
		d.UseNumber()
		return d.Decode(target)
	}
	if trimmed[0] == '[' {
		var arr []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		}
		if err := dec(&arr); err != nil {
			return fmt.Errorf("specification %q fields: %w", raw.Name, err)
		}
		for _, field := range arr {
			s.Fields[field.Name] = field.Value
		}
		return nil
	}
	if err := dec(&s.Fields); err != nil {
		return fmt.Errorf("specification %q fields: %w", raw.Name, err)
	}
	return nil
}

func fieldString(fields map[string]any, name string) string {
	switch v := fields[name].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func fieldFloat(fields map[string]any, name string) (float64, bool) {
	switch v := fields[name].(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		var f float64
		_, err := fmt.Sscanf(strings.TrimSpace(v), "%g", &f)
		return f, err == nil
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func fieldInt(fields map[string]any, name string) (int, bool) {
	v, ok := fieldFloat(fields, name)
	if !ok || v != math.Trunc(v) {
		return 0, false
	}
	return int(v), true
}

func fieldBool(fields map[string]any, name string) bool {
	switch v := fields[name].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	case json.Number:
		return v.String() != "0"
	}
	return false
}

// CompiledFormat is a custom format ready for evaluation.
type CompiledFormat struct {
	Format CustomFormat
	groups []specGroup
	// Unsupported lists conditions this engine cannot evaluate faithfully
	// (for example indexer flags on usenet); they count as "not matched".
	Unsupported []string
	// timeoutMatches decides how a condition that could not be evaluated
	// (regex timeout) counts. It is false by default so a timeout never
	// awards a format; NewEvaluator sets it for negatively scored formats so
	// that a blocker such as BR-DISK keeps blocking when its regex times out
	// on a pathological title or an overloaded host.
	timeoutMatches bool
}

type specGroup struct {
	implementation string
	specs          []compiledSpec
}

type compiledSpec struct {
	spec     Specification
	eval     func(*Release) (bool, error)
	describe string
}

// CompileError reports why a custom format cannot be used.
type CompileError struct {
	Format    string
	Condition string
	Err       error
}

func (e *CompileError) Error() string {
	if e.Condition != "" {
		return fmt.Sprintf("custom format %q, condition %q: %v", e.Format, e.Condition, e.Err)
	}
	return fmt.Sprintf("custom format %q: %v", e.Format, e.Err)
}

func (e *CompileError) Unwrap() error { return e.Err }

// CompileRegex compiles a .NET-compatible, case-insensitive pattern exactly
// like Sonarr/Radarr do for release title, group and edition conditions.
func CompileRegex(pattern string) (*regexp2.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("regular expression must not be empty")
	}
	re, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = RegexTimeout
	return re, nil
}

// matchFailures counts conditions that could not be evaluated (a regex
// that ran into RegexTimeout). Such a condition counts as not matched, or as
// matched for negatively scored formats; the failures are logged at most
// once a minute.
var matchFailures struct {
	count  atomic.Int64
	logged atomic.Int64 // unix time of the last log line
}

// MatchFailures returns how many conditions could not be evaluated.
func MatchFailures() int64 { return matchFailures.count.Load() }

func reportMatchFailure(format string, err error) {
	n := matchFailures.count.Add(1)
	now := time.Now().Unix()
	last := matchFailures.logged.Load()
	if now-last >= 60 && matchFailures.logged.CompareAndSwap(last, now) {
		log.Printf("[arr] custom format %q: a condition could not be evaluated: %v (%d so far)", format, err, n)
	}
}

// Compile validates a custom format for an app and prepares it for matching.
func Compile(app App, cf CustomFormat) (*CompiledFormat, error) {
	if strings.TrimSpace(cf.Name) == "" {
		return nil, &CompileError{Format: cf.Name, Err: fmt.Errorf("name is required")}
	}
	compiled := &CompiledFormat{Format: cf}
	byImpl := map[string]int{}
	for _, spec := range cf.Specifications {
		cs, unsupported, err := compileSpec(app, spec)
		if err != nil {
			return nil, &CompileError{Format: cf.Name, Condition: spec.Name, Err: err}
		}
		if unsupported != "" {
			compiled.Unsupported = append(compiled.Unsupported, fmt.Sprintf("%s (%s)", spec.Name, unsupported))
		}
		idx, ok := byImpl[spec.Implementation]
		if !ok {
			idx = len(compiled.groups)
			byImpl[spec.Implementation] = idx
			compiled.groups = append(compiled.groups, specGroup{implementation: spec.Implementation})
		}
		compiled.groups[idx].specs = append(compiled.groups[idx].specs, cs)
	}
	return compiled, nil
}

func compileSpec(app App, spec Specification) (compiledSpec, string, error) {
	cs := compiledSpec{spec: spec}
	supported := false
	for _, impl := range SpecImplementations[app] {
		if impl == spec.Implementation {
			supported = true
			break
		}
	}
	if !supported {
		return cs, "", fmt.Errorf("implementation %q is not available for %s", spec.Implementation, app)
	}
	unsupported := ""
	switch spec.Implementation {
	case SpecReleaseTitle, SpecReleaseGroup, SpecEdition:
		pattern := fieldString(spec.Fields, "value")
		re, err := CompileRegex(pattern)
		if err != nil {
			return cs, "", fmt.Errorf("invalid regular expression: %w", err)
		}
		impl := spec.Implementation
		cs.describe = fmt.Sprintf("%s matches /%s/", map[string]string{SpecReleaseTitle: "title", SpecReleaseGroup: "group", SpecEdition: "edition"}[impl], pattern)
		cs.eval = func(r *Release) (bool, error) {
			switch impl {
			case SpecReleaseTitle:
				ok, err := matchRegex(re, r.Title)
				if ok || err != nil {
					return ok, err
				}
				return matchRegex(re, r.Filename)
			case SpecReleaseGroup:
				return matchRegex(re, r.ReleaseGroup)
			default:
				return matchRegex(re, r.Edition)
			}
		}
	case SpecLanguage:
		value, ok := fieldInt(spec.Fields, "value")
		if !ok {
			return cs, "", fmt.Errorf("language value must be a number")
		}
		lang, ok := LanguageFromID(app, value)
		if !ok {
			return cs, "", fmt.Errorf("invalid language condition value: %d", value)
		}
		except := fieldBool(spec.Fields, "exceptLanguage")
		cs.describe = "language is " + string(lang)
		if except {
			cs.describe = "has a language other than " + string(lang)
		}
		cs.eval = func(r *Release) (bool, error) {
			compared := lang
			if lang == LanguageOriginal && r.OriginalLanguage != "" && r.OriginalLanguage != LanguageUnknown {
				compared = r.OriginalLanguage
			}
			var match bool
			if lang == LanguageAny && !except {
				match = len(r.Languages) > 0
			} else if except {
				for _, l := range r.Languages {
					if l != compared {
						match = true
						break
					}
				}
			} else {
				match = containsLanguage(r.Languages, compared)
			}
			return match, nil
		}
	case SpecSource:
		value, ok := fieldInt(spec.Fields, "value")
		if !ok || value < 0 || (app == AppSonarr && value > 7) || (app == AppRadarr && value > 9) {
			return cs, "", fmt.Errorf("invalid source condition value: %v", spec.Fields["value"])
		}
		cs.describe = "source is " + SourceName(app, Source(value))
		cs.eval = func(r *Release) (bool, error) { return r.Quality.Source == Source(value), nil }
	case SpecResolution:
		value, ok := fieldInt(spec.Fields, "value")
		if !ok || !validResolution(value) {
			return cs, "", fmt.Errorf("invalid resolution condition value: %v", spec.Fields["value"])
		}
		cs.describe = fmt.Sprintf("resolution is %dp", value)
		cs.eval = func(r *Release) (bool, error) { return r.Quality.Resolution == value, nil }
	case SpecQualityModifier:
		value, ok := fieldInt(spec.Fields, "value")
		if !ok || value < 0 || value > 5 {
			return cs, "", fmt.Errorf("invalid quality modifier condition value: %v", spec.Fields["value"])
		}
		cs.describe = "modifier is " + ModifierName(Modifier(value))
		cs.eval = func(r *Release) (bool, error) { return r.Quality.Modifier == Modifier(value), nil }
	case SpecIndexerFlag:
		value, ok := fieldInt(spec.Fields, "value")
		if !ok || value <= 0 || !validIndexerFlag(app, value) {
			return cs, "", fmt.Errorf("invalid indexer flag condition value: %v", spec.Fields["value"])
		}
		cs.describe = "indexer flag " + IndexerFlagName(app, value)
		unsupported = "indexer flags are only known for some torrent sources"
		cs.eval = func(r *Release) (bool, error) { return r.IndexerFlags&value == value, nil }
	case SpecReleaseType:
		value, ok := fieldInt(spec.Fields, "value")
		if !ok || value < 0 || value > 3 {
			return cs, "", fmt.Errorf("invalid release type condition value: %v", spec.Fields["value"])
		}
		cs.describe = "release type is " + ReleaseType(value).String()
		cs.eval = func(r *Release) (bool, error) { return r.ReleaseType == ReleaseType(value), nil }
	case SpecSize:
		minGB, _ := fieldFloat(spec.Fields, "min")
		maxGB, ok := fieldFloat(spec.Fields, "max")
		if !ok || minGB < 0 || maxGB <= minGB {
			return cs, "", fmt.Errorf("size condition needs 0 <= min < max (got min=%v max=%v)", spec.Fields["min"], spec.Fields["max"])
		}
		minBytes := int64(minGB * 1024 * 1024 * 1024)
		maxBytes := int64(math.Min(maxGB*1024*1024*1024, math.MaxInt64))
		cs.describe = fmt.Sprintf("size is above %g GB and at most %g GB", minGB, maxGB)
		cs.eval = func(r *Release) (bool, error) { return r.Size > minBytes && r.Size <= maxBytes, nil }
	case SpecYear:
		minYear, _ := fieldInt(spec.Fields, "min")
		maxYear, ok := fieldInt(spec.Fields, "max")
		if !ok || maxYear < minYear {
			return cs, "", fmt.Errorf("year condition needs min <= max")
		}
		cs.describe = fmt.Sprintf("year between %d and %d", minYear, maxYear)
		cs.eval = func(r *Release) (bool, error) { return r.Year >= minYear && r.Year <= maxYear, nil }
	}
	return cs, unsupported, nil
}

func validResolution(v int) bool {
	for _, r := range Resolutions {
		if r == v {
			return true
		}
	}
	return false
}

func matchRegex(re *regexp2.Regexp, s string) (bool, error) {
	// Bound the backtracking work like Parse bounds the parsed title; this
	// also covers Filename and releases not built by Parse.
	s = capTitle(s)
	if s == "" {
		return false, nil
	}
	ok, err := re.MatchString(s)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// ConditionResult explains one evaluated condition.
type ConditionResult struct {
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	Description    string `json:"description"`
	Negate         bool   `json:"negate,omitempty"`
	Required       bool   `json:"required,omitempty"`
	Matched        bool   `json:"matched"`
	Error          string `json:"error,omitempty"`
}

// FormatResult explains one evaluated custom format.
type FormatResult struct {
	Key        string            `json:"key"`
	Name       string            `json:"name"`
	Matched    bool              `json:"matched"`
	Conditions []ConditionResult `json:"conditions,omitempty"`
}

// Matches evaluates the format with *arr semantics: conditions are grouped by
// implementation; a group passes when every required condition is satisfied
// and at least one condition is satisfied; the format matches when all groups
// pass. Negate is applied per condition before grouping.
func (c *CompiledFormat) Matches(r *Release) bool {
	matched, _ := c.evaluate(r, false)
	return matched
}

// Explain evaluates the format and returns per-condition details.
func (c *CompiledFormat) Explain(r *Release) FormatResult {
	matched, conditions := c.evaluate(r, true)
	return FormatResult{Key: c.Format.Key(), Name: c.Format.Name, Matched: matched, Conditions: conditions}
}

func (c *CompiledFormat) evaluate(r *Release, explain bool) (bool, []ConditionResult) {
	var details []ConditionResult
	if len(c.groups) == 0 {
		// An empty custom format matches everything in *arr.
		return true, nil
	}
	all := true
	for _, g := range c.groups {
		anyMatch := false
		requiredFailed := false
		for _, cs := range g.specs {
			raw, err := cs.eval(r)
			if err != nil {
				reportMatchFailure(c.Format.Name, err)
			}
			match := raw
			if cs.spec.Negate {
				match = !raw
			}
			if err != nil {
				// Never let a timeout award a format, but never let it
				// silently lift a penalty either.
				match = c.timeoutMatches
			}
			if match {
				anyMatch = true
			} else if cs.spec.Required {
				requiredFailed = true
			}
			if explain {
				cr := ConditionResult{Name: cs.spec.Name, Implementation: cs.spec.Implementation, Description: cs.describe,
					Negate: cs.spec.Negate, Required: cs.spec.Required, Matched: match}
				if err != nil {
					cr.Error = err.Error()
				}
				details = append(details, cr)
			} else if !all {
				break
			}
		}
		if requiredFailed || !anyMatch {
			all = false
			if !explain {
				return false, nil
			}
		}
	}
	return all, details
}

// FormatSet is a set of compiled custom formats evaluated together.
type FormatSet struct {
	formats []*CompiledFormat
	byKey   map[string]*CompiledFormat
}

// NewFormatSet compiles formats for an app. Formats that fail to compile are
// returned as errors and left out of the set.
func NewFormatSet(app App, formats []CustomFormat) (*FormatSet, []error) {
	set := &FormatSet{byKey: map[string]*CompiledFormat{}}
	var errs []error
	for _, cf := range formats {
		compiled, err := Compile(app, cf)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		key := cf.Key()
		if _, dup := set.byKey[key]; dup {
			errs = append(errs, &CompileError{Format: cf.Name, Err: fmt.Errorf("duplicate custom format %q", key)})
			continue
		}
		set.byKey[key] = compiled
		set.formats = append(set.formats, compiled)
	}
	sort.SliceStable(set.formats, func(i, j int) bool { return set.formats[i].Format.Name < set.formats[j].Format.Name })
	return set, errs
}

// Len returns the number of formats.
func (s *FormatSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.formats)
}

// Get returns the compiled format with a key.
func (s *FormatSet) Get(key string) (*CompiledFormat, bool) {
	if s == nil {
		return nil, false
	}
	c, ok := s.byKey[key]
	return c, ok
}

// Formats returns the compiled formats ordered by name.
func (s *FormatSet) Formats() []*CompiledFormat {
	if s == nil {
		return nil
	}
	return s.formats
}

// MatchKeys returns the keys of the formats matching a release, ordered by
// name like *arr does. Only formats whose key is in limit are evaluated when
// limit is non-nil.
func (s *FormatSet) MatchKeys(r *Release, limit map[string]int) []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, c := range s.formats {
		key := c.Format.Key()
		if limit != nil {
			if _, ok := limit[key]; !ok {
				continue
			}
		}
		if c.Matches(r) {
			out = append(out, key)
		}
	}
	return out
}
