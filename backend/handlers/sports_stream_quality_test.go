package handlers

import (
	"novastream/models"
	"testing"
)

func TestReportedSportsQuality(t *testing.T) {
	for _, tc := range []struct {
		label   string
		height  int
		bitrate int64
	}{
		{"Game 2160p 12 Mbps", 2160, 12000000}, {"Game 4K", 2160, 0}, {"Game FHD", 1080, 0}, {"Game 720p", 720, 0},
		{"Game HD", 0, 0}, {"Game 1080 fans", 0, 0}, {"Game 4K 720p", 720, 0}, {"Team UHD 50fps", 2160, 0},
	} {
		q := reportedSportsQuality(tc.label)
		if tc.height == 0 && tc.bitrate == 0 {
			if q != nil {
				t.Errorf("%q should be unknown: %+v", tc.label, q)
			}
			continue
		}
		if q == nil || q.ResolutionHeight != tc.height || q.BitrateBps != tc.bitrate || q.Origin != "label" {
			t.Errorf("%q: %+v", tc.label, q)
		}
	}
}
func TestSportsQualityRankingPreservesConfidence(t *testing.T) {
	matches := []models.SportsStreamMatch{
		{ChannelName: "Possible 4K", Confidence: .74},
		{ChannelName: "Z 1080p", Confidence: .9},
		{ChannelName: "A 720p", Confidence: .9},
	}
	sortSportsStreamMatches(matches)
	if matches[0].ChannelName != "Z 1080p" || matches[1].ChannelName != "A 720p" || matches[2].ChannelName != "Possible 4K" {
		t.Fatalf("wrong ranking: %+v", matches)
	}
}
func TestStremioQualityKeepsOriginalIndexes(t *testing.T) {
	got := playableStremioStreamOptions([]stremioStream{{URL: "https://example.test/a", Name: "720p"}, {URL: "magnet:unsupported", Name: "4K"}, {URL: "https://example.test/b", Description: "4K"}})
	if len(got) != 2 || got[0].Index != 2 || got[1].Index != 0 || got[0].ReportedQuality.ResolutionHeight != 2160 {
		t.Fatalf("wrong options %+v", got)
	}
}
