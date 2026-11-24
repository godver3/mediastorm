package arr

import (
	"regexp"
	"strconv"

	ptt "github.com/itsrenoria/ptt-go"
)

const epEnd = `(?:$|[^0-9a-z])`

var (
	// S01E01E02, S01E01-E02, S01E01-02, S01E01.S01E02, S01E01 - S01E02
	reMultiSxE = regexp.MustCompile(`(?i)s\d{1,4}[ ._-]?e\d{1,4}(?:(?:[._-]?e|-|[ ._]-[ ._]?e|[ .]-[ .]s\d{1,4}[ .]?e|[.-]s\d{1,4}[ .]?e)\d{1,4})+` + epEnd)
	// 1x01-02, 2x09-2x10, 1x01-x03, 8x01_02, 2x04.2x05, 02x01x02
	reMultiNxN = regexp.MustCompile(`(?i)\b\d{1,2}x\d{1,3}(?:(?:-|_|-\d{1,2}x|-x|\.\d{1,2}x|x)\d{1,3})+` + epEnd)
	// "Season 1 Episode 05-06", "S42 Ep10688 - Ep10692", "S01 - 01-02"
	reMultiWords = regexp.MustCompile(`(?i)(?:episode\s?\d{1,4}\s?-\s?\d{1,4}|ep\.?\s?\d{1,5}\s?-\s?ep\.?\s?\d{1,5}|\bs\d{1,2}\s-\s\d{1,4}-\d{1,4}\b)`)

	reSingleSxE  = regexp.MustCompile(`(?i)s\d{1,4}[ ._-]?e\d{1,4}` + epEnd)
	reSingleNxN  = regexp.MustCompile(`(?i)\b\d{1,2}x\d{2,3}\b`)
	reSingleWord = regexp.MustCompile(`(?i)\b(?:s\d{1,2}\s-\s\d{1,4}|s\d{1,2}\s(?:\d{3}|1[0-8]\d{2})|season\s?\d{1,3}\s?ep\.?\s?\d{1,4}|episode\s?\d{1,4})\b`)
	// Absolute numbering: "Title - 23", "Title_-_03", "E1135", "EP06", "[17]".
	reAbsolute = regexp.MustCompile(`(?i)(?:\s-\s\d{2,4}(?:v\d)?(?:\s|$|[\[(.])|_-_\d{2,4}|\b(?:e|ep|episodio|episode)\.?\s?\d{1,4}\b|\]\[\d{2,4}\]|\s\d{2,4}\s\[)`)
	reDaily    = regexp.MustCompile(`(?:^|[^0-9])(?:19|20)\d{2}[ ._-](?:0[1-9]|1[0-2]|\d{2})[ ._-]\d{2}(?:$|[^0-9])`)

	reSeasonPack = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\d{1,4}(?:[ ._-]?-[ ._-]?s?\d{1,4})?|season[ ._-]?\d{1,4}|saison[ ._-]?\d{1,4}|stagione[ ._-]?\d{1,4}|complete[ ._-]series)(?:$|[^a-z0-9e])`)
	// Partial seasons and specials are not full season packs.
	// "S01.Part.2", "Season 1 Part 2", "Season.1.Vol.2", "S01 Special".
	rePartialSeason = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\d{1,4}|season[ ._-]?\d{1,4})[ ._-](?:part[ ._-]?\d|p\d|vol(?:ume)?[ ._-]?\d|special)`)
	// Anime batches with a tilde range: "[Erai-raws] Show - 01 ~ 12 [1080p]"
	// (Sonarr: absoluteepisode \s?~\s? absoluteepisode).
	reAbsoluteTildeRange = regexp.MustCompile(`(?:\s-\s?|_-_)(\d{2,4})(?:\.\d{1,2})?\s?~\s?(\d{2,4})(?:\.\d{1,2})?(?:$|[^0-9])`)
)

