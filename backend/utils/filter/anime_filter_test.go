package filter

import (
	"testing"

	"novastream/models"
)

// Anime Season Episode Data
// These are based on real TVDB data for accurate testing

// One Piece: https://thetvdb.com/series/one-piece
// As of 2024, One Piece has 22+ seasons with 1100+ episodes
// These counts are adjusted so Season 22 contains episode 1153
// (Season 22 starts at episode 1059 and episode 1153 is episode 95 of Season 22)
var onePieceSeasonCounts = map[int]int{
	1:  61,  // East Blue
	2:  16,  // Entering into the Grand Line
	3:  14,  // Introducing Chopper at the Winter Island
	4:  39,  // Alabasta
	5:  13,  // TV Original
	6:  52,  // Sky Island (Skypiea)
	7:  33,  // Long Ring Long Land
	8:  21,  // Water Seven
	9:  73,  // Enies Lobby
	10: 45,  // Thriller Bark
	11: 54,  // Sabaody Archipelago
	12: 14,  // Amazon Lily
	13: 26,  // Impel Down
	14: 58,  // Marineford
	15: 62,  // Fishman Island
	16: 118, // Punk Hazard & Dressrosa
	17: 55,  // Zou
	18: 195, // Whole Cake Island & Wano (combined in some sources)
	19: 21,  // Wano continued
	20: 36,  // Wano continued
	21: 52,  // Wano continued
	22: 100, // Egghead (ongoing) - adjusted to include ep 1153
}

// Sum of seasons 1-21 = 1058, so season 22 starts at episode 1059
// Episode 1153 would be episode 95 of season 22 (1153 - 1058 = 95)

// Dr. Stone: https://thetvdb.com/series/dr-stone
var drStoneSeasonCounts = map[int]int{
	1: 24, // Stone World
	2: 11, // Stone Wars
	3: 22, // New World
	4: 10, // Science Future (ongoing)
}

// Kaiju No. 8: https://thetvdb.com/series/kaiju-no-8
var kaijuNo8SeasonCounts = map[int]int{
	1: 12, // First season
	2: 12, // Second season (ongoing)
}

// Record of Ragnarok: https://thetvdb.com/series/record-of-ragnarok
var recordOfRagnarokSeasonCounts = map[int]int{
	1: 12, // First season
	2: 15, // Second season
}

// Demon Slayer: https://thetvdb.com/series/demon-slayer-kimetsu-no-yaiba
var demonSlayerSeasonCounts = map[int]int{
	1: 26, // Tanjiro Kamado, Unwavering Resolve Arc
	2: 18, // Mugen Train Arc + Entertainment District Arc
	3: 11, // Swordsmith Village Arc
	4: 11, // Hashira Training Arc
}

// Attack on Titan: https://thetvdb.com/series/attack-on-titan
var attackOnTitanSeasonCounts = map[int]int{
	1: 25, // Season 1
	2: 12, // Season 2
	3: 22, // Season 3
	4: 30, // Final Season (Parts 1-3)
}

// Naruto Shippuden: https://thetvdb.com/series/naruto-shippuden
var narutoShippudenSeasonCounts = map[int]int{
	1:  32,
	2:  21,
	3:  18,
	4:  17,
	5:  24,
	6:  31,
	7:  8,
	8:  24,
	9:  21,
	10: 25,
	11: 21,
	12: 33,
	13: 20,
	14: 25,
	15: 28,
	16: 17,
	17: 28,
	18: 21,
	19: 20,
	20: 26,
	21: 20,
}

// =============================================================================
// ONE PIECE TESTS
// =============================================================================

