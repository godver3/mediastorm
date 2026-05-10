package indexer

import (
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/utils/filter"
)

func TestScoreResult_ServicePriority(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingServicePriority, Name: "Service Priority", Enabled: true, Order: 0},
		},
		ServicePriority: config.StreamingServicePriorityUsenet,
	}

	usenet := models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeUsenet}
	debrid := models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeDebrid}

	scoreU, _ := ScoreResult(usenet, ctx)
	scoreD, _ := ScoreResult(debrid, ctx)

	if scoreU <= scoreD {
		t.Fatalf("expected usenet score (%d) > debrid score (%d) when usenet is preferred", scoreU, scoreD)
	}
}

func TestScoreResult_PreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"remux"}),
	}

	with := models.NZBResult{Title: "Movie 2024 Remux 1080p"}
	without := models.NZBResult{Title: "Movie 2024 BluRay 1080p"}

	scoreWith, _ := ScoreResult(with, ctx)
	scoreWithout, _ := ScoreResult(without, ctx)

	if scoreWith <= scoreWithout {
		t.Fatalf("expected preferred term match (%d) > no match (%d)", scoreWith, scoreWithout)
	}
}

func TestScoreResult_NonPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingNonPreferredTerms, Name: "Non-Preferred Terms", Enabled: true, Order: 0},
		},
		NonPreferredTerms: filter.CompileTerms([]string{"cam"}),
	}

	cam := models.NZBResult{Title: "Movie 2024 CAM"}
	bluray := models.NZBResult{Title: "Movie 2024 BluRay"}

	scoreCam, _ := ScoreResult(cam, ctx)
	scoreBluray, _ := ScoreResult(bluray, ctx)

	if scoreCam >= scoreBluray {
		t.Fatalf("expected non-preferred term (%d) < no match (%d)", scoreCam, scoreBluray)
	}
	if scoreCam >= 0 {
		t.Fatalf("expected negative score for non-preferred match, got %d", scoreCam)
	}
}

func TestScoreResult_Resolution(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		},
	}

	r4k := models.NZBResult{Title: "Movie 2160p"}
	r1080 := models.NZBResult{Title: "Movie 1080p"}
	r720 := models.NZBResult{Title: "Movie 720p"}

	s4k, _ := ScoreResult(r4k, ctx)
	s1080, _ := ScoreResult(r1080, ctx)
	s720, _ := ScoreResult(r720, ctx)

	if s4k <= s1080 || s1080 <= s720 {
		t.Fatalf("expected 4k(%d) > 1080p(%d) > 720p(%d)", s4k, s1080, s720)
	}
}

func TestScoreResult_Size(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 0},
		},
	}

	big := models.NZBResult{Title: "Movie", SizeBytes: 10 * 1024 * 1024 * 1024}  // 10GB
	small := models.NZBResult{Title: "Movie", SizeBytes: 1 * 1024 * 1024 * 1024} // 1GB

	sBig, _ := ScoreResult(big, ctx)
	sSmall, _ := ScoreResult(small, ctx)

	if sBig <= sSmall {
		t.Fatalf("expected bigger file (%d) > smaller file (%d)", sBig, sSmall)
	}
}

func TestScoreResult_DisabledCriteria(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingServicePriority, Name: "Service Priority", Enabled: false, Order: 0},
		},
		ServicePriority: config.StreamingServicePriorityUsenet,
	}

	usenet := models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeUsenet}
	score, breakdown := ScoreResult(usenet, ctx)

	if score != 0 {
		t.Fatalf("expected 0 score with disabled criterion, got %d", score)
	}
	if len(breakdown) != 0 {
		t.Fatalf("expected no breakdown items with disabled criterion, got %d", len(breakdown))
	}
}

func TestScoreResult_YearMatchTiebreaker(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{},
	}

	with := models.NZBResult{Title: "Test", Attributes: map[string]string{"yearMatch": "true"}}
	without := models.NZBResult{Title: "Test", Attributes: map[string]string{"yearMatch": "false"}}

	sWith, _ := ScoreResult(with, ctx)
	sWithout, _ := ScoreResult(without, ctx)

	if sWith <= sWithout {
		t.Fatalf("expected year match (%d) > no year match (%d)", sWith, sWithout)
	}
}

