package mediaresolve

import (
	"testing"
)

func TestParseAbsoluteEpisodeNumber(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantEp  int
		wantOk  bool
	}{
		// SubsPlease format (most common anime release format)
		{"SubsPlease standard", "[SubsPlease] One Piece - 1153 (1080p) [HASH].mkv", 1153, true},
		{"SubsPlease 3-digit", "[SubsPlease] Anime - 042 (1080p).mkv", 42, true},
		{"SubsPlease 2-digit", "[SubsPlease] Anime - 01 (720p).mkv", 1, true},
		{"SubsPlease with version", "[SubsPlease] One Piece - 1153v2 (1080p).mkv", 1153, true},

		// Erai-raws format
		{"Erai-raws standard", "[Erai-raws] One Piece - 1153 [1080p].mkv", 1153, true},
		{"Erai-raws multiple audio", "[Erai-raws] Anime - 42 [1080p][Multiple Subtitle].mkv", 42, true},

		// Other common fansub formats
		{"Judas format", "[Judas] Anime - 100 [1080p][HEVC].mkv", 100, true},
		{"Generic group", "[Group] Anime Title - 0042 [720p].mkv", 42, true},
		{"Underscore separator", "Anime_-_1153_[1080p].mkv", 1153, true},

		// Episode keyword formats
		{"Episode keyword", "Anime Episode 1153 [1080p].mkv", 1153, true},
		{"Episode keyword lowercase", "anime episode 42.mkv", 42, true},
		{"Ep dot format", "Anime Ep.1153 [720p].mkv", 1153, true},
		{"Ep space format", "Anime Ep 42 [1080p].mkv", 42, true},
		{"Ep no space", "Anime Ep42 [720p].mkv", 42, true},

		// Hash format
		{"Hash format", "Anime #1153 [1080p].mkv", 1153, true},
		{"Hash with space", "Anime # 042 [720p].mkv", 42, true},

		// Edge cases - should NOT match
		{"Resolution only 1080p", "Anime [1080p].mkv", 0, false},
		{"Resolution only 720p", "Anime [720p].mkv", 0, false},
		{"Resolution only 480p", "Anime [480p].mkv", 0, false},
		{"Year in parentheses", "Anime (2024) [1080p].mkv", 0, false},
		{"Year in brackets", "Anime [2024] [1080p].mkv", 0, false},
		{"Checksum hash", "Anime [ABCD1234].mkv", 0, false},
		{"No episode number", "Anime Title [1080p].mkv", 0, false},
		{"Empty string", "", 0, false},

		// SXXEXX format with 2-digit episode should NOT be parsed as absolute
		{"SXXEXX format 2-digit", "Anime S01E42 [1080p].mkv", 0, false},
		{"SXXeXX format 2-digit", "Anime S22E68 [1080p].mkv", 0, false},

		// S01ENNNN format (anime using absolute episode in S01E format)
		// This is common for long-running anime where releases use S01E1153 instead of S22E68
		{"S01E with 4-digit episode", "One Piece S01E1153 The Episode Title [1080p].mkv", 1153, true},
		{"S01E with 3-digit episode", "Anime S01E123 [1080p].mkv", 123, true},
		{"s01e lowercase", "anime s01e1000 [720p].mkv", 1000, true},
		{"S01E at end of filename", "One.Piece.S01E1153.REPACK.1080p.mkv", 1153, true},

		// BD/DVD release formats
		{"BD release", "[Group] Anime - 1153 [BD][1080p].mkv", 1153, true},
		{"DVD release", "[Group] Anime - 42 [DVD][480p].mkv", 42, true},

		// Special cases
		{"Episode with hyphen separator", "One Piece - 1063 - Some Title [1080p].mkv", 1063, true},
		{"Episode at end", "Anime Title - 999 [FIN].mkv", 999, true},
		{"Four digit episode", "Long Running Anime - 1234 [1080p].mkv", 1234, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEp, gotOk := ParseAbsoluteEpisodeNumber(tt.input)
			if gotOk != tt.wantOk {
				t.Errorf("ParseAbsoluteEpisodeNumber(%q) ok = %v, want %v", tt.input, gotOk, tt.wantOk)
			}
			if gotEp != tt.wantEp {
				t.Errorf("ParseAbsoluteEpisodeNumber(%q) ep = %d, want %d", tt.input, gotEp, tt.wantEp)
			}
		})
	}
}