func TestAnimeFilter_OnePiece_SeasonPackRejection(t *testing.T) {
	// Test: Looking for S22E68 (absolute 1153), should reject season packs that can't contain it
	resolver := NewSeriesEpisodeResolver(onePieceSeasonCounts)

	tests := []struct {
		name        string
		title       string
		shouldPass  bool
		description string
	}{
		// Season packs that CANNOT contain S22E68
		{
			name:        "Season 1 pack rejected",
			title:       "One.Piece.Season.01.1080p.BluRay.x265-GROUP",
			shouldPass:  false,
			description: "Season 1 (eps 1-61) cannot contain S22E68",
		},
		{
			name:        "Season 2 pack rejected",
			title:       "One.Piece.S02.1080p.WEB-DL.x264",
			shouldPass:  false,
			description: "Season 2 cannot contain S22E68",
		},
		{
			name:        "Season 3 pack rejected",
			title:       "[Judas] One Piece - Season 03 [BD][1080p][HEVC]",
			shouldPass:  false,
			description: "Season 3 cannot contain S22E68",
		},
		{
			name:        "Season 5 pack rejected",
			title:       "One.Piece.Season.5.COMPLETE.720p.WEB-DL",
			shouldPass:  false,
			description: "Season 5 cannot contain S22E68",
		},
		{
			name:        "Season 10 pack rejected",
			title:       "One.Piece.S10.1080p.BluRay.x265",
			shouldPass:  false,
			description: "Season 10 cannot contain S22E68",
		},
		{
			name:        "Multi-season pack S01-S05 rejected",
			title:       "One.Piece.S01-S05.1080p.BluRay.x265-GROUP",
			shouldPass:  false,
			description: "Seasons 1-5 cannot contain S22E68",
		},
		{
			name:        "Multi-season pack S06-S10 rejected",
			title:       "One.Piece.S06-S10.COMPLETE.1080p.WEB-DL",
			shouldPass:  false,
			description: "Seasons 6-10 cannot contain S22E68",
		},

		// Season packs that CAN contain S22E68
		{
			name:        "Season 22 pack accepted",
			title:       "One.Piece.S22.1080p.WEB-DL.x264-GROUP",
			shouldPass:  true,
			description: "Season 22 contains S22E68",
		},
		{
			name:        "Season 22 alternate format accepted",
			title:       "[SubsPlease] One Piece - Season 22 [1080p]",
			shouldPass:  true,
			description: "Season 22 contains S22E68",
		},

		// Complete packs - should be accepted (might contain the episode)
		{
			name:        "Complete series pack accepted",
			title:       "One.Piece.COMPLETE.1080p.WEB-DL.x265",
			shouldPass:  true,
			description: "Complete pack might contain the target episode",
		},

		// S01ENNNN format (anime absolute numbering)
		{
			name:        "S01E1153 format accepted",
			title:       "One.Piece.S01E1153.1080p.WEB-DL.x264",
			shouldPass:  true,
			description: "S01E1153 is anime absolute format, should match S22E68 (abs 1153)",
		},
		{
			name:        "s01e1153 lowercase accepted",
			title:       "one.piece.s01e1153.1080p.web-dl.x264",
			shouldPass:  true,
			description: "Lowercase S01E1153 format",
		},

		// Fansub absolute episode format
		{
			name:        "SubsPlease absolute format accepted",
			title:       "[SubsPlease] One Piece - 1153 (1080p) [HASH].mkv",
			shouldPass:  true,
			description: "Fansub absolute episode format",
		},
		{
			name:        "Erai-raws absolute format accepted",
			title:       "[Erai-raws] One Piece - 1153 [1080p].mkv",
			shouldPass:  true,
			description: "Erai-raws absolute episode format",
		},

		// Wrong absolute episodes should be rejected
		{
			name:        "Wrong absolute episode rejected",
			title:       "[SubsPlease] One Piece - 1063 (1080p).mkv",
			shouldPass:  false,
			description: "Episode 1063 is not 1153",
		},
		{
			name:        "Wrong S01E format rejected",
			title:       "One.Piece.S01E1063.1080p.WEB-DL.x264",
			shouldPass:  false,
			description: "S01E1063 is not S01E1153",
		},
	}

	opts := Options{
		ExpectedTitle:         "One Piece",
		IsMovie:               false,
		TargetSeason:          22,
		TargetEpisode:         68,
		TargetAbsoluteEpisode: 1153,
		EpisodeResolver:       resolver,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected. %s", tt.title, tt.description)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed. %s", tt.title, tt.description)
				}
			}
		})
	}
}