// parseReleaseType classifies a title. ptt-go is only consulted (lazily)
// when none of the episode patterns decide, because it is comparatively slow.
func parseReleaseType(title string, lazyInfo func() *ptt.TorrentInfo) (ReleaseType, bool, int) {
	if n := tildeRangeEpisodes(title); n > 1 {
		return ReleaseTypeMultiEpisode, false, n
	}
	switch {
	case reMultiSxE.MatchString(title), reMultiNxN.MatchString(title), reMultiWords.MatchString(title):
		n, implausible := countEpisodes(title)
		if n < 2 && implausible {
			// "S01E05-100.Days": an episode title, not a 96-episode range.
			return ReleaseTypeSingleEpisode, false, 1
		}
		if n < 2 {
			n = 2
		}
		return ReleaseTypeMultiEpisode, false, n
	case reSingleSxE.MatchString(title), reSingleNxN.MatchString(title), reSingleWord.MatchString(title):
		return ReleaseTypeSingleEpisode, false, 1
	case reDaily.MatchString(title):
		// Daily episodes have no *arr release type.
		return ReleaseTypeUnknown, false, 1
	case rePartialSeason.MatchString(title):
		return ReleaseTypeUnknown, false, 0
	case reSeasonPack.MatchString(title):
		return ReleaseTypeSeasonPack, true, 0
	case reAbsolute.MatchString(title):
		return ReleaseTypeSingleEpisode, false, 1
	}
	info := lazyInfo()
	episodes := 0
	if info != nil {
		episodes = len(info.Episodes)
	}
	if info != nil {
		switch {
		case episodes > 1:
			return ReleaseTypeMultiEpisode, false, episodes
		case episodes == 1:
			return ReleaseTypeSingleEpisode, false, 1
		case len(info.Seasons) > 0 && episodes == 0:
			return ReleaseTypeSeasonPack, true, 0
		}
	}
	return ReleaseTypeUnknown, false, 0
}

var (
	reEpisodeRange = regexp.MustCompile(`(?i)e(\d{1,4})[ ._]?-[ ._]?(e?)(\d{1,4})\b`)
	reNxNRange     = regexp.MustCompile(`(?i)\b\d{1,2}x(\d{1,3})(-\d{1,2}x|-x|-)(\d{1,3})\b`)
	reEpisodeNum   = regexp.MustCompile(`(?i)e\d{1,4}`)
)

const (
	// maxEpisodeRange caps season/episode ranges ("S01E01-E24"); anything
	// wider is a misread, not a release.
	maxEpisodeRange = 100
	// maxAbsoluteRange caps anime batch ranges ("001 ~ 1000").
	maxAbsoluteRange = 2000
)

// plausibleRange reports whether first-last is a believable episode range.
// An unprefixed end that has more digits than the start ("E05-100") is an
// episode title, not a range.
func plausibleRange(first, last string, prefixed bool, limit int) (int, bool) {
	a, errA := strconv.Atoi(first)
	b, errB := strconv.Atoi(last)
	if errA != nil || errB != nil || b <= a || b-a+1 > limit {
		return 0, false
	}
	if !prefixed && len(last) > len(first) {
		return 0, false
	}
	return b - a + 1, true
}

// countEpisodes estimates how many episodes a multi-episode title covers.
// implausible reports a range that was rejected as such.
func countEpisodes(title string) (n int, implausible bool) {
	for _, re := range []*regexp.Regexp{reEpisodeRange, reNxNRange} {
		if m := re.FindStringSubmatch(title); m != nil {
			// "E01-E04", "1x01-1x04" and "1x01-x04" carry a prefix; "E01-04" and
			// "1x01-04" do not.
			prefixed := m[2] != "" && m[2] != "-"
			if count, ok := plausibleRange(m[1], m[3], prefixed, maxEpisodeRange); ok {
				return count, false
			}
			implausible = true
		}
	}
	return len(reEpisodeNum.FindAllString(title, -1)), implausible
}

// tildeRangeEpisodes counts the episodes of an anime batch "01 ~ 12".
func tildeRangeEpisodes(title string) int {
	m := reAbsoluteTildeRange.FindStringSubmatch(title)
	if m == nil {
		return 0
	}
	n, _ := plausibleRange(m[1], m[2], true, maxAbsoluteRange)
	return n
}