func TestScoreResult_BreakdownHasReasons(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 1},
		},
	}

	r := models.NZBResult{Title: "Movie 2160p", SizeBytes: 5 * 1024 * 1024 * 1024}
	_, breakdown := ScoreResult(r, ctx)

	if len(breakdown) < 2 {
		t.Fatalf("expected at least 2 breakdown items, got %d", len(breakdown))
	}

	for _, b := range breakdown {
		if b.Criterion == "" {
			t.Fatal("breakdown item missing criterion name")
		}
		if b.Reason == "" {
			t.Fatal("breakdown item missing reason")
		}
	}
}

func TestScoreResult_PriorityOrderMatters(t *testing.T) {
	// Higher-priority criteria (lower position index) should have higher weight
	// Position 0 = 1000pts, Position 1 = 500pts
	// So preferred terms at position 0 should dominate resolution at position 1
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		},
		PreferredTerms: filter.CompileTerms([]string{"remux"}),
	}

	// Remux 720p vs non-remux 2160p
	remux720 := models.NZBResult{Title: "Movie Remux 720p"}
	plain4k := models.NZBResult{Title: "Movie 2160p BluRay"}

	sRemux, _ := ScoreResult(remux720, ctx)
	sPlain, _ := ScoreResult(plain4k, ctx)

	if sRemux <= sPlain {
		t.Fatalf("expected preferred term match at position 0 (%d) > resolution at position 1 (%d)", sRemux, sPlain)
	}
}

func TestScoreResult_WeightedPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"DV=3", "REMUX=2", "HDR"}),
	}

	// Matches DV(3) + REMUX(2) + HDR(1) = weight 6
	allMatch := models.NZBResult{Title: "Movie.2024.REMUX.DV.HDR"}
	// Matches only DV(3) = weight 3
	dvOnly := models.NZBResult{Title: "Movie.2024.DV.x265"}
	// No match = weight 0
	noMatch := models.NZBResult{Title: "Movie.2024.1080p.BluRay"}

	sAll, _ := ScoreResult(allMatch, ctx)
	sDV, _ := ScoreResult(dvOnly, ctx)
	sNone, _ := ScoreResult(noMatch, ctx)

	if sAll <= sDV {
		t.Fatalf("expected all-match (%d) > DV-only (%d)", sAll, sDV)
	}
	if sDV <= sNone {
		t.Fatalf("expected DV-only (%d) > no-match (%d)", sDV, sNone)
	}
}

func TestScoreResult_WeightedNonPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingNonPreferredTerms, Name: "Non-Preferred Terms", Enabled: true, Order: 0},
		},
		NonPreferredTerms: filter.CompileTerms([]string{"CAM=3", "HDTS"}),
	}

	cam := models.NZBResult{Title: "Movie.2024.CAM"}      // weight 3
	hdts := models.NZBResult{Title: "Movie.2024.HDTS"}    // weight 1
	clean := models.NZBResult{Title: "Movie.2024.BluRay"} // weight 0

	sCam, _ := ScoreResult(cam, ctx)
	sHdts, _ := ScoreResult(hdts, ctx)
	sClean, _ := ScoreResult(clean, ctx)

	if sCam >= sHdts {
		t.Fatalf("expected CAM=3 (%d) < HDTS=1 (%d) — higher weight = more penalty", sCam, sHdts)
	}
	if sHdts >= sClean {
		t.Fatalf("expected HDTS (%d) < clean (%d)", sHdts, sClean)
	}
}