func TestAnimeFilter_OnePiece_AbsoluteEpisodeRange(t *testing.T) {
	// Test the absolute episode range calculation
	resolver := NewSeriesEpisodeResolver(onePieceSeasonCounts)

	tests := []struct {
		name           string
		seasons        []int
		expectedMinAbs int
		expectedMaxAbs int
	}{
		{
			name:           "Season 1",
			seasons:        []int{1},
			expectedMinAbs: 1,
			expectedMaxAbs: 61,
		},
		{
			name:           "Season 2",
			seasons:        []int{2},
			expectedMinAbs: 62, // 61 + 1
			expectedMaxAbs: 77, // 61 + 16
		},
		{
			name:           "Seasons 1-3",
			seasons:        []int{1, 2, 3},
			expectedMinAbs: 1,
			expectedMaxAbs: 91, // 61 + 16 + 14
		},
		{
			name:           "Season 22",
			seasons:        []int{22},
			expectedMinAbs: 1059, // Sum of seasons 1-21 + 1 = 1058 + 1
			expectedMaxAbs: 1158, // Sum of seasons 1-22 = 1058 + 100
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minAbs, maxAbs := getAbsoluteEpisodeRange(tt.seasons, resolver)

			if minAbs != tt.expectedMinAbs {
				t.Errorf("getAbsoluteEpisodeRange(%v) minAbs = %d, want %d", tt.seasons, minAbs, tt.expectedMinAbs)
			}
			if maxAbs != tt.expectedMaxAbs {
				t.Errorf("getAbsoluteEpisodeRange(%v) maxAbs = %d, want %d", tt.seasons, maxAbs, tt.expectedMaxAbs)
			}
		})
	}
}

// =============================================================================
// DR. STONE TESTS
// =============================================================================

