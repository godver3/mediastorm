package filter

import (
	"testing"
)

func TestCompileTerms_PlainSubstring(t *testing.T) {
	terms := CompileTerms([]string{"dub", "cam"})
	if len(terms) != 2 {
		t.Fatalf("expected 2 compiled terms, got %d", len(terms))
	}
	if terms[0].regex != nil {
		t.Error("expected plain term, got regex")
	}
	if terms[0].plain != "dub" {
		t.Errorf("expected plain=%q, got %q", "dub", terms[0].plain)
	}
}

func TestCompileTerms_Regex(t *testing.T) {
	terms := CompileTerms([]string{`/\bDUB\b/`})
	if len(terms) != 1 {
		t.Fatalf("expected 1 compiled term, got %d", len(terms))
	}
	if terms[0].regex == nil {
		t.Fatal("expected regex term, got plain")
	}
}

func TestCompileTerms_InvalidRegexFallback(t *testing.T) {
	terms := CompileTerms([]string{"/invalid[/"})
	if len(terms) != 1 {
		t.Fatalf("expected 1 compiled term, got %d", len(terms))
	}
	if terms[0].regex != nil {
		t.Error("expected plain fallback for invalid regex, got regex")
	}
	// Falls back to the whole string including slashes
	if terms[0].plain != "/invalid[/" {
		t.Errorf("expected plain=%q, got %q", "/invalid[/", terms[0].plain)
	}
}

func TestCompileTerms_EmptyAndWhitespace(t *testing.T) {
	terms := CompileTerms([]string{"", "  ", "\t"})
	if len(terms) != 0 {
		t.Fatalf("expected 0 compiled terms for empty/whitespace, got %d", len(terms))
	}
}

func TestCompileTerms_SingleSlash(t *testing.T) {
	// A term like "/x" (no closing slash) or "/" should be plain
	terms := CompileTerms([]string{"/x", "/"})
	for _, term := range terms {
		if term.regex != nil {
			t.Error("short slash term should be plain, not regex")
		}
	}
}

func TestMatchesAnyTerm_PlainSubstring(t *testing.T) {
	terms := CompileTerms([]string{"dub"})

	if !MatchesAnyTerm("Movie.DUBBED.2024", terms) {
		t.Error("plain 'dub' should match 'DUBBED'")
	}
	if !MatchesAnyTerm("redub something", terms) {
		t.Error("plain 'dub' should match 'redub'")
	}
	if MatchesAnyTerm("Movie.2024.1080p", terms) {
		t.Error("plain 'dub' should not match title without 'dub'")
	}
}

func TestMatchesAnyTerm_RegexWordBoundary(t *testing.T) {
	terms := CompileTerms([]string{`/\bDUB\b/`})

	if !MatchesAnyTerm("Movie DUB 2024", terms) {
		t.Error("regex should match standalone DUB")
	}
	if !MatchesAnyTerm("Movie.DUB.2024", terms) {
		t.Error("regex should match DUB at word boundary (dot)")
	}
	if MatchesAnyTerm("Movie.DUBBED.2024", terms) {
		t.Error("regex \\bDUB\\b should NOT match DUBBED")
	}
}

func TestMatchesAnyTerm_RegexCharClass(t *testing.T) {
	terms := CompileTerms([]string{`/x26[45]/`})

	if !MatchesAnyTerm("Movie.x264.1080p", terms) {
		t.Error("regex should match x264")
	}
	if !MatchesAnyTerm("Movie.X265.2024", terms) {
		t.Error("regex should match X265 (case-insensitive)")
	}
	if MatchesAnyTerm("Movie.x266.2024", terms) {
		t.Error("regex should not match x266")
	}
}

func TestMatchesAnyTerm_CaseInsensitive(t *testing.T) {
	// Plain substring is case-insensitive
	plainTerms := CompileTerms([]string{"CAM"})
	if !MatchesAnyTerm("movie.cam.2024", plainTerms) {
		t.Error("plain match should be case-insensitive")
	}

	// Regex is also case-insensitive ((?i) flag)
	regexTerms := CompileTerms([]string{`/\bcam\b/`})
	if !MatchesAnyTerm("Movie.CAM.2024", regexTerms) {
		t.Error("regex match should be case-insensitive")
	}
}

func TestMatchesAnyTerm_EmptyTerms(t *testing.T) {
	if MatchesAnyTerm("any title", nil) {
		t.Error("nil terms should return false")
	}
	if MatchesAnyTerm("any title", []CompiledTerm{}) {
		t.Error("empty terms should return false")
	}
}

func TestMatchesAnyTerm_EmptyTitle(t *testing.T) {
	terms := CompileTerms([]string{"dub"})
	if MatchesAnyTerm("", terms) {
		t.Error("empty title should not match")
	}
}

func TestMatchesAnyTerm_MultipleTerms(t *testing.T) {
	terms := CompileTerms([]string{"cam", `/\bDUB\b/`, "telesync"})

	if !MatchesAnyTerm("Movie.CAM.2024", terms) {
		t.Error("should match first plain term")
	}
	if !MatchesAnyTerm("Movie DUB 2024", terms) {
		t.Error("should match regex term")
	}
	if !MatchesAnyTerm("Movie.TELESYNC.2024", terms) {
		t.Error("should match third plain term")
	}
	if MatchesAnyTerm("Movie.1080p.2024", terms) {
		t.Error("should not match unrelated title")
	}
}
