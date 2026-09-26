package filter

import (
	"testing"

	"novastream/models"
)

func TestResults_StormOfTheCenturyMetadataAlias(t *testing.T) {
	cases := []struct {
		title string
		keep  bool
	}{
		{"Stephen.Kings.Storm.of.the.Century.S01E01.Storm.of.the.Century.Part.1.1080p.HULU.WEB-DL.AAC2.0.H.264-NINJACENTRAL", true},
		{"Stephen.Kings.Storm.of.the.Century.S01.1080p.HULU.WEBRip.AAC2.0.x264-NOGRP", true},
		{"Stephen.Kings.Storm.of.the.Century.S01.COMPLETE.720p.HULU.WEBRip", true},
		{"Stephen King's Storm of the Century S01E01 1080p WEB-DL", true},
		{"Stephen King’s Storm of the Century S01E01 1080p WEB-DL", true},
		{"Storm.of.the.Century.S01E01.1080p.WEB-DL", true},
		{"Stephen.Kings.Storm.Of.The.Century.S01E02.1080p.WEB.H264-SKYFiRE", false},
		{"Stephen.Kings.Storm.Of.The.Century.S02E01.1080p.WEB.H264-SKYFiRE", false},
		{"Stephen.Kings.Storm.Of.The.Century.S01E01.1080p.AV1.10bit-MeGusta", false},
		{"Stephen.Kings.The.Stand.S01E01.1080p.WEB-DL", false},
		{"Documentary.About.Storm.of.the.Century.S01E01.1080p.WEB-DL", false},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := Results([]models.NZBResult{{Title: tc.title}}, Options{
				ExpectedTitle:   "Storm of the Century",
				AlternateTitles: []string{"Stephen King's Storm of the Century"},
				ExpectedYear:    1999,
				TargetSeason:    1,
				TargetEpisode:   1,
				FilterOutTerms:  []string{"av1"},
			})
			if (len(got) == 1) != tc.keep {
				t.Fatalf("kept = %t, want %t", len(got) == 1, tc.keep)
			}
			if tc.keep && got[0].Attributes["titleMatch"] != "strong" {
				t.Fatal("expected strong title identity")
			}
		})
	}
}
