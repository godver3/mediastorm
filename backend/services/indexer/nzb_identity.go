package indexer

import (
	"fmt"
	"strings"

	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/debrid"
)

// Capture the selected catalog episode before localized, daily, mapped or
// season-pack fallback queries replace Query. Never parse a result's title.
func withNZBIdentity(opts SearchOptions) SearchOptions {
	if opts.acquisitionIdentity.CatalogID != "" {
		return opts
	}
	id := mediaidentity.CanonicalTitleID(opts.MediaType, opts.TitleID, nil)
	if (id == "" || strings.HasPrefix(id, "imdb:") || strings.HasPrefix(id, "tt")) && opts.TVDBID > 0 {
		kind := "movie"
		if opts.MediaType == "series" {
			kind = "series"
		}
		id = fmt.Sprintf("tvdb:%s:%d", kind, opts.TVDBID)
	}
	identity := models.NZBIdentity{CatalogID: id, MediaType: opts.MediaType}
	if opts.MediaType == "series" {
		parsed := debrid.ParseQuery(opts.Query)
		identity.Season, identity.Episode = parsed.Season, parsed.Episode
		if opts.Numbering != nil {
			if opts.Numbering.SeriesID != "" {
				identity.CatalogID = mediaidentity.CanonicalSeriesID(opts.Numbering.SeriesID, nil)
			}
			identity.Ordering = opts.Numbering.Ordering
		}
	}
	opts.acquisitionIdentity = identity
	return opts
}
