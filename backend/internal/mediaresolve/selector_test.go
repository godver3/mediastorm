package mediaresolve

import (
	"testing"
)

// =============================================================================
// Daily Show Date Matching Tests
// =============================================================================

func TestParseDailyDate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantYear  int
		wantMonth int
		wantDay   int
		wantOk    bool
	}{
		// Standard scene release formats
		{"Dot separated", "The.Daily.Show.2026.01.22.Alexandria.Stapleton.mkv", 2026, 1, 22, true},
		{"Hyphen separated", "The-Daily-Show-2026-01-22-Guest.mkv", 2026, 1, 22, true},
		{"Space separated", "The Daily Show 2026 01 22 Guest.mkv", 2026, 1, 22, true},
		{"Mixed separators", "The.Daily.Show.2026-01-22.Guest.mkv", 2026, 1, 22, true},

		// Different date positions
		{"Date at start", "2026.01.22.The.Daily.Show.mkv", 2026, 1, 22, true},
		{"Date in middle", "Show.Name.2026.01.22.Episode.Title.1080p.mkv", 2026, 1, 22, true},
		{"Date at end", "The.Daily.Show.1080p.2026.01.22.mkv", 2026, 1, 22, true},

		// Edge cases - valid dates
		{"January 1st", "Show.2026.01.01.mkv", 2026, 1, 1, true},
		{"December 31st", "Show.2026.12.31.mkv", 2026, 12, 31, true},
		{"Leap year Feb 29", "Show.2024.02.29.mkv", 2024, 2, 29, true},

		// Edge cases - should NOT match
		{"No date", "The.Daily.Show.S31E11.mkv", 0, 0, 0, false},
		{"Year only", "Show.2026.mkv", 0, 0, 0, false},
		{"Invalid month 13", "Show.2026.13.01.mkv", 0, 0, 0, false},
		{"Invalid month 0", "Show.2026.00.15.mkv", 0, 0, 0, false},
		{"Invalid day 32", "Show.2026.01.32.mkv", 0, 0, 0, false},
		{"Invalid day 0", "Show.2026.01.00.mkv", 0, 0, 0, false},
		{"Year too old", "Show.1899.01.15.mkv", 0, 0, 0, false},
		{"Year too future", "Show.2101.01.15.mkv", 0, 0, 0, false},
		{"Empty string", "", 0, 0, 0, false},
		{"Resolution looks like date", "Show.1080.720.480.mkv", 0, 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotYear, gotMonth, gotDay, gotOk := ParseDailyDate(tt.input)
			if gotOk != tt.wantOk {
				t.Errorf("ParseDailyDate(%q) ok = %v, want %v", tt.input, gotOk, tt.wantOk)
			}
			if gotYear != tt.wantYear {
				t.Errorf("ParseDailyDate(%q) year = %d, want %d", tt.input, gotYear, tt.wantYear)
			}
			if gotMonth != tt.wantMonth {
				t.Errorf("ParseDailyDate(%q) month = %d, want %d", tt.input, gotMonth, tt.wantMonth)
			}
			if gotDay != tt.wantDay {
				t.Errorf("ParseDailyDate(%q) day = %d, want %d", tt.input, gotDay, tt.wantDay)
			}
		})
	}
}

func TestDatesMatchWithTolerance(t *testing.T) {
	tests := []struct {
		name          string
		fileDate      string
		targetDate    string
		toleranceDays int
		want          bool
	}{
		// Exact match (tolerance 0)
		{"Exact match", "2026-01-22", "2026-01-22", 0, true},
		{"Different day exact", "2026-01-21", "2026-01-22", 0, false},

		// Tolerance of 1 day
		{"One day before", "2026-01-21", "2026-01-22", 1, true},
		{"One day after", "2026-01-23", "2026-01-22", 1, true},
		{"Exact with tolerance", "2026-01-22", "2026-01-22", 1, true},
		{"Two days off", "2026-01-20", "2026-01-22", 1, false},

		// Tolerance of 2 days
		{"Two days before", "2026-01-20", "2026-01-22", 2, true},
		{"Two days after", "2026-01-24", "2026-01-22", 2, true},
		{"Three days off", "2026-01-19", "2026-01-22", 2, false},

		// Month boundary
		{"End of month", "2026-01-31", "2026-02-01", 1, true},
		{"Start of month", "2026-02-01", "2026-01-31", 1, true},

		// Year boundary - NOTE: Current implementation uses simple day math,
		// so year boundaries don't work with tolerance. This is acceptable
		// since we use exact matching (tolerance 0) for daily shows.
		{"End of year same year", "2026-12-30", "2026-12-31", 1, true},
		{"Different years", "2025-12-31", "2026-01-01", 1, false}, // Known limitation

		// Invalid inputs
		{"Empty file date", "", "2026-01-22", 0, false},
		{"Empty target date", "2026-01-22", "", 0, false},
		{"Invalid file format", "2026/01/22", "2026-01-22", 0, false},
		{"Invalid target format", "2026-01-22", "2026/01/22", 0, false},
		{"Incomplete date", "2026-01", "2026-01-22", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DatesMatchWithTolerance(tt.fileDate, tt.targetDate, tt.toleranceDays)
			if got != tt.want {
				t.Errorf("DatesMatchWithTolerance(%q, %q, %d) = %v, want %v",
					tt.fileDate, tt.targetDate, tt.toleranceDays, got, tt.want)
			}
		})
	}
}

