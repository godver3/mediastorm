package arr

import "testing"

// Anime naming that preset ranking depends on: absolute numbers, version
// suffixes (a v2 is a revision, ranked like a PROPER) and batches.
func TestParseAnimeReleaseNames(t *testing.T) {
	cases := []struct {
		title   string
		typ     ReleaseType
		count   int
		version int
	}{
		{"[SubsPlease] One Piece - 1001 (1080p) [ABCD1234].mkv", ReleaseTypeSingleEpisode, 1, 1},
		{"[Erai-raws] Frieren - 01v2 [1080p][Multiple Subtitle].mkv", ReleaseTypeSingleEpisode, 1, 2},
		{"[Judas] Mushoku Tensei S2 - 05 [1080p][HEVC x265 10bit].mkv", ReleaseTypeSingleEpisode, 1, 1},
		{"[Anime Time] Frieren - 01-12 [Batch] [1080p][HEVC 10bit x265][AAC][Multi Sub]", ReleaseTypeMultiEpisode, 12, 1},
		{"Show.S01E01v2.1080p.WEB.h264-GRP", ReleaseTypeSingleEpisode, 1, 2},
	}
	for _, tc := range cases {
		r := Parse(AppSonarr, tc.title)
		if r.ReleaseType != tc.typ || r.EpisodeCount != tc.count || r.Revision.Version != tc.version {
			t.Errorf("%s: type %v count %d version %d, want %v %d %d", tc.title, r.ReleaseType, r.EpisodeCount, r.Revision.Version, tc.typ, tc.count, tc.version)
		}
	}
}