func TestAnimeFilter_DrStone_SeasonMatching(t *testing.T) {
	// Test: Looking for S03E10, should only accept Season 3 content
	resolver := NewSeriesEpisodeResolver(drStoneSeasonCounts)

	tests := []struct {
		name       string
		title      string
		shouldPass bool
	}{
		// Season packs
		{
			name:       "Season 3 pack accepted",
			title:      "Dr.Stone.S03.1080p.WEB-DL.x264-GROUP",
			shouldPass: true,
		},
		{
			name:       "Season 3 alternate name accepted",
			title:      "Dr.STONE.New.World.S03.1080p.CR.WEB-DL",
			shouldPass: true,
		},
		{
			name:       "Season 1 pack rejected",
			title:      "Dr.Stone.S01.1080p.BluRay.x265",
			shouldPass: false,
		},
		{
			name:       "Season 2 pack rejected",
			title:      "Dr.Stone.Stone.Wars.S02.1080p.WEB-DL",
			shouldPass: false,
		},

		// Single episodes
		{
			name:       "S03E10 accepted",
			title:      "Dr.Stone.S03E10.1080p.WEB-DL.x264",
			shouldPass: true,
		},
		{
			name:       "S01E10 rejected",
			title:      "Dr.Stone.S01E10.1080p.WEB-DL.x264",
			shouldPass: false,
		},
		{
			name:       "S02E10 rejected",
			title:      "Dr.Stone.S02E10.1080p.WEB-DL.x264",
			shouldPass: false,
		},

		// Absolute episode format (S03E10 = absolute episode 45)
		{
			name:       "Absolute ep 45 fansub accepted",
			title:      "[SubsPlease] Dr. Stone - 45 (1080p).mkv",
			shouldPass: true,
		},

		// Complete pack
		{
			name:       "Complete pack accepted",
			title:      "Dr.Stone.COMPLETE.1080p.BluRay.x265",
			shouldPass: true,
		},
	}

	opts := Options{
		ExpectedTitle:         "Dr Stone",
		IsMovie:               false,
		TargetSeason:          3,
		TargetEpisode:         10,
		TargetAbsoluteEpisode: 45, // 24 + 11 + 10
		EpisodeResolver:       resolver,
		AlternateTitles:       []string{"Dr. Stone", "Dr.Stone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

// =============================================================================
// KAIJU NO. 8 TESTS
// =============================================================================

func TestAnimeFilter_KaijuNo8_Season2(t *testing.T) {
	// Test: Looking for S02E04
	resolver := NewSeriesEpisodeResolver(kaijuNo8SeasonCounts)

	tests := []struct {
		name       string
		title      string
		shouldPass bool
	}{
		// Season 2 content
		{
			name:       "S02 pack accepted",
			title:      "Kaiju.No.8.S02.1080p.CR.WEB-DL.x265",
			shouldPass: true,
		},
		{
			name:       "S02E04 accepted",
			title:      "Kaiju.No.8.S02E04.1080p.WEB-DL.x264",
			shouldPass: true,
		},
		{
			name:       "Fansub S02E04 accepted",
			title:      "[SubsPlease] Kaiju No. 8 - S02E04 (1080p).mkv",
			shouldPass: true,
		},

		// Season 1 content - should be rejected
		{
			name:       "S01 pack rejected",
			title:      "Kaiju.No.8.S01.1080p.WEB-DL.x264-GROUP",
			shouldPass: false,
		},
		{
			name:       "S01E04 rejected",
			title:      "Kaiju.No.8.S01E04.1080p.WEB-DL.x264",
			shouldPass: false,
		},

		// Absolute episode format (S02E04 = absolute 16)
		{
			name:       "Absolute ep 16 fansub accepted",
			title:      "[SubsPlease] Kaiju No. 8 - 16 (1080p).mkv",
			shouldPass: true,
		},
		{
			name:       "Wrong absolute ep 04 rejected",
			title:      "[SubsPlease] Kaiju No. 8 - 04 (1080p).mkv",
			shouldPass: false,
		},

		// Complete pack
		{
			name:       "Complete pack accepted",
			title:      "Kaiju.No.8.COMPLETE.1080p.WEB-DL",
			shouldPass: true,
		},
	}

	opts := Options{
		ExpectedTitle:         "Kaiju No 8",
		IsMovie:               false,
		TargetSeason:          2,
		TargetEpisode:         4,
		TargetAbsoluteEpisode: 16, // 12 + 4
		EpisodeResolver:       resolver,
		AlternateTitles:       []string{"Kaiju No. 8", "Kaiju No.8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

// =============================================================================
// RECORD OF RAGNAROK TESTS
// =============================================================================

func TestAnimeFilter_RecordOfRagnarok_Season2(t *testing.T) {
	// Test: Looking for S02E01 (absolute 13)
	resolver := NewSeriesEpisodeResolver(recordOfRagnarokSeasonCounts)

	tests := []struct {
		name       string
		title      string
		shouldPass bool
	}{
		// Season 2 content
		{
			name:       "S02 pack accepted",
			title:      "Record.of.Ragnarok.S02.1080p.NF.WEB-DL.x265",
			shouldPass: true,
		},
		{
			name:       "S02E01 accepted",
			title:      "Record.of.Ragnarok.S02E01.1080p.WEB-DL.x264",
			shouldPass: true,
		},
		{
			name:       "Fansub S02E01 accepted",
			title:      "[SubsPlease] Shuumatsu no Valkyrie - S02E01 (1080p).mkv",
			shouldPass: true,
		},

		// Season 1 content - should be rejected
		{
			name:       "S01 pack rejected",
			title:      "Record.of.Ragnarok.S01.1080p.NF.WEB-DL.x265",
			shouldPass: false,
		},
		{
			name:       "S01E01 rejected",
			title:      "Record.of.Ragnarok.S01E01.1080p.WEB-DL.x264",
			shouldPass: false,
		},

		// Absolute episode format (S02E01 = absolute 13)
		{
			name:       "Absolute ep 13 fansub accepted",
			title:      "[SubsPlease] Shuumatsu no Valkyrie - 13 (1080p).mkv",
			shouldPass: true,
		},
		{
			name:       "Wrong absolute ep 01 rejected",
			title:      "[SubsPlease] Shuumatsu no Valkyrie - 01 (1080p).mkv",
			shouldPass: false,
		},

		// Complete pack
		{
			name:       "Complete pack accepted",
			title:      "Record.of.Ragnarok.COMPLETE.1080p.WEB-DL",
			shouldPass: true,
		},
	}

	opts := Options{
		ExpectedTitle:         "Record of Ragnarok",
		IsMovie:               false,
		TargetSeason:          2,
		TargetEpisode:         1,
		TargetAbsoluteEpisode: 13, // 12 + 1
		EpisodeResolver:       resolver,
		AlternateTitles:       []string{"Shuumatsu no Valkyrie", "Shuumatsu no Walkure"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

// =============================================================================
// DEMON SLAYER TESTS
// =============================================================================

func TestAnimeFilter_DemonSlayer_Season3(t *testing.T) {
	// Test: Looking for S03E05 (Swordsmith Village Arc)
	resolver := NewSeriesEpisodeResolver(demonSlayerSeasonCounts)

	tests := []struct {
		name       string
		title      string
		shouldPass bool
	}{
		// Season 3 content
		{
			name:       "S03 pack accepted",
			title:      "Demon.Slayer.Kimetsu.no.Yaiba.S03.1080p.CR.WEB-DL",
			shouldPass: true,
		},
		{
			name:       "S03E05 accepted",
			title:      "Demon.Slayer.S03E05.Swordsmith.Village.1080p.WEB-DL",
			shouldPass: true,
		},
		{
			name:       "Swordsmith Village arc name accepted",
			title:      "Demon.Slayer.Swordsmith.Village.Arc.S03.1080p.CR.WEB-DL",
			shouldPass: true,
		},

		// Other seasons - should be rejected
		{
			name:       "S01 pack rejected",
			title:      "Demon.Slayer.S01.1080p.BluRay.x265",
			shouldPass: false,
		},
		{
			name:       "S02 Entertainment District rejected",
			title:      "Demon.Slayer.Entertainment.District.Arc.S02.1080p.WEB-DL",
			shouldPass: false,
		},
		{
			name:       "S04 pack rejected",
			title:      "Demon.Slayer.S04.Hashira.Training.1080p.WEB-DL",
			shouldPass: false,
		},

		// Absolute episode (S03E05 = 26 + 18 + 5 = 49)
		{
			name:       "Absolute ep 49 fansub accepted",
			title:      "[SubsPlease] Kimetsu no Yaiba - 49 (1080p).mkv",
			shouldPass: true,
		},

		// Complete pack
		{
			name:       "Complete pack accepted",
			title:      "Demon.Slayer.COMPLETE.1080p.BluRay.x265",
			shouldPass: true,
		},
	}

	opts := Options{
		ExpectedTitle:         "Demon Slayer",
		IsMovie:               false,
		TargetSeason:          3,
		TargetEpisode:         5,
		TargetAbsoluteEpisode: 49, // 26 + 18 + 5
		EpisodeResolver:       resolver,
		AlternateTitles:       []string{"Kimetsu no Yaiba", "Demon Slayer Kimetsu no Yaiba"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

// =============================================================================
// ATTACK ON TITAN TESTS
// =============================================================================

func TestAnimeFilter_AttackOnTitan_FinalSeason(t *testing.T) {
	// Test: Looking for S04E20 (Final Season Part 2)
	resolver := NewSeriesEpisodeResolver(attackOnTitanSeasonCounts)

	tests := []struct {
		name       string
		title      string
		shouldPass bool
	}{
		// Season 4 content
		{
			name:       "S04 pack accepted",
			title:      "Attack.on.Titan.S04.The.Final.Season.1080p.WEB-DL",
			shouldPass: true,
		},
		{
			name:       "S04E20 accepted",
			title:      "Attack.on.Titan.S04E20.1080p.WEB-DL.x264",
			shouldPass: true,
		},
		{
			name:       "Final Season Part 2 accepted",
			title:      "Shingeki.no.Kyojin.The.Final.Season.Part.2.S04.1080p.CR.WEB-DL",
			shouldPass: true,
		},

		// Other seasons - should be rejected
		{
			name:       "S01 pack rejected",
			title:      "Attack.on.Titan.S01.1080p.BluRay.x265",
			shouldPass: false,
		},
		{
			name:       "S02 pack rejected",
			title:      "Attack.on.Titan.S02.1080p.WEB-DL",
			shouldPass: false,
		},
		{
			name:       "S03 pack rejected",
			title:      "Attack.on.Titan.S03.1080p.BluRay.x265",
			shouldPass: false,
		},

		// Absolute episode (S04E20 = 25 + 12 + 22 + 20 = 79)
		{
			name:       "Absolute ep 79 fansub accepted",
			title:      "[SubsPlease] Shingeki no Kyojin - 79 (1080p).mkv",
			shouldPass: true,
		},

		// Complete pack
		{
			name:       "Complete pack accepted",
			title:      "Attack.on.Titan.COMPLETE.1080p.BluRay.x265",
			shouldPass: true,
		},
	}

	opts := Options{
		ExpectedTitle:         "Attack on Titan",
		IsMovie:               false,
		TargetSeason:          4,
		TargetEpisode:         20,
		TargetAbsoluteEpisode: 79, // 25 + 12 + 22 + 20
		EpisodeResolver:       resolver,
		AlternateTitles:       []string{"Shingeki no Kyojin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

// =============================================================================
// EDGE CASES AND REGRESSION TESTS
// =============================================================================

func TestAnimeFilter_S01ENNNN_Format(t *testing.T) {
	// Test that S01E#### format with high episode numbers is recognized as anime absolute
	// and not rejected when target season is different

	resolver := NewSeriesEpisodeResolver(onePieceSeasonCounts)

	tests := []struct {
		name          string
		title         string
		expectedTitle string
		targetSeason  int
		targetEpisode int
		absoluteEp    int
		shouldPass    bool
	}{
		{
			name:          "S01E1153 matches S22E68 target",
			title:         "One.Piece.S01E1153.1080p.WEB-DL.x264",
			expectedTitle: "One Piece",
			targetSeason:  22,
			targetEpisode: 68,
			absoluteEp:    1153,
			shouldPass:    true,
		},
		{
			name:          "S01E999 with high absolute target",
			title:         "One.Piece.S01E999.1080p.WEB-DL",
			expectedTitle: "One Piece",
			targetSeason:  10,
			targetEpisode: 5,
			absoluteEp:    999,
			shouldPass:    true,
		},
		{
			name:          "S01E50 with low absolute - not anime format (ep < 100)",
			title:         "One.Piece.S01E050.1080p.WEB-DL",
			expectedTitle: "One Piece",
			targetSeason:  1,
			targetEpisode: 50,
			absoluteEp:    50,
			shouldPass:    true, // S01E50 with target S01 - season matches
		},
		{
			name:          "Regular S02E05 wrong season rejected",
			title:         "One.Piece.S02E05.1080p.WEB-DL",
			expectedTitle: "One Piece",
			targetSeason:  1,
			targetEpisode: 5,
			absoluteEp:    5,
			shouldPass:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{
				ExpectedTitle:         tt.expectedTitle,
				IsMovie:               false,
				TargetSeason:          tt.targetSeason,
				TargetEpisode:         tt.targetEpisode,
				TargetAbsoluteEpisode: tt.absoluteEp,
				EpisodeResolver:       resolver,
			}

			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

func TestAnimeFilter_NoTargetEpisode_NoRejection(t *testing.T) {
	// When no target episode is specified, the target episode filter should not reject anything
	resolver := NewSeriesEpisodeResolver(onePieceSeasonCounts)

	results := []models.NZBResult{
		{Title: "One.Piece.S01.1080p.WEB-DL"},
		{Title: "One.Piece.S10.1080p.WEB-DL"},
		{Title: "One.Piece.S22.1080p.WEB-DL"},
		{Title: "One.Piece.COMPLETE.1080p.WEB-DL"},
	}

	opts := Options{
		ExpectedTitle:         "One Piece",
		IsMovie:               false,
		TargetSeason:          0, // No target season
		TargetEpisode:         0, // No target episode
		TargetAbsoluteEpisode: 0, // No target absolute
		EpisodeResolver:       resolver,
	}

	filtered := Results(results, opts)

	// All should pass since no target episode filtering
	if len(filtered) != len(results) {
		t.Errorf("Expected all %d results to pass when no target episode, got %d", len(results), len(filtered))
	}
}

func TestAnimeFilter_WithoutResolver_LimitedFiltering(t *testing.T) {
	// Without an episode resolver, absolute episode range filtering should be skipped
	// but season matching should still work

	results := []models.NZBResult{
		{Title: "One.Piece.S22.1080p.WEB-DL"},          // Matches target season
		{Title: "One.Piece.S01.1080p.WEB-DL"},          // Wrong season
		{Title: "One.Piece.S01E1153.1080p.WEB-DL"},     // S01E#### format (anime absolute)
		{Title: "[SubsPlease] One Piece - 1153 (1080p).mkv"}, // Fansub absolute
	}

	opts := Options{
		ExpectedTitle:         "One Piece",
		IsMovie:               false,
		TargetSeason:          22,
		TargetEpisode:         68,
		TargetAbsoluteEpisode: 1153,
		EpisodeResolver:       nil, // No resolver
	}

	filtered := Results(results, opts)

	// Should filter by season match where possible
	// S22 should pass (season match)
	// S01 pack should be rejected (wrong season)
	// S01E1153 should pass (anime absolute format)
	// Fansub absolute should pass (has episode info)
	expectedCount := 3
	if len(filtered) != expectedCount {
		t.Errorf("Expected %d results without resolver, got %d", expectedCount, len(filtered))
		for i, r := range filtered {
			t.Logf("  Result[%d]: %s", i, r.Title)
		}
	}
}

func TestAnimeFilter_MultiSeasonPack(t *testing.T) {
	// Test multi-season packs like S01-S10, S15-S22
	resolver := NewSeriesEpisodeResolver(onePieceSeasonCounts)

	tests := []struct {
		name       string
		title      string
		shouldPass bool
	}{
		// Packs that include Season 22
		{
			name:       "S20-S22 includes target",
			title:      "One.Piece.S20-S22.1080p.WEB-DL.x265",
			shouldPass: true,
		},
		{
			name:       "S15-S22 includes target",
			title:      "One.Piece.S15-S22.1080p.BluRay.x265",
			shouldPass: true,
		},

		// Packs that don't include Season 22
		{
			name:       "S01-S10 doesn't include target",
			title:      "One.Piece.S01-S10.1080p.BluRay.x265",
			shouldPass: false,
		},
		{
			name:       "S11-S15 doesn't include target",
			title:      "One.Piece.S11-S15.1080p.WEB-DL.x265",
			shouldPass: false,
		},
		{
			name:       "S16-S20 doesn't include target",
			title:      "One.Piece.S16-S20.1080p.WEB-DL.x265",
			shouldPass: false,
		},
	}

	opts := Options{
		ExpectedTitle:         "One Piece",
		IsMovie:               false,
		TargetSeason:          22,
		TargetEpisode:         68,
		TargetAbsoluteEpisode: 1153,
		EpisodeResolver:       resolver,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

func TestAnimeFilter_BatchReleaseWithEpisodeRange(t *testing.T) {
	// Test batch releases like "01-26" or "001-050"
	resolver := NewSeriesEpisodeResolver(drStoneSeasonCounts)

	tests := []struct {
		name       string
		title      string
		shouldPass bool
	}{
		// Dr. Stone S03E10 = absolute 45
		{
			name:       "Batch 35-57 includes ep 45",
			title:      "[Group] Dr Stone New World - 35-57 [1080p]",
			shouldPass: true,
		},
		{
			name:       "Batch 01-24 doesn't include ep 45",
			title:      "[Group] Dr Stone - 01-24 [1080p]",
			shouldPass: false,
		},
		{
			name:       "Batch 25-35 doesn't include ep 45",
			title:      "[Group] Dr Stone Stone Wars - 25-35 [1080p]",
			shouldPass: false,
		},
	}

	opts := Options{
		ExpectedTitle:         "Dr Stone",
		IsMovie:               false,
		TargetSeason:          3,
		TargetEpisode:         10,
		TargetAbsoluteEpisode: 45,
		EpisodeResolver:       resolver,
		AlternateTitles:       []string{"Dr. Stone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []models.NZBResult{{Title: tt.title}}
			filtered := Results(results, opts)

			passed := len(filtered) > 0
			if passed != tt.shouldPass {
				if tt.shouldPass {
					t.Errorf("Expected %q to PASS but was rejected", tt.title)
				} else {
					t.Errorf("Expected %q to be REJECTED but passed", tt.title)
				}
			}
		})
	}
}

// =============================================================================
// HELPER FUNCTION TESTS
// =============================================================================

func TestShouldRejectByTargetEpisode_NilParsed(t *testing.T) {
	opts := Options{
		TargetSeason:  1,
		TargetEpisode: 5,
	}

	rejected, reason := shouldRejectByTargetEpisode(nil, opts)
	if rejected {
		t.Errorf("shouldRejectByTargetEpisode(nil) should not reject, got rejected with reason: %s", reason)
	}
}

func TestGetAbsoluteEpisodeRange_NilResolver(t *testing.T) {
	min, max := getAbsoluteEpisodeRange([]int{1, 2, 3}, nil)
	if min != 0 || max != 0 {
		t.Errorf("getAbsoluteEpisodeRange with nil resolver should return (0, 0), got (%d, %d)", min, max)
	}
}

func TestGetAbsoluteEpisodeRange_EmptySeasons(t *testing.T) {
	resolver := NewSeriesEpisodeResolver(onePieceSeasonCounts)
	min, max := getAbsoluteEpisodeRange([]int{}, resolver)
	if min != 0 || max != 0 {
		t.Errorf("getAbsoluteEpisodeRange with empty seasons should return (0, 0), got (%d, %d)", min, max)
	}
}