func TestCandidateMatchesAbsoluteEpisode(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		targetEp  int
		want      bool
	}{
		// Matching cases
		{"SubsPlease match", "[SubsPlease] One Piece - 1153 (1080p).mkv", 1153, true},
		{"Erai-raws match", "[Erai-raws] Anime - 42 [1080p].mkv", 42, true},
		{"Episode keyword match", "Anime Episode 100 [1080p].mkv", 100, true},

		// S01ENNNN format (anime using absolute episode in S01E format)
		{"S01E1153 format", "One Piece S01E1153 Title [1080p].mkv", 1153, true},
		{"S01E format dotted", "One.Piece.S01E1153.REPACK.1080p.mkv", 1153, true},
		{"s01e lowercase", "anime.s01e0999.mkv", 999, true},

		// Non-matching cases
		{"Wrong episode", "[SubsPlease] One Piece - 1152 (1080p).mkv", 1153, false},
		{"No episode number", "Anime [1080p].mkv", 1153, false},
		{"Zero target", "[SubsPlease] Anime - 42 (1080p).mkv", 0, false},
		{"Negative target", "[SubsPlease] Anime - 42 (1080p).mkv", -1, false},

		// Resolution should not match
		{"Resolution as episode", "Anime [1080p].mkv", 1080, false},
		{"Year as episode", "Anime (2024) [1080p].mkv", 2024, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CandidateMatchesAbsoluteEpisode(tt.candidate, tt.targetEp)
			if got != tt.want {
				t.Errorf("CandidateMatchesAbsoluteEpisode(%q, %d) = %v, want %v", tt.candidate, tt.targetEp, got, tt.want)
			}
		})
	}
}

func TestCandidateMatchesEpisode(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		target    EpisodeCode
		want      bool
	}{
		// Standard SXXEXX format
		{"Standard S01E01", "Show.S01E01.1080p.mkv", EpisodeCode{Season: 1, Episode: 1}, true},
		{"Standard S22E68", "Show.S22E68.720p.mkv", EpisodeCode{Season: 22, Episode: 68}, true},
		{"Lowercase sxxexx", "show.s01e05.mkv", EpisodeCode{Season: 1, Episode: 5}, true},
		{"With spaces", "Show S01 E05 1080p.mkv", EpisodeCode{Season: 1, Episode: 5}, true},

		// Alternative episode patterns (assuming season)
		{"Ep format in season pack", "Ep.05.mkv", EpisodeCode{Season: 1, Episode: 5}, true},
		{"Episode keyword", "Episode 10.mkv", EpisodeCode{Season: 1, Episode: 10}, true},

		// Non-matching cases
		{"Wrong season", "Show.S02E01.mkv", EpisodeCode{Season: 1, Episode: 1}, false},
		{"Wrong episode", "Show.S01E02.mkv", EpisodeCode{Season: 1, Episode: 1}, false},
		{"No episode info", "Show.1080p.mkv", EpisodeCode{Season: 1, Episode: 1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CandidateMatchesEpisode(tt.candidate, tt.target)
			if got != tt.want {
				t.Errorf("CandidateMatchesEpisode(%q, S%02dE%02d) = %v, want %v",
					tt.candidate, tt.target.Season, tt.target.Episode, got, tt.want)
			}
		})
	}
}