func TestCandidateMatchesDailyDate(t *testing.T) {
	tests := []struct {
		name          string
		candidate     string
		targetAirDate string
		toleranceDays int
		want          bool
	}{
		// Exact match cases
		{"Exact date match", "The.Daily.Show.2026.01.22.Guest.mkv", "2026-01-22", 0, true},
		{"Different format same date", "The-Daily-Show-2026-01-22.mkv", "2026-01-22", 0, true},

		// Wrong date (exact match required)
		{"Wrong date", "The.Daily.Show.2026.01.21.Guest.mkv", "2026-01-22", 0, false},
		{"Day before", "The.Daily.Show.2026.01.21.mkv", "2026-01-22", 0, false},
		{"Day after", "The.Daily.Show.2026.01.23.mkv", "2026-01-22", 0, false},

		// With tolerance
		{"Day before with tolerance", "The.Daily.Show.2026.01.21.mkv", "2026-01-22", 1, true},
		{"Day after with tolerance", "The.Daily.Show.2026.01.23.mkv", "2026-01-22", 1, true},

		// Real-world examples
		{"Real release name", "The.Daily.Show.2026.01.22.Alexandria.Stapleton.1080p.WEB.h264-EDITH.mkv", "2026-01-22", 0, true},
		{"Simu Liu episode", "The.Daily.Show.2026.01.21.Simu.Liu.1080p.WEB.h264-EDITH.mkv", "2026-01-22", 0, false},
		{"Simu Liu with tolerance", "The.Daily.Show.2026.01.21.Simu.Liu.1080p.WEB.h264-EDITH.mkv", "2026-01-22", 1, true},

		// Edge cases
		{"No date in filename", "The.Daily.Show.S31E11.mkv", "2026-01-22", 0, false},
		{"Empty target date", "The.Daily.Show.2026.01.22.mkv", "", 0, false},
		{"Empty candidate", "", "2026-01-22", 0, false},

		// Other daily shows
		{"Colbert", "The.Late.Show.with.Stephen.Colbert.2026.01.22.mkv", "2026-01-22", 0, true},
		{"Jimmy Fallon", "The.Tonight.Show.Starring.Jimmy.Fallon.2026.01.22.mkv", "2026-01-22", 0, true},
		{"John Oliver", "Last.Week.Tonight.with.John.Oliver.2026.01.19.mkv", "2026-01-19", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CandidateMatchesDailyDate(tt.candidate, tt.targetAirDate, tt.toleranceDays)
			if got != tt.want {
				t.Errorf("CandidateMatchesDailyDate(%q, %q, %d) = %v, want %v",
					tt.candidate, tt.targetAirDate, tt.toleranceDays, got, tt.want)
			}
		})
	}
}

