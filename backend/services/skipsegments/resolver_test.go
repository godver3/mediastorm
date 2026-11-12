package skipsegments

import (
	"reflect"
	"testing"
)

func ms(value float64) *float64  { return &value }
func sec(value float64) *float64 { return &value }

func providerRange(startMS, endMS float64) *ProviderSegment {
	return &ProviderSegment{StartMS: ms(startMS), EndMS: ms(endMS), Confidence: 0.9, SubmissionCount: 3}
}

func skipRange(startMS, endMS float64, match string) *ProviderSegment {
	return &ProviderSegment{StartMS: ms(startMS), EndMS: ms(endMS), Match: match}
}

func byType(segments []Segment) map[string]Segment {
	result := map[string]Segment{}
	for _, segment := range segments {
		result[segment.Type] = segment
	}
	return result
}

// The cases below are ported from frontend/hooks/__tests__/skipSegmentResolver.test.ts.

func TestIntroDBAuthoritativeOverConflictingChapters(t *testing.T) {
	chapters := []Chapter{
		{Title: "Opening Credits", Start: 30, End: sec(75)},
		{Title: "End Credits", Start: 1200, End: sec(1320)},
	}
	got := byType(Resolve(&IntroDBResponse{Intro: providerRange(45_000, 95_000), Outro: providerRange(1_180_000, 1_320_000)}, nil, chapters, 1320))
	if want := (Segment{TypeIntro, 45, 95, SourceIntroDB}); got[TypeIntro] != want {
		t.Errorf("intro = %+v, want %+v", got[TypeIntro], want)
	}
	if want := (Segment{TypeOutro, 1180, 1320, SourceIntroDB}); got[TypeOutro] != want {
		t.Errorf("outro = %+v, want %+v", got[TypeOutro], want)
	}
}

func TestChaptersFillMissingIntroDBTypes(t *testing.T) {
	chapters := []Chapter{
		{Title: "Previously on Example", Start: 0, End: sec(35)},
		{Title: "01 - Intro", Start: 35, End: sec(90)},
		{Title: "Chapter 3", Start: 90, End: sec(1200)},
		{Title: "Closing Credits", Start: 1200, End: sec(1320)},
	}
	got := byType(Resolve(&IntroDBResponse{Intro: providerRange(40_000, 100_000)}, nil, chapters, 1320))
	if got[TypeIntro].Source != SourceIntroDB {
		t.Errorf("intro source = %q", got[TypeIntro].Source)
	}
	if want := (Segment{TypeRecap, 0, 35, SourceChapterExplicit}); got[TypeRecap] != want {
		t.Errorf("recap = %+v, want %+v", got[TypeRecap], want)
	}
	if want := (Segment{TypeOutro, 1200, 1320, SourceChapterExplicit}); got[TypeOutro] != want {
		t.Errorf("outro = %+v, want %+v", got[TypeOutro], want)
	}
}

func TestHeuristicIntroFallback(t *testing.T) {
	chapters := []Chapter{
		{Title: "Chapter 1", Start: 0, End: sec(90)},
		{Title: "Chapter 2", Start: 90, End: sec(140)},
		{Title: "Chapter 3", Start: 140, End: sec(1200)},
	}
	got := Resolve(nil, nil, chapters, 1200)
	want := []Segment{{TypeIntro, 90, 140, SourceChapterHeuristic}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("segments = %+v, want %+v", got, want)
	}
}

func TestHeuristicRejectsZeroLongAndNamedChapters(t *testing.T) {
	cases := map[string][]Chapter{
		"at zero": {
			{Title: "Chapter 1", Start: 0, End: sec(50)},
			{Title: "Chapter 2", Start: 50, End: sec(1200)},
		},
		"too long": {
			{Title: "Chapter 1", Start: 0, End: sec(30)},
			{Title: "Chapter 2", Start: 30, End: sec(100)},
			{Title: "Chapter 3", Start: 100, End: sec(1200)},
		},
		"named scene": {
			{Title: "Chapter 1", Start: 0, End: sec(30)},
			{Title: "The Mysterious Visitor", Start: 30, End: sec(80)},
			{Title: "Chapter 3", Start: 80, End: sec(1200)},
		},
	}
	for name, chapters := range cases {
		if got := Resolve(nil, nil, chapters, 1200); len(got) != 0 {
			t.Errorf("%s: segments = %+v, want none", name, got)
		}
	}
}

