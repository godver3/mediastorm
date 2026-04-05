package handlers

import (
	"strings"
	"unicode"

	"novastream/models"
)

func isAnimeTitle(title *models.Title) bool {
	if title == nil {
		return false
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
		return true
	}
	if !hasAnimationGenre {
		return false
	}

	if isEastAsianLanguageCode(title.Language) {
		return true
	}
	if hasEastAsianScript(title.OriginalName) {
		return true
	}
	for _, alt := range title.AlternateTitles {
		if hasEastAsianScript(alt) {
			return true
		}
	}

	switch strings.TrimSpace(title.AirsTimezone) {
	case "Asia/Tokyo", "Asia/Shanghai", "Asia/Chongqing", "Asia/Harbin", "Asia/Urumqi",
		"Asia/Taipei", "Asia/Hong_Kong", "Asia/Macau", "Asia/Seoul":
		return true
	default:
		return false
	}
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
