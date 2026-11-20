package jellyfinbridge

import (
	"regexp"
	"strings"
	"time"

	"novastream/models"
)

// Jellyfin's Live TV page lists programmes in rows (series, movies, sports,
// kids, news) by the programme's IsSeries/IsMovie/... flags. Guides rarely
// say outright what a programme is: XMLTV categories are often genres in the
// guide's language ("Krimi", "Nachrichten"), and many guides carry no
// episode numbers. The flags are derived from what is there.

type programKind struct {
	movie, series, sports, news, kids bool
}

// movieMinRuntime separates a film from an episode of a fiction genre: a TV
// episode runs up to an hour, a feature film rarely under 80 minutes.
const movieMinRuntime = 80 * time.Minute

// Category words, lowercase, matched as whole category names or words in
// them (English and German guides).
var (
	movieCategories  = []string{"movie", "film", "spielfilm", "feature film", "kinofilm", "tv movie", "fernsehfilm"}
	seriesCategories = []string{"series", "serie", "sitcom", "soap", "dokusoap", "telenovela", "reality", "animation", "zeichentrick", "episode"}
	sportsCategories = []string{"sport", "sports", "fußball", "fussball", "football", "soccer", "tennis", "formel 1", "motorsport", "handball", "basketball", "eishockey", "wintersport", "radsport", "boxen"}
	newsCategories   = []string{"news", "nachrichten", "newscast", "weather", "wetter"}
	kidsCategories   = []string{"kids", "children", "kinder", "kindersendung", "jugend"}
	// fictionCategories are genres of films and series alike; runtime decides.
	fictionCategories = []string{"drama", "krimi", "crime", "thriller", "action", "abenteuer", "adventure", "comedy", "komödie", "fantasy", "sciencefiction", "science fiction", "sci-fi", "horror", "western", "romantik", "romance", "mystery", "war", "kriegsfilm", "historienfilm", "liebesfilm", "family drama"}
)

// episodeInTitle matches an episode number guides put in the title,
// "Morden im Norden (96)" or "Folge 12".
var episodeInTitle = regexp.MustCompile(`(?i)(\(\d{1,4}\)\s*$|\bfolge\s+\d+|\bepisode\s+\d+|\bs\d{1,2}\s?e\d{1,3}\b)`)

func classifyProgram(prog models.EPGProgram) programKind {
	cats := make([]string, 0, len(prog.Categories))
	for _, c := range prog.Categories {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			cats = append(cats, c)
		}
	}
	has := func(words []string) bool {
		for _, c := range cats {
			for _, w := range words {
				if c == w || strings.HasPrefix(c, w+" ") || strings.HasSuffix(c, " "+w) || strings.Contains(c, " "+w+" ") {
					return true
				}
			}
		}
		return false
	}
	var k programKind
	k.sports = has(sportsCategories)
	k.news = has(newsCategories)
	k.kids = has(kidsCategories)
	episode := prog.Episode != "" || episodeInTitle.MatchString(prog.Title)
	runtime := prog.Stop.Sub(prog.Start)
	switch {
	case k.sports || k.news:
		// Sport and news are their own rows, never films or series.
	case has(movieCategories):
		k.movie = !episode
		k.series = episode
	case has(seriesCategories) || episode:
		k.series = true
	case has(fictionCategories):
		k.movie = runtime >= movieMinRuntime
		k.series = !k.movie
	}
	return k
}

// matches reports whether the programme passes the query's kind filters.
func (k programKind) matches(q programQueryParams) bool {
	for _, f := range []struct {
		want *bool
		is   bool
	}{{q.IsMovie, k.movie}, {q.IsSeries, k.series}, {q.IsSports, k.sports}, {q.IsNews, k.news}, {q.IsKids, k.kids}} {
		if f.want != nil && *f.want != f.is {
			return false
		}
	}
	return true
}