func TestSelectBestCandidate_AbsoluteEpisode(t *testing.T) {
	// Test that absolute episode matching works when SXXEXX fails
	candidates := []Candidate{
		{Label: "/[SubsPlease] One Piece - 1152 (1080p).mkv", Priority: 1},
		{Label: "/[SubsPlease] One Piece - 1153 (1080p).mkv", Priority: 1},
		{Label: "/[SubsPlease] One Piece - 1154 (1080p).mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:          "[SubsPlease] One Piece - 1153 (1080p)",
		TargetSeason:          22,
		TargetEpisode:         68,
		AbsoluteEpisodeNumber: 1153,
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	if idx != 1 {
		t.Errorf("SelectBestCandidate returned index %d, want 1 (episode 1153)", idx)
	}
	if reason == "" {
		t.Error("SelectBestCandidate returned empty reason")
	}
	t.Logf("Selection reason: %s", reason)
}

func TestSelectBestCandidate_SeasonPackWithAbsolute(t *testing.T) {
	// Season pack where files use absolute numbering
	candidates := []Candidate{
		{Label: "/One Piece - 1150 [BD][1080p].mkv", Priority: 1},
		{Label: "/One Piece - 1151 [BD][1080p].mkv", Priority: 1},
		{Label: "/One Piece - 1152 [BD][1080p].mkv", Priority: 1},
		{Label: "/One Piece - 1153 [BD][1080p].mkv", Priority: 1},
		{Label: "/One Piece - 1154 [BD][1080p].mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:          "One Piece Season 22 BD",
		TargetSeason:          22,
		TargetEpisode:         68,
		AbsoluteEpisodeNumber: 1153,
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	if idx != 3 {
		t.Errorf("SelectBestCandidate returned index %d, want 3 (episode 1153)", idx)
	}
	t.Logf("Selection reason: %s", reason)
}

func TestSelectBestCandidate_RejectsWrongEpisode(t *testing.T) {
	// Should reject when neither SXXEXX nor absolute episode matches
	candidates := []Candidate{
		{Label: "/One Piece - 1063 (1080p).mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:          "[SubsPlease] One Piece - 1063 (1080p)",
		TargetSeason:          22,
		TargetEpisode:         68,
		AbsoluteEpisodeNumber: 1153, // Looking for 1153, but file is 1063
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	if idx != -1 {
		t.Errorf("SelectBestCandidate should have rejected (idx=-1), got idx=%d", idx)
	}
	if reason == "" {
		t.Error("SelectBestCandidate should have returned a rejection reason")
	}
	t.Logf("Rejection reason: %s", reason)
}

func TestSelectBestCandidate_StandardSXXEXX(t *testing.T) {
	// Standard SXXEXX matching should still work
	candidates := []Candidate{
		{Label: "/Show.S01E01.1080p.mkv", Priority: 1},
		{Label: "/Show.S01E02.1080p.mkv", Priority: 1},
		{Label: "/Show.S01E03.1080p.mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:  "Show S01E02",
		TargetSeason:  1,
		TargetEpisode: 2,
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	if idx != 1 {
		t.Errorf("SelectBestCandidate returned index %d, want 1 (S01E02)", idx)
	}
	t.Logf("Selection reason: %s", reason)
}

func TestSelectBestCandidate_NoAbsoluteWhenSXXEXXMatches(t *testing.T) {
	// When SXXEXX matches, should use that even if absolute is also provided
	candidates := []Candidate{
		{Label: "/Show.S01E01.1080p.mkv", Priority: 1},
		{Label: "/Show.S01E02.1080p.mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:          "Show S01E02",
		TargetSeason:          1,
		TargetEpisode:         2,
		AbsoluteEpisodeNumber: 999, // This shouldn't affect selection when SXXEXX works
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	if idx != 1 {
		t.Errorf("SelectBestCandidate returned index %d, want 1", idx)
	}
	t.Logf("Selection reason: %s", reason)
}

func TestTokenizeParts(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  int // expected token count
	}{
		{"Simple name", []string{"One Piece"}, 2},
		{"With numbers", []string{"One Piece 1153"}, 3},
		{"With special chars", []string{"[SubsPlease] One Piece - 1153"}, 4},
		{"Empty string", []string{""}, 0},
		{"Multiple parts", []string{"Part1", "Part2"}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TokenizeParts(tt.parts...)
			if len(got) != tt.want {
				t.Errorf("TokenizeParts(%v) returned %d tokens, want %d", tt.parts, len(got), tt.want)
			}
		})
	}
}

func TestNormalizeReleasePart(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"Simple filename", "Show.S01E01.mkv", "Show.S01E01"},
		{"With path", "/path/to/Show.S01E01.mkv", "Show.S01E01"},
		{"Windows path", "C:\\path\\to\\Show.S01E01.mkv", "Show.S01E01"},
		{"No extension", "Show.S01E01", "Show.S01E01"},
		{"Empty string", "", ""},
		{"Whitespace", "  ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeReleasePart(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeReleasePart(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractEpisodeCode(t *testing.T) {
	tests := []struct {
		name       string
		parts      []string
		wantSeason int
		wantEp     int
		wantOk     bool
	}{
		{"Standard SXXEXX", []string{"Show.S01E05.mkv"}, 1, 5, true},
		{"Lowercase", []string{"show.s02e10.mkv"}, 2, 10, true},
		{"With spaces", []string{"Show S03 E15"}, 3, 15, true},
		{"No match", []string{"Show.1080p.mkv"}, 0, 0, false},
		{"Empty", []string{""}, 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractEpisodeCode(tt.parts...)
			if ok != tt.wantOk {
				t.Errorf("ExtractEpisodeCode(%v) ok = %v, want %v", tt.parts, ok, tt.wantOk)
			}
			if got.Season != tt.wantSeason || got.Episode != tt.wantEp {
				t.Errorf("ExtractEpisodeCode(%v) = S%02dE%02d, want S%02dE%02d",
					tt.parts, got.Season, got.Episode, tt.wantSeason, tt.wantEp)
			}
		})
	}
}

func TestComputeSimilarityScore(t *testing.T) {
	tests := []struct {
		name          string
		candidateName string
		releaseTokens []string
		releaseFlat   string
		wantPositive  bool
	}{
		{
			"Matching tokens",
			"One Piece - 1153.mkv",
			[]string{"one", "piece", "1153"},
			"onepiece1153",
			true,
		},
		{
			"Partial match",
			"One Piece - 1153.mkv",
			[]string{"one", "piece"},
			"onepiece",
			true,
		},
		{
			"No match",
			"Different Show.mkv",
			[]string{"one", "piece"},
			"onepiece",
			false,
		},
		{
			"Sample file penalty",
			"Sample - One Piece.mkv",
			[]string{"one", "piece"},
			"onepiece",
			false, // Should be penalized
		},
		{
			"Extras penalty",
			"Extras - One Piece.mkv",
			[]string{"one", "piece"},
			"onepiece",
			false, // Should be penalized
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeSimilarityScore(tt.candidateName, tt.releaseTokens, tt.releaseFlat)
			if tt.wantPositive && score <= 0 {
				t.Errorf("ComputeSimilarityScore expected positive score, got %d", score)
			}
			if !tt.wantPositive && score > 0 {
				t.Errorf("ComputeSimilarityScore expected zero or negative score, got %d", score)
			}
		})
	}
}