func TestInvalidIntroDBFallsBackToChapters(t *testing.T) {
	chapters := []Chapter{{Title: "Intro", Start: 30, End: sec(75)}}
	got := byType(Resolve(&IntroDBResponse{Intro: providerRange(90_000, 80_000)}, nil, chapters, 1200))
	if want := (Segment{TypeIntro, 30, 75, SourceChapterExplicit}); got[TypeIntro] != want {
		t.Errorf("intro = %+v, want %+v", got[TypeIntro], want)
	}
}

func TestSkipDBFillsBeforeChapters(t *testing.T) {
	chapters := []Chapter{
		{Title: "Intro", Start: 20, End: sec(70)},
		{Title: "Recap", Start: 0, End: sec(20)},
	}
	skip := &SkipDBResponse{}
	skip.Segments.Intro = skipRange(45_000, 95_000, "exact")
	skip.Segments.Recap = skipRange(0, 35_000, "shifted")
	got := byType(Resolve(&IntroDBResponse{Intro: providerRange(40_000, 90_000)}, skip, chapters, 1200))
	if want := (Segment{TypeIntro, 40, 90, SourceIntroDB}); got[TypeIntro] != want {
		t.Errorf("intro = %+v, want %+v", got[TypeIntro], want)
	}
	if want := (Segment{TypeRecap, 0, 35, SourceSkipDB}); got[TypeRecap] != want {
		t.Errorf("recap = %+v, want %+v", got[TypeRecap], want)
	}
}

func TestOutOfRangeSkipDBUsesChapters(t *testing.T) {
	skip := &SkipDBResponse{}
	skip.Segments.Outro = skipRange(1_100_000, 1_200_000, "out-of-range")
	got := byType(Resolve(nil, skip, []Chapter{{Title: "End Credits", Start: 1100, End: sec(1200)}}, 1200))
	if want := (Segment{TypeOutro, 1100, 1200, SourceChapterExplicit}); got[TypeOutro] != want {
		t.Errorf("outro = %+v, want %+v", got[TypeOutro], want)
	}
}

func TestProviderRangesClampToDuration(t *testing.T) {
	got := byType(Resolve(&IntroDBResponse{Outro: providerRange(1_100_000, 1_400_000), Recap: providerRange(1_300_000, 1_350_000)}, nil, nil, 1200))
	if want := (Segment{TypeOutro, 1100, 1200, SourceIntroDB}); got[TypeOutro] != want {
		t.Errorf("outro = %+v, want %+v", got[TypeOutro], want)
	}
	if _, ok := got[TypeRecap]; ok {
		t.Errorf("recap starting after duration should be dropped: %+v", got[TypeRecap])
	}
	// Unknown duration: no clamping.
	unbounded := byType(Resolve(&IntroDBResponse{Outro: providerRange(1_100_000, 1_400_000)}, nil, nil, 0))
	if unbounded[TypeOutro].End != 1400 {
		t.Errorf("unbounded outro end = %v, want 1400", unbounded[TypeOutro].End)
	}
}

func TestSkipDBPreviewIncludedAndOrdered(t *testing.T) {
	skip := &SkipDBResponse{}
	skip.Segments.Preview = skipRange(1_250_000, 1_300_000, "exact")
	skip.Segments.Recap = skipRange(0, 30_000, "agnostic")
	got := Resolve(nil, skip, nil, 1320)
	want := []Segment{{TypeRecap, 0, 30, SourceSkipDB}, {TypePreview, 1250, 1300, SourceSkipDB}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("segments = %+v, want %+v", got, want)
	}
}

