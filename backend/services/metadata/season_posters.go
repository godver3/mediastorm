package metadata

import (
	"net/url"
	"path"
	"strings"

	"novastream/models"
)

// TVDB season.image may be a wide banner rather than a season poster.
func isPortraitSeasonImage(image *models.Image) bool {
	if image == nil || strings.TrimSpace(image.URL) == "" {
		return false
	}
	if image.Width > 0 && image.Height > 0 && image.Width >= image.Height {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(image.Type)) {
	case "banner", "backdrop", "background", "fanart", "thumb", "still", "logo":
		return false
	}
	u, err := url.Parse(image.URL)
	if err != nil {
		return false
	}
	if strings.HasSuffix(strings.ToLower(u.Hostname()), ".thetvdb.com") {
		p := strings.ToLower(u.Path)
		if strings.Contains(p, "/seasonwide/") || strings.Contains(p, "/seasonswide/") ||
			strings.Contains(p, "/fanart/") || strings.Contains(p, "/backgrounds/") ||
			strings.Contains(p, "/images/missing/") ||
			(strings.Contains(p, "/season/") && path.Base(path.Dir(p)) == "banners") {
			return false
		}
	}
	return true
}

// Select once while constructing the season, before storing details in cache.
// A series poster remains available without TMDB and for alternate orderings.
func tvdbSeasonPoster(source string, seriesPoster *models.Image) *models.Image {
	image := newTVDBImage(source, "poster", 0, 0)
	if isPortraitSeasonImage(image) {
		return image
	}
	if isPortraitSeasonImage(seriesPoster) {
		copy := *seriesPoster
		return &copy
	}
	return nil
}

// Reuse the poster carried by an existing episode-enrichment response. This
// never fetches anything and never maps aired-season covers onto other orders.
func applyTMDBSeasonPoster(details *models.SeriesDetails, number int, poster *models.Image) bool {
	ordering := strings.ToLower(strings.TrimSpace(details.ActiveOrdering))
	if (ordering != "" && ordering != "official" && ordering != "default") || !isPortraitSeasonImage(poster) {
		return false
	}
	for i := range details.Seasons {
		season := &details.Seasons[i]
		if season.Number != number {
			continue
		}
		fallback := season.Image == nil
		for _, image := range []*models.Image{details.Title.Poster, details.Title.TextPoster} {
			if image != nil && season.Image != nil && image.URL == season.Image.URL {
				fallback = true
			}
		}
		if fallback && (season.Image == nil || season.Image.URL != poster.URL) {
			copy := *poster
			season.Image = &copy
			return true
		}
	}
	return false
}
