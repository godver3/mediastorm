// Package skipsegments resolves intro/recap/outro skip ranges for playback by
// merging IntroDB, SkipDB and embedded chapter names. The merge rules are a
// direct port of the former client resolver (frontend/hooks/skipSegmentResolver.ts)
// so every client gets identical boundaries.
package skipsegments

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Segment types. "outro" is the end-credits range; "preview" is only supplied by SkipDB.
const (
	TypeIntro   = "intro"
	TypeRecap   = "recap"
	TypeOutro   = "outro"
	TypePreview = "preview"
)

// Segment sources, in priority order for each type.
const (
	SourceIntroDB          = "introdb"
	SourceSkipDB           = "skipdb"
	SourceChapterExplicit  = "chapter-explicit"
	SourceChapterHeuristic = "chapter-heuristic"
)

// Segment is a normalized skip range. Start and End are seconds from the start of the media.
type Segment struct {
	Type   string  `json:"type"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Source string  `json:"source"`
}

// Chapter is an embedded chapter as reported by the client player, in seconds.
// End is optional; a missing or non-increasing end falls back to the next chapter's start, then the duration.
type Chapter struct {
	Title string   `json:"title"`
	Start float64  `json:"start"`
	End   *float64 `json:"end,omitempty"`
}

// ProviderSegment is a raw IntroDB/SkipDB range in milliseconds.
type ProviderSegment struct {
	StartMS         *float64 `json:"start_ms"`
	EndMS           *float64 `json:"end_ms"`
	Confidence      float64  `json:"confidence,omitempty"`
	SubmissionCount int      `json:"submission_count,omitempty"`
	// Match is SkipDB-only: exact, shifted, agnostic or out-of-range.
	Match string `json:"match,omitempty"`
}

// IntroDBResponse is the api.introdb.app /segments payload.
type IntroDBResponse struct {
	Intro *ProviderSegment `json:"intro"`
	Recap *ProviderSegment `json:"recap"`
	Outro *ProviderSegment `json:"outro"`
}

// SkipDBResponse is the api.skipdb.tv /api/segments payload.
type SkipDBResponse struct {
	Segments struct {
		Intro   *ProviderSegment `json:"intro"`
		Recap   *ProviderSegment `json:"recap"`
		Outro   *ProviderSegment `json:"outro"`
		Preview *ProviderSegment `json:"preview"`
	} `json:"segments"`
}

var (
	introTitles        = setOf("intro", "opening credits", "opening titles", "opening title sequence", "title sequence", "op")
	recapTitles        = setOf("recap", "previously", "previously on")
	outroTitles        = setOf("outro", "credits", "end credits", "ending credits", "closing credits", "closing titles", "end titles", "ed")
	leadingNumberRe    = regexp.MustCompile(`^\s*\d+\s*[-.:)]\s*`)
	nonAlphanumericRe  = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	genericChapterRe   = regexp.MustCompile(`^chapter \d+$`)
	resolvedTypeOrder  = []string{TypeIntro, TypeRecap, TypeOutro}
	providerTypeOrders = []string{TypeIntro, TypeRecap, TypeOutro, TypePreview}
)

func setOf(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func normalizeChapterTitle(title string) string {
	normalized := strings.ToLower(strings.TrimSpace(title))
	normalized = leadingNumberRe.ReplaceAllString(normalized, "")
	normalized = nonAlphanumericRe.ReplaceAllString(normalized, " ")
	return strings.TrimSpace(normalized)
}

func classifyChapterTitle(title string) string {
	normalized := normalizeChapterTitle(title)
	if _, ok := introTitles[normalized]; ok {
		return TypeIntro
	}
	if _, ok := recapTitles[normalized]; ok || strings.HasPrefix(normalized, "previously on ") {
		return TypeRecap
	}
	if _, ok := outroTitles[normalized]; ok {
		return TypeOutro
	}
	return ""
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// chapterEnd returns the chapter's end time and whether one could be determined.
func chapterEnd(chapters []Chapter, index int, duration float64) (float64, bool) {
	chapter := chapters[index]
	if chapter.End != nil && finite(*chapter.End) && *chapter.End > chapter.Start {
		return *chapter.End, true
	}
	if index+1 < len(chapters) {
		next := chapters[index+1].Start
		if finite(next) && next > chapter.Start {
			return next, true
		}
	}
	if finite(duration) && duration > chapter.Start {
		return duration, true
	}
	return 0, false
}

// ValidProviderRange validates a provider range (milliseconds) against the media
// duration (seconds, 0 = unknown) and clamps its end to the duration. SkipDB
// "out-of-range" matches are rejected.
func ValidProviderRange(source string, segment *ProviderSegment, duration float64) (startMS, endMS float64, ok bool) {
	if segment == nil || segment.StartMS == nil || segment.EndMS == nil {
		return 0, 0, false
	}
	if source == SourceSkipDB && segment.Match == "out-of-range" {
		return 0, 0, false
	}
	startMS, endMS = *segment.StartMS, *segment.EndMS
	if !finite(startMS) || !finite(endMS) || startMS < 0 || endMS <= startMS {
		return 0, 0, false
	}
	if duration = SanitizeDuration(duration); duration > 0 {
		durationMS := duration * 1000
		if startMS >= durationMS {
			return 0, 0, false
		}
		endMS = math.Min(endMS, durationMS)
	}
	if endMS <= startMS {
		return 0, 0, false
	}
	return startMS, endMS, true
}

// maxOutroTailSeconds is how far before the end of the file a provider's end
// credits may finish (a post-credits tag or "next time" preview) before the range
// is taken to belong to a different cut of the episode.
const maxOutroTailSeconds = 180

// OutroFitsDuration reports whether a provider outro ending at endMS can be this
// file's end credits. IntroDB keys ranges by episode, not by cut, so an outro
// submitted for a broadcast cut lands mid-scene in a longer extended cut and,
// with credits auto-skip on, plays the next episode with minutes left. An unknown
// duration (0) accepts the range.
func OutroFitsDuration(endMS, duration float64) bool {
	duration = SanitizeDuration(duration)
	return duration <= 0 || duration-endMS/1000 <= maxOutroTailSeconds
}

func providerSegment(segmentType, source string, segment *ProviderSegment, duration float64) *Segment {
	startMS, endMS, ok := ValidProviderRange(source, segment, duration)
	if !ok {
		return nil
	}
	if segmentType == TypeOutro && !OutroFitsDuration(endMS, duration) {
		return nil
	}
	return &Segment{Type: segmentType, Start: startMS / 1000, End: endMS / 1000, Source: source}
}

func introDBSegments(response *IntroDBResponse, duration float64) map[string]*Segment {
	result := map[string]*Segment{}
	if response == nil {
		return result
	}
	result[TypeIntro] = providerSegment(TypeIntro, SourceIntroDB, response.Intro, duration)
	result[TypeRecap] = providerSegment(TypeRecap, SourceIntroDB, response.Recap, duration)
	result[TypeOutro] = providerSegment(TypeOutro, SourceIntroDB, response.Outro, duration)
	return result
}

func skipDBSegments(response *SkipDBResponse, duration float64) map[string]*Segment {
	result := map[string]*Segment{}
	if response == nil {
		return result
	}
	result[TypeIntro] = providerSegment(TypeIntro, SourceSkipDB, response.Segments.Intro, duration)
	result[TypeRecap] = providerSegment(TypeRecap, SourceSkipDB, response.Segments.Recap, duration)
	result[TypeOutro] = providerSegment(TypeOutro, SourceSkipDB, response.Segments.Outro, duration)
	result[TypePreview] = providerSegment(TypePreview, SourceSkipDB, response.Segments.Preview, duration)
	return result
}

// IntroDBComplete reports whether IntroDB alone supplies a valid intro, recap and outro,
// in which case SkipDB is not consulted.
func IntroDBComplete(response *IntroDBResponse, duration float64) bool {
	segments := introDBSegments(response, duration)
	return segments[TypeIntro] != nil && segments[TypeRecap] != nil && segments[TypeOutro] != nil
}

func explicitChapterSegments(chapters []Chapter, duration float64) map[string]*Segment {
	resolved := map[string]*Segment{}
	for index, chapter := range chapters {
		segmentType := classifyChapterTitle(chapter.Title)
		if segmentType == "" || resolved[segmentType] != nil {
			continue
		}
		end, ok := chapterEnd(chapters, index, duration)
		if !ok || end <= chapter.Start {
			continue
		}
		// A bare "Credits" marker near the beginning is more likely opening credits.
		// Require ambiguous outro labels to occur in the latter half of the media.
		normalized := normalizeChapterTitle(chapter.Title)
		if segmentType == TypeOutro && (normalized == "credits" || normalized == "ed") && chapter.Start < duration*0.5 {
			continue
		}
		resolved[segmentType] = &Segment{Type: segmentType, Start: chapter.Start, End: end, Source: SourceChapterExplicit}
	}
	return resolved
}

func isGenericIntroCandidateTitle(title string) bool {
	normalized := normalizeChapterTitle(title)
	return normalized == "opening" || normalized == "title" || genericChapterRe.MatchString(normalized)
}

func heuristicIntroSegment(chapters []Chapter, duration float64) *Segment {
	for index := 0; index < len(chapters)-1; index++ {
		chapter := chapters[index]
		if !isGenericIntroCandidateTitle(chapter.Title) {
			continue
		}
		end, ok := chapterEnd(chapters, index, duration)
		followingEnd, followingOK := chapterEnd(chapters, index+1, duration)
		if !ok || !followingOK {
			continue
		}
		segmentDuration := end - chapter.Start
		followingDuration := followingEnd - chapters[index+1].Start
		if chapter.Start >= 15 && chapter.Start < 240 && end <= 300 &&
			segmentDuration >= 20 && segmentDuration <= 60 &&
			followingDuration >= math.Max(120, segmentDuration*2) {
			return &Segment{Type: TypeIntro, Start: chapter.Start, End: end, Source: SourceChapterHeuristic}
		}
	}
	return nil
}

func sanitizeChapters(chapters []Chapter) []Chapter {
	clean := make([]Chapter, 0, len(chapters))
	for _, chapter := range chapters {
		if !finite(chapter.Start) {
			continue
		}
		clean = append(clean, chapter)
	}
	return clean
}

// SanitizeDuration maps NaN, infinite and negative durations to 0 (unknown).
func SanitizeDuration(duration float64) float64 {
	if !finite(duration) || duration < 0 {
		return 0
	}
	return duration
}

// Resolve merges each segment type independently: IntroDB, then SkipDB, then
// explicitly named chapters, then (intro only) the conservative chapter heuristic.
// Preview ranges come from SkipDB only. duration is in seconds (0 = unknown).
// The result is ordered by start time.
func Resolve(introDB *IntroDBResponse, skipDB *SkipDBResponse, chapters []Chapter, duration float64) []Segment {
	duration = SanitizeDuration(duration)
	chapters = sanitizeChapters(chapters)
	fromIntroDB := introDBSegments(introDB, duration)
	fromSkipDB := skipDBSegments(skipDB, duration)
	fromChapters := explicitChapterSegments(chapters, duration)

	segments := make([]Segment, 0, len(providerTypeOrders))
	for _, segmentType := range resolvedTypeOrder {
		candidate := firstSegment(fromIntroDB[segmentType], fromSkipDB[segmentType], fromChapters[segmentType])
		if candidate == nil && segmentType == TypeIntro {
			candidate = heuristicIntroSegment(chapters, duration)
		}
		if candidate != nil {
			segments = append(segments, *candidate)
		}
	}
	if preview := fromSkipDB[TypePreview]; preview != nil {
		segments = append(segments, *preview)
	}
	sort.SliceStable(segments, func(i, j int) bool { return segments[i].Start < segments[j].Start })
	return segments
}

func firstSegment(candidates ...*Segment) *Segment {
	for _, candidate := range candidates {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}