func TestScoreResult_MultipleWeightedTermsSum(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"DV=3", "HDR=2"}),
	}

	// Matches both DV(3) + HDR(2) = 5
	both := models.NZBResult{Title: "Movie.DV.HDR.2024"}
	// Matches only DV(3) = 3
	dvOnly := models.NZBResult{Title: "Movie.DV.2024"}

	sBoth, _ := ScoreResult(both, ctx)
	sDV, _ := ScoreResult(dvOnly, ctx)

	if sBoth <= sDV {
		t.Fatalf("expected both match (%d) > DV-only (%d)", sBoth, sDV)
	}
}

func TestScoreResult_BackwardCompat_NoWeights(t *testing.T) {
	// Terms without =N suffix should still work (weight defaults to 1)
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"remux"}),
	}

	with := models.NZBResult{Title: "Movie 2024 Remux 1080p"}
	without := models.NZBResult{Title: "Movie 2024 BluRay 1080p"}

	scoreWith, _ := ScoreResult(with, ctx)
	scoreWithout, _ := ScoreResult(without, ctx)

	if scoreWith <= scoreWithout {
		t.Fatalf("expected preferred term match (%d) > no match (%d)", scoreWith, scoreWithout)
	}
}

func TestScoreResult_DownloadPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		},
		DownloadPreferredTerms: filter.CompileTerms([]string{"season pack=3", "complete"}),
		UseDownloadRanking:     true,
	}

	match := models.NZBResult{Title: "Show.S01.1080p.Season.Pack.Complete"}
	plain := models.NZBResult{Title: "Show.S01E01.2160p"}

	scoreMatch, breakdownMatch := ScoreResult(match, ctx)
	scorePlain, _ := ScoreResult(plain, ctx)

	if scoreMatch <= scorePlain {
		t.Fatalf("expected download preferred match (%d) > plain result (%d)", scoreMatch, scorePlain)
	}
	found := false
	for _, item := range breakdownMatch {
		if item.Criterion == "Download Preferred Terms" {
			found = true
			if item.Points <= 0 {
				t.Fatalf("expected positive download preferred term score, got %d", item.Points)
			}
		}
	}
	if !found {
		t.Fatal("expected download preferred terms breakdown item")
	}
}

func TestScoreResult_DownloadPreferredTermsIgnoredWhenDisabled(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		},
		DownloadPreferredTerms: filter.CompileTerms([]string{"x265=3"}),
		UseDownloadRanking:     false,
	}

	match := models.NZBResult{Title: "Show.S01.1080p.WEBRip.x265"}
	plain := models.NZBResult{Title: "Show.S01E01.2160p.WEB-DL"}

	scoreMatch, breakdownMatch := ScoreResult(match, ctx)
	scorePlain, _ := ScoreResult(plain, ctx)

	if scoreMatch >= scorePlain {
		t.Fatalf("expected disabled download terms not to outrank 2160p result (%d >= %d)", scoreMatch, scorePlain)
	}
	for _, item := range breakdownMatch {
		if item.Criterion == "Download Preferred Terms" {
			t.Fatal("did not expect download preferred terms breakdown when download ranking is disabled")
		}
	}
}

func TestSortResultsByScore_NegativeScoresLast(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingNonPreferredTerms, Name: "Non-Preferred Terms", Enabled: true, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		},
		NonPreferredTerms: filter.CompileTerms([]string{"french=2"}),
	}

	results := []models.NZBResult{
		{Title: "Wayward.Pines.S01E03.FRENCH.1080p.WEB-DL"},
		{Title: "Wayward.Pines.S01E03.DVDRip"},
		{Title: "Wayward.Pines.S01E03.1080p.WEB-DL"},
	}

	(&Service{}).sortResultsByScore(results, ctx)

	if results[0].Title != "Wayward.Pines.S01E03.1080p.WEB-DL" {
		t.Fatalf("expected positive-scored result first, got %q", results[0].Title)
	}
	if results[1].Title != "Wayward.Pines.S01E03.DVDRip" {
		t.Fatalf("expected neutral-scored result second, got %q", results[1].Title)
	}
	if results[2].Title != "Wayward.Pines.S01E03.FRENCH.1080p.WEB-DL" {
		t.Fatalf("expected negative-scored result last, got %q", results[2].Title)
	}
}