// Credits-chapter cases ported from frontend/hooks/__tests__/useTVPostPlayback.test.tsx.

func TestCreditsChapterDetection(t *testing.T) {
	outro := func(chapters []Chapter) *Segment {
		for _, segment := range Resolve(nil, nil, chapters, 100) {
			if segment.Type == TypeOutro {
				return &segment
			}
		}
		return nil
	}
	if got := outro([]Chapter{{Title: "12. End Credits", Start: 85}}); got == nil || got.Start != 85 || got.End != 100 {
		t.Errorf("numbered end credits = %+v", got)
	}
	if got := outro([]Chapter{{Title: "Chapter 12", Start: 90}}); got != nil {
		t.Errorf("generic last chapter must not be credits: %+v", got)
	}
	got := outro([]Chapter{
		{Title: "Credits", Start: 1, End: sec(5)},
		{Title: "End Credits", Start: 85, End: sec(95)},
		{Title: "Post Credits Scene", Start: 95, End: sec(100)},
	})
	if got == nil || got.Start != 85 || got.End != 95 {
		t.Errorf("early bare Credits / post-credits scene should not win: %+v", got)
	}
}

func TestChapterEndFallsBackToNextStartThenDuration(t *testing.T) {
	got := Resolve(nil, nil, []Chapter{
		{Title: "Recap", Start: 0},
		{Title: "Story", Start: 40, End: sec(30)},
		{Title: "Outro", Start: 1100},
	}, 1200)
	want := []Segment{{TypeRecap, 0, 40, SourceChapterExplicit}, {TypeOutro, 1100, 1200, SourceChapterExplicit}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("segments = %+v, want %+v", got, want)
	}
}

// The Office S01E06: IntroDB's outro (1296–1339 s) is for the ~22 min broadcast
// cut; in the 1909 s extended cut it lands mid-scene. SkipDB, asked with the
// file's duration, marks it out-of-range, so no outro should survive.
func TestProviderOutroFromShorterCutIsRejected(t *testing.T) {
	introDB := &IntroDBResponse{Intro: providerRange(0, 31_000), Outro: providerRange(1_296_000, 1_339_000)}
	skipDB := &SkipDBResponse{}
	skipDB.Segments.Outro = skipRange(1_296_000, 1_339_000, "out-of-range")
	got := byType(Resolve(introDB, skipDB, nil, 1909))
	if outro, ok := got[TypeOutro]; ok {
		t.Errorf("outro = %+v, want none", outro)
	}
	if want := (Segment{TypeIntro, 0, 31, SourceIntroDB}); got[TypeIntro] != want {
		t.Errorf("intro = %+v, want %+v", got[TypeIntro], want)
	}
	// The file's own credits chapter still applies.
	chapters := []Chapter{{Title: "Main", Start: 0}, {Title: "End Credits", Start: 1860}}
	if want := (Segment{TypeOutro, 1860, 1909, SourceChapterExplicit}); byType(Resolve(introDB, skipDB, chapters, 1909))[TypeOutro] != want {
		t.Errorf("chapter outro not used")
	}
}

func TestProviderOutroWithShortTagIsKept(t *testing.T) {
	// Credits ending two minutes before the end (a post-credits tag) still count.
	got := byType(Resolve(&IntroDBResponse{Outro: providerRange(1_200_000, 1_260_000)}, nil, nil, 1380))
	if want := (Segment{TypeOutro, 1200, 1260, SourceIntroDB}); got[TypeOutro] != want {
		t.Errorf("outro = %+v, want %+v", got[TypeOutro], want)
	}
	// Unknown duration: nothing to compare against, keep it.
	if _, ok := byType(Resolve(&IntroDBResponse{Outro: providerRange(1_200_000, 1_260_000)}, nil, nil, 0))[TypeOutro]; !ok {
		t.Errorf("outro dropped with unknown duration")
	}
}
