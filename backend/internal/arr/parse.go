package arr

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	ptt "github.com/itsrenoria/ptt-go"
)

// Parse extracts the *arr-relevant facts from a release title: quality,
// revision, languages, release group, release type and (for movies) edition
// and year. Languages are returned as parsed, before FinalizeLanguages.
func Parse(app App, title string) Release {
	title = capTitle(title)
	clean := stripExtension(strings.TrimSpace(title))
	// Custom formats match the release title without its file extension,
	// exactly like *arr's parsed ReleaseTitle.
	rel := Release{App: app, Title: clean}
	var info *ptt.TorrentInfo
	parsed := false
	lazyInfo := func() *ptt.TorrentInfo {
		if !parsed {
			info, parsed = parsePTT(clean), true
		}
		return info
	}

	rel.ReleaseGroup = parseReleaseGroup(app, clean)
	tokens := releaseTokens(clean, nil)
	rel.Quality, rel.Revision = parseQuality(app, strings.TrimSpace(title))
	rel.Languages = parseLanguagesFor(app, clean, tokens, rel.ReleaseGroup)
	if app == AppRadarr {
		if info := lazyInfo(); info != nil {
			rel.Year = info.Year
			rel.Edition = info.Edition
		}
		// Radarr matches title conditions against the "simple" title in
		// which the movie name is replaced by "A Movie".
		rel.Title = simpleMovieTitle(clean)
		if rel.Quality.Name == "TELESYNC" {
			rel.Quality.Resolution = parseResolution(clean)
		}
	} else {
		rel.ReleaseType, rel.FullSeason, rel.EpisodeCount = parseReleaseType(clean, lazyInfo)
	}
	return rel
}

// maxTitleLen bounds the work spent on one title (dozens of regexes, ptt-go
// and custom-format patterns); real release names stay far below it. Custom
// format matching applies the same cap to every matched string.
const maxTitleLen = 512

// capTitle cuts an overlong title to maxTitleLen bytes on a rune boundary.
func capTitle(title string) string {
	if len(title) <= maxTitleLen {
		return title
	}
	cut := maxTitleLen
	for cut > 0 && !utf8.RuneStart(title[cut]) {
		cut--
	}
	return title[:cut]
}

// ptt-go parsers mutate handler state during Parse, so each goroutine needs
// its own instance; the pool reuses compiled handlers.
var pttParsers = sync.Pool{New: func() any {
	p := ptt.NewParser()
	ptt.AddDefaults(p)
	return p
}}

// pttRun and pttRelease are seams for tests.
var (
	pttRun     = func(p *ptt.Parser, title string) *ptt.TorrentInfo { return p.Parse(title) }
	pttRelease = func(p *ptt.Parser) { pttParsers.Put(p) }
)

func parsePTT(title string) (info *ptt.TorrentInfo) {
	p := pttParsers.Get().(*ptt.Parser)
	defer func() {
		if recover() != nil {
			// A parser that panicked mid-Parse may hold inconsistent
			// handler state; drop it instead of returning it to the pool.
			info = nil
			return
		}
		pttRelease(p)
	}()
	return pttRun(p, title)
}

// extensionRe lists the file extensions *arr removes from a release title
// before matching (verified against Sonarr's parse endpoint).
var extensionRe = regexp.MustCompile(`(?i)\.(avc|mkv|mp4|m4v|avi|wmv|ts|m2ts|m2v|mov|webm|flv|divx|xvid|mpg|mpeg|3gp|asf|rm|rmvb|ogm|vob|wtv|strm|iso|img|bin|nzb|par2)$`)

func stripExtension(title string) string {
	return extensionRe.ReplaceAllString(title, "")
}

// releaseTokens returns the part of the title after the show/movie name,
// which is where quality and language tags live. Falling back to the whole
// title keeps detection working for unusual layouts.
var tokenStartRe = regexp.MustCompile(`(?i)(?:\b|_)(?:s\d{1,4}(?:[ ._-]?-?e\d{1,4})*|\d{1,2}x\d{2,3}|(?:19|20)\d{2}[ ._-]\d{2}[ ._-]\d{2}|(?:19|20)\d{2}|e\d{2,4}|ep\.? ?\d{1,4}|season[ ._-]?\d{1,3}|complete)(?:\b|_)`)

func releaseTokens(title string, _ *ptt.TorrentInfo) string {
	if loc := tokenStartRe.FindStringIndex(title); loc != nil && loc[0] > 0 {
		return title[loc[0]:]
	}
	return title
}

// ---------------------------------------------------------------------------

var (
	reMovieYear = regexp.MustCompile(`[ ._(\[](?:19|20)\d{2}(?:[ ._)\]]|$)`)
	// Edition and quality tags end the movie name even before the year.
	reMovieFirst = regexp.MustCompile(`(?i)[ ._(\[](?:\d{3,4}p|bluray|blu-ray|web-?dl|webrip|hdtv|dvdrip|remux|uhd|imax|extended|uncut|unrated|remastered|directors|director'?s|theatrical|special[ ._]edition|final[ ._]cut|criterion|fanedit|limited)(?:[ ._)\]-]|$)`)
	reMovieLang  = regexp.MustCompile(`(?i)[ ._](?:german|french|truefrench|english|multi)(?:[ ._-]|$)`)
)

// simpleMovieTitle replaces the movie name at the start of a release title
// with "A Movie", like Radarr's SimpleReleaseTitle. The name ends at the
// first edition/quality tag or the release year, whichever comes first;
// without either, a language tag ends it.
func simpleMovieTitle(title string) string {
	end := -1
	if loc := reMovieYear.FindStringIndex(title); loc != nil && loc[0] > 0 {
		end = loc[0]
	}
	if loc := reMovieFirst.FindStringIndex(title); loc != nil && loc[0] > 0 && (end < 0 || loc[0] < end) {
		end = loc[0]
	}
	if end < 0 {
		if loc := reMovieLang.FindStringIndex(title); loc != nil && loc[0] > 0 {
			end = loc[0]
		}
	}
	if end <= 0 {
		return title
	}
	// Like Radarr's title group, the name excludes separators before the
	// year, e.g. the space in "Name (2020)".
	name := strings.TrimRight(title[:end], " ._-")
	end = len(name)
	if name == "" || strings.HasPrefix(name, "[") || strings.HasPrefix(name, "(") {
		return title
	}
	// Radarr's Parser.ParseMovieTitle inserts "A.Movie" when the movie name
	// contains a dot and "A Movie" otherwise (also for underscores).
	if strings.Contains(name, ".") {
		return "A.Movie" + title[end:]
	}
	return "A Movie" + title[end:]
}
