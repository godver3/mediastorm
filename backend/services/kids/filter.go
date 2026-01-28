package kids

import (
	"strings"

	"novastream/models"
)

// Rating hierarchies - lower number = more restrictive
var movieRatingOrder = map[string]int{
	"G":     1,
	"PG":    2,
	"PG-13": 3,
	"R":     4,
	"NC-17": 5,
	"NR":    6, // Not Rated - treat as most permissive
}

var tvRatingOrder = map[string]int{
	"TV-Y":   1,
	"TV-Y7":  2,
	"TV-Y7-FV": 2, // Fantasy violence variant of TV-Y7
	"TV-G":   3,
	"TV-PG":  4,
	"TV-14":  5,
	"TV-MA":  6,
	"NR":     7, // Not Rated - treat as most permissive
}

// GetRatingLevel returns the restrictiveness level for a rating.
// Lower numbers are more restrictive. Returns 0 if rating is unknown.
func GetRatingLevel(certification, mediaType string) int {
	cert := strings.ToUpper(strings.TrimSpace(certification))
	if cert == "" {
		return 0 // Unknown rating
	}

	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "movie" {
		return movieRatingOrder[cert]
	}
	// series or tv
	return tvRatingOrder[cert]
}

// IsRatingAllowed checks if a content's rating is allowed given the maximum rating.
// mediaType should be "movie" or "series".
// Returns true if the content is allowed, false if it should be blocked.
func IsRatingAllowed(certification, maxRating, mediaType string) bool {
	// If no max rating is set, allow everything
	if strings.TrimSpace(maxRating) == "" {
		return true
	}

	certification = strings.TrimSpace(certification)
	// If content has no rating, block it for safety in kids mode
	if certification == "" {
		return false
	}

	contentLevel := GetRatingLevel(certification, mediaType)
	maxLevel := GetRatingLevel(maxRating, mediaType)

	// If either level is 0, we can't determine - block for safety
	if contentLevel == 0 || maxLevel == 0 {
		return false
	}

	return contentLevel <= maxLevel
}

// FilterTrendingByRating filters a list of TrendingItems by rating.
// Items without a certification or with a rating exceeding maxRating are removed.
// Deprecated: Use FilterTrendingByRatings with separate movie/TV ratings instead.
func FilterTrendingByRating(items []models.TrendingItem, maxRating string) []models.TrendingItem {
	return FilterTrendingByRatings(items, maxRating, maxRating)
}

// FilterTrendingByRatings filters a list of TrendingItems by rating.
// Uses separate max ratings for movies and TV shows.
// Items without a certification or with a rating exceeding the max are removed.
func FilterTrendingByRatings(items []models.TrendingItem, maxMovieRating, maxTVRating string) []models.TrendingItem {
	// If both ratings are empty, no filter applied
	if strings.TrimSpace(maxMovieRating) == "" && strings.TrimSpace(maxTVRating) == "" {
		return items
	}

	result := make([]models.TrendingItem, 0, len(items))
	for _, item := range items {
		mediaType := strings.ToLower(item.Title.MediaType)
		var maxRating string
		if mediaType == "movie" {
			maxRating = maxMovieRating
		} else {
			maxRating = maxTVRating
		}
		// If no rating set for this media type, allow all
		if strings.TrimSpace(maxRating) == "" {
			result = append(result, item)
			continue
		}
		if IsRatingAllowed(item.Title.Certification, maxRating, item.Title.MediaType) {
			result = append(result, item)
		}
	}
	return result
}

// FilterTitlesByRating filters a list of Titles by rating.
func FilterTitlesByRating(titles []models.Title, maxRating string) []models.Title {
	if strings.TrimSpace(maxRating) == "" {
		return titles // No filter applied
	}

	result := make([]models.Title, 0, len(titles))
	for _, title := range titles {
		if IsRatingAllowed(title.Certification, maxRating, title.MediaType) {
			result = append(result, title)
		}
	}
	return result
}

// IsListAllowed checks if a list URL is in the allowed lists for a kids profile.
func IsListAllowed(listURL string, allowedLists []string) bool {
	if len(allowedLists) == 0 {
		return true // No restrictions
	}

	listURL = strings.TrimSpace(listURL)
	if listURL == "" {
		return false
	}

	for _, allowed := range allowedLists {
		if strings.TrimSpace(allowed) == listURL {
			return true
		}
	}
	return false
}

// GetDefaultRatingForMode returns the default max rating for a kids mode.
func GetDefaultRatingForMode(mode string) string {
	switch mode {
	case "rating", "both":
		return "G" // Default to G for rating-based mode
	default:
		return ""
	}
}

// ValidateKidsMode checks if a mode string is valid.
func ValidateKidsMode(mode string) bool {
	switch mode {
	case "", "rating", "content_list", "both":
		return true
	default:
		return false
	}
}

// ValidateRating checks if a rating string is valid (known MPAA or TV rating).
func ValidateRating(rating string) bool {
	rating = strings.ToUpper(strings.TrimSpace(rating))
	if rating == "" {
		return true // Empty is valid (no restriction)
	}
	_, movieOK := movieRatingOrder[rating]
	_, tvOK := tvRatingOrder[rating]
	return movieOK || tvOK
}
