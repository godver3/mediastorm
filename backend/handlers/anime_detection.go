package handlers

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"novastream/models"
)

func isAnimeTitle(title *models.Title) bool {
	anime, _ := animeDecision(title)
	return anime
}

// animeDecision tells whether a title is anime and why, for logs: the genre
// Anime, or Animation together with an East Asian original language, title
// script or airing timezone.
func animeDecision(title *models.Title) (bool, string) {
	if title == nil {
		return false, "no metadata"
	}

	hasAnimeGenre := false
	hasAnimationGenre := false
	for _, genre := range title.Genres {
		switch strings.ToLower(strings.TrimSpace(genre)) {
		case "anime":
			hasAnimeGenre = true
		case "animation":
			hasAnimationGenre = true
		}
	}

	if hasAnimeGenre {
		return true, "genre Anime"
	}
	if !hasAnimationGenre {
		return false, "no Anime or Animation genre"
	}

	if isEastAsianLanguageCode(title.Language) {
		return true, "genre Animation, original language " + title.Language
	}
	if hasEastAsianScript(title.OriginalName) {
		return true, "genre Animation, original title " + title.OriginalName
	}
	for _, alt := range title.AlternateTitles {
		if hasEastAsianScript(alt) {
			return true, "genre Animation, alternate title " + alt
		}
	}

	switch strings.TrimSpace(title.AirsTimezone) {
	case "Asia/Tokyo", "Asia/Shanghai", "Asia/Chongqing", "Asia/Harbin", "Asia/Urumqi",
		"Asia/Taipei", "Asia/Hong_Kong", "Asia/Macau", "Asia/Seoul":
		return true, "genre Animation, airs in " + title.AirsTimezone
	default:
		return false, "genre Animation without East Asian origin"
	}
}

// animeLogged remembers titles whose decision was logged, so each is
// logged once per process (bounded).
var animeLogged sync.Map

var animeLoggedCount atomic.Int64

// logAnimeDecision logs a title's anime decision once.
func logAnimeDecision(key string, title *models.Title) bool {
	anime, reason := animeDecision(title)
	if key == "" && title != nil {
		key = title.ID
	}
	if key != "" && animeLoggedCount.Load() < 50_000 {
		if _, seen := animeLogged.LoadOrStore(key, anime); !seen {
			animeLoggedCount.Add(1)
			name := key
			if title != nil && title.Name != "" {
				name = title.Name
			}
			log.Printf("[anime] %q anime=%v: %s", name, anime, reason)
		}
	}
	return anime
}

func hydratedMovieSearchTitles(title *models.Title) []string {
	if title == nil {
		return nil
	}
	titles := make([]string, 0, len(title.AlternateTitles)+2)
	if canonical := strings.TrimSpace(title.Name); canonical != "" {
		titles = append(titles, canonical)
	}
	if original := strings.TrimSpace(title.OriginalName); original != "" {
		titles = append(titles, original)
	}
	titles = append(titles, title.AlternateTitles...)
	return titles
}

func isEastAsianLanguageCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "ja", "jpn", "jp", "zh", "zho", "chi", "cn", "ko", "kor", "kr":
		return true
	default:
		return false
	}
}

func hasEastAsianScript(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// TitleAnime tells whether a title is anime, and why, from its metadata.
func (h *IndexerHandler) TitleAnime(ctx context.Context, mediaType, titleID, name string, year int, imdbID string) (bool, string) {
	switch mediaType {
	case "series":
		if h.MetadataSvc == nil {
			return false, "no metadata service"
		}
		details, err := h.MetadataSvc.SeriesDetails(ctx, models.SeriesDetailsQuery{TitleID: titleID, Name: name, Year: year, IMDBID: imdbID})
		if err != nil || details == nil {
			return false, "no metadata"
		}
		logAnimeDecision("", &details.Title)
		return animeDecision(&details.Title)
	case "movie":
		if h.MovieMetadataSvc == nil {
			return false, "no metadata service"
		}
		title, err := h.MovieMetadataSvc.MovieInfo(ctx, models.MovieDetailsQuery{TitleID: titleID, Name: name, Year: year, IMDBID: imdbID})
		if err != nil || title == nil {
			return false, "no metadata"
		}
		logAnimeDecision("", title)
		return animeDecision(title)
	}
	return false, "unknown media type"
}
