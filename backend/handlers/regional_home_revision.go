package handlers

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"novastream/models"
)

// Home clients may skip shelf reloads when the manifest is unchanged. Regional
// rows need a bounded public-data refresh even when personal activity is quiet.
func regionalHomeRevision(shelves []models.ShelfConfig, now time.Time) string {
	for _, shelf := range shelves {
		if !shelf.Enabled {
			continue
		}
		query, _ := url.ParseQuery(shelf.TMDBDiscoverQuery)
		if strings.HasPrefix(shelf.ID, "regional-") || query.Get("country_chart") == "netflix" {
			return fmt.Sprintf("regional:%d", now.UTC().Unix()/(6*60*60))
		}
	}
	return ""
}
