package localmedia

import (
	"strings"

	"novastream/models"
)

// WithTitleIDExternalIDs fills the query's empty external IDs from the
// catalog title ID ("tmdb:tv:1399", "tvdb:series:81189", "imdb:tt0944947"):
// a library file may be matched under another ID than the catalog's, so
// every known one is compared.
func WithTitleIDExternalIDs(query models.LocalMediaMatchQuery) models.LocalMediaMatchQuery {
	parts := strings.Split(strings.TrimSpace(query.TitleID), ":")
	if len(parts) < 2 {
		return query
	}
	last := parts[len(parts)-1]
	switch strings.ToLower(parts[0]) {
	case "tmdb":
		if query.TMDBID == "" {
			query.TMDBID = last
		}
	case "tvdb":
		if query.TVDBID == "" {
			query.TVDBID = last
		}
	case "imdb":
		if query.IMDBID == "" {
			query.IMDBID = last
		}
	}
	return query
}

// PickLibraryCopy returns the largest of the copies allow accepts (a smaller
// one is a sample or a lesser copy), nil when it accepts none. An error from
// allow ends the pick.
func PickLibraryCopy(copies []LibraryCopy, allow func(LibraryCopy) (bool, error)) (*LibraryCopy, error) {
	var best *LibraryCopy
	for i := range copies {
		c := copies[i]
		if best != nil && c.Item.SizeBytes <= best.Item.SizeBytes {
			continue
		}
		if allow != nil {
			ok, err := allow(c)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		best = &copies[i]
	}
	return best, nil
}
