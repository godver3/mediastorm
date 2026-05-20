package handlers

import (
	"strings"
	"testing"
)

func TestCleanYouTubeVTTStripsInlineTimingAndTags(t *testing.T) {
	input := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nignored old line\n<00:00:01.200><c>hello</c> <00:00:02.000><c.colorE5E5E5>world &amp; friends</c>\n\n")

	cleaned := string(cleanYouTubeVTT(input))

	if strings.Contains(cleaned, "<00:00") || strings.Contains(cleaned, "<c") || strings.Contains(cleaned, "</c>") {
		t.Fatalf("expected inline YouTube tags to be stripped, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "hello world & friends") {
		t.Fatalf("expected cleaned cue text, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "00:00:01.000 --> 00:00:04.000") {
		t.Fatalf("expected cue timing line to be preserved, got:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "ignored old line") {
		t.Fatalf("expected multi-line cue to collapse to the latest line, got:\n%s", cleaned)
	}
}

func TestCleanYouTubeVTTPreventsOverlappingCues(t *testing.T) {
	input := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nfirst line\n\n00:00:02.500 --> 00:00:05.000\nsecond line\n\n")

	cleaned := string(cleanYouTubeVTT(input))

	if !strings.Contains(cleaned, "00:00:01.000 --> 00:00:02.500") {
		t.Fatalf("expected first cue to end at next cue start, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "00:00:02.500 --> 00:00:05.000") {
		t.Fatalf("expected second cue timing to remain, got:\n%s", cleaned)
	}
}

func TestCleanYouTubeVTTDropsHeaderMetadata(t *testing.T) {
	input := []byte("WEBVTT\nKind: captions\nLanguage: en\n\n00:00:01.000 --> 00:00:02.000\nhello\n\n")

	cleaned := string(cleanYouTubeVTT(input))

	if strings.Contains(cleaned, "Kind:") || strings.Contains(cleaned, "Language:") {
		t.Fatalf("expected YouTube header metadata to be dropped, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "hello") {
		t.Fatalf("expected cue text to remain, got:\n%s", cleaned)
	}
}

func TestYouTubeCaptionFormatIsTranslated(t *testing.T) {
	if !youtubeCaptionFormatIsTranslated("https://www.youtube.com/api/timedtext?v=test&lang=en&tlang=ar&fmt=vtt") {
		t.Fatal("expected translated caption URL to be detected")
	}
	if youtubeCaptionFormatIsTranslated("https://www.youtube.com/api/timedtext?v=test&lang=en&fmt=vtt") {
		t.Fatal("expected source caption URL to remain selectable")
	}
	if youtubeCaptionFormatIsTranslated("://not a url") {
		t.Fatal("expected malformed URL to be treated as non-translated")
	}
}