func TestSelectBestCandidate_DailyShow(t *testing.T) {
	// Test daily show file selection with date-based matching
	candidates := []Candidate{
		{Label: "/The.Daily.Show.2026.01.21.Simu.Liu.1080p.WEB.h264-EDITH.mkv", Priority: 1},
		{Label: "/The.Daily.Show.2026.01.22.Alexandria.Stapleton.1080p.WEB.h264-EDITH.mkv", Priority: 1},
		{Label: "/The.Daily.Show.2026.01.23.Guest.1080p.WEB.h264-EDITH.mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:  "The Daily Show 2026 01 22 Alexandria Stapleton",
		TargetSeason:  31,
		TargetEpisode: 11,
		IsDaily:       true,
		TargetAirDate: "2026-01-22",
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	if idx != 1 {
		t.Errorf("SelectBestCandidate returned index %d, want 1 (Jan 22 episode)", idx)
	}
	if reason == "" {
		t.Error("SelectBestCandidate returned empty reason")
	}
	t.Logf("Selection reason: %s", reason)
}

func TestSelectBestCandidate_DailyShowRejectsWrongDate(t *testing.T) {
	// Should reject when date doesn't match (using exact match, no tolerance)
	candidates := []Candidate{
		{Label: "/The.Daily.Show.2026.01.21.Simu.Liu.1080p.WEB.h264-EDITH.mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:  "The Daily Show 2026 01 22 Alexandria Stapleton",
		TargetSeason:  31,
		TargetEpisode: 11,
		IsDaily:       true,
		TargetAirDate: "2026-01-22", // Looking for Jan 22, but file is Jan 21
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

func TestSelectBestCandidate_DailyShowWithMultipleFiles(t *testing.T) {
	// Pack with multiple episodes - should select correct date
	candidates := []Candidate{
		{Label: "/Sample/the.daily.show.2026.01.22.sample.mkv", Priority: 1},
		{Label: "/The.Daily.Show.2026.01.20.Guest1.mkv", Priority: 1},
		{Label: "/The.Daily.Show.2026.01.21.Guest2.mkv", Priority: 1},
		{Label: "/The.Daily.Show.2026.01.22.Guest3.mkv", Priority: 1},
		{Label: "/The.Daily.Show.2026.01.23.Guest4.mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:  "The Daily Show January 2026 Pack",
		TargetSeason:  31,
		TargetEpisode: 11,
		IsDaily:       true,
		TargetAirDate: "2026-01-22",
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	// Should select index 3 (Jan 22 main file, not sample)
	if idx != 3 {
		t.Errorf("SelectBestCandidate returned index %d, want 3 (Jan 22 episode, not sample)", idx)
	}
	t.Logf("Selection reason: %s", reason)
}

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

		// Standalone E## format (anime without season prefix)
		{"Standalone E01 with quote", "[OZC]RahXephon E01 'Invasion of the Capital' [720p].mkv", 1, true},
		{"Standalone E01 with space", "Anime E01 Title.mkv", 1, true},
		{"Standalone E42 in brackets", "[Group] Anime [E42] [1080p].mkv", 42, true},
		{"Standalone E123", "Show E123 - The Episode.mkv", 123, true},
		{"Lowercase e05", "anime e05.mkv", 5, true},
		// Standalone E## should NOT match S01E01 (already tested above as should not parse)
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

		// Standalone E## format (anime without season prefix)
		{"RahXephon E01 format", "[OZC]RahXephon E01 'Invasion of the Capital' [720p].mkv", 1, true},
		{"Standalone E05", "Anime E05 Title.mkv", 5, true},

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

		// Multi-season batch: Season 2 files should NOT match S01E02
		{"Season 02 dir - wrong season", "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 05.mkv", EpisodeCode{Season: 1, Episode: 2}, false},
		{"Season 02 dir - episode matches but wrong season", "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 02.mkv", EpisodeCode{Season: 1, Episode: 2}, false},
		{"Season 02 dir - ep 01", "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 01.mkv", EpisodeCode{Season: 1, Episode: 1}, false},
		// Season 1 files (no Season indicator) should still match
		{"Season 1 bare ep match", "/[Anime Time] Record Of Ragnarok/02[Anime Time] Record Of Ragnarok - 02.mkv", EpisodeCode{Season: 1, Episode: 2}, true},
		{"Season 1 dir with indicator", "/Show Season 01/Show - 02.mkv", EpisodeCode{Season: 1, Episode: 2}, true},
		// parseEpisodeNumber should skip "Season 02" as episode number
		{"Season number not episode", "/Show Season 02 - 05.mkv", EpisodeCode{Season: 1, Episode: 5}, false},
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

func TestSelectBestCandidate_MultiSeasonBatch(t *testing.T) {
	// Exact reproduction of the Record of Ragnarok bug:
	// Torrent has Season 1 (12 eps) + Season 2 (10 eps) in separate directories.
	// Target is S01E02 but the selector was picking S02E05 because "Season 02"
	// was being parsed as episode number 2.
	candidates := []Candidate{
		{Label: "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 05.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok/02[Anime Time] Record Of Ragnarok - 02.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok/03[Anime Time] Record Of Ragnarok - 03.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok/04[Anime Time] Record Of Ragnarok - 04.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok/05[Anime Time] Record Of Ragnarok - 05.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 01.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 02.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 03.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok Season 02/[Anime Time] Record Of Ragnarok Season 02 - 04.mkv", Priority: 1},
		{Label: "/[Anime Time] Record Of Ragnarok/01[Anime Time] Record Of Ragnarok - 01.mkv", Priority: 1},
	}

	hints := SelectionHints{
		ReleaseTitle:  "[Anime Time] Record Of Ragnarok (Season 01+ Season 02 (Part1)) [NF] [Dual Audio] [1080p][HEVC 10bit x265][AAC][Multi Sub] [Batch] (Shuumatsu no Walküre, Shuumatsu no Valkyrie)",
		TargetSeason:  1,
		TargetEpisode: 2,
	}

	idx, reason := SelectBestCandidate(candidates, hints)

	// Should select index 1: the S01E02 file, NOT the S02E05 file
	if idx != 1 {
		t.Errorf("SelectBestCandidate returned index %d (%q), want 1 (S01E02 file)", idx, candidates[idx].Label)
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
		{
			"Trailer file penalty",
			"Show.S01E01.Trailer.mkv",
			[]string{"show", "s01e01"},
			"shows01e01",
			false, // Should be penalized
		},
		{
			"Trailer in path penalty",
			"/Trailers/Show.S01E01.mkv",
			[]string{"show", "s01e01"},
			"shows01e01",
			false, // Should be penalized
		},
		{
			"Featurette penalty",
			"Show.Featurette.mkv",
			[]string{"show"},
			"show",
			false, // Should be penalized
		},
		{
			"Bonus penalty",
			"Bonus - Behind the Scenes.mkv",
			[]string{"show"},
			"show",
			false, // Should be penalized
		},
		{
			"Promo penalty",
			"Show.Promo.mkv",
			[]string{"show"},
			"show",
			false, // Should be penalized
		},
		{
			"Behind the scenes path penalty",
			"/Behind The Scenes/Making.Of.mkv",
			[]string{"making"},
			"making",
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
