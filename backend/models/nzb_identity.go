package models

import (
	"fmt"
	"strconv"
	"strings"
)

// NZBIdentity is the selected catalog item, never an identity parsed from a
// release name. Search results carry it through health, probe and resolve calls.
type NZBIdentity struct {
	CatalogID string `json:"catalogId"`
	MediaType string `json:"mediaType"`
	Season    int    `json:"season,omitempty"`
	Episode   int    `json:"episode,omitempty"`
	Ordering  string `json:"ordering,omitempty"`
}

func (i NZBIdentity) Key() (string, error) {
	id := strings.ToLower(strings.TrimSpace(i.CatalogID))
	parts := strings.Split(id, ":")
	if len(parts) != 3 || (parts[0] != "tmdb" && parts[0] != "tvdb") {
		return "", fmt.Errorf("NZB budget requires a TMDB or TVDB catalog identity")
	}
	n, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("invalid NZB catalog ID")
	}
	id = strings.Join(parts[:2], ":") + ":" + strconv.FormatInt(n, 10)
	switch i.MediaType {
	case "movie":
		if parts[1] == "movie" && i.Season == 0 && i.Episode == 0 {
			return id, nil
		}
	case "series":
		if ((parts[0] == "tmdb" && parts[1] == "tv") || (parts[0] == "tvdb" && parts[1] == "series")) && i.Season >= 0 && i.Episode > 0 {
			key := fmt.Sprintf("%s:s%de%d", id, i.Season, i.Episode)
			if order := strings.ToLower(strings.TrimSpace(i.Ordering)); order != "" && order != "official" {
				key += ":ordering:" + order
			}
			return key, nil
		}
	}
	return "", fmt.Errorf("NZB budget requires movie identity or series identity with season and episode")
}

// WithNZBIdentity clones attributes because cached search responses can be
// shared by concurrent requests for different selected items. Existing native
// clients already round-trip Attributes, including keys they do not recognize.
func (r NZBResult) WithNZBIdentity(identity NZBIdentity) NZBResult {
	attrs := make(map[string]string, len(r.Attributes)+5)
	for key, value := range r.Attributes {
		attrs[key] = value
	}
	attrs["nzbCatalogId"] = identity.CatalogID
	attrs["nzbMediaType"] = identity.MediaType
	attrs["nzbSeason"] = strconv.Itoa(identity.Season)
	attrs["nzbEpisode"] = strconv.Itoa(identity.Episode)
	attrs["nzbOrdering"] = identity.Ordering
	r.Attributes = attrs
	return r
}

func (r NZBResult) NZBIdentity() NZBIdentity {
	parse := func(value string) int {
		if value == "" {
			return 0
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return -1
		}
		return n
	}
	return NZBIdentity{CatalogID: r.Attributes["nzbCatalogId"], MediaType: r.Attributes["nzbMediaType"], Season: parse(r.Attributes["nzbSeason"]), Episode: parse(r.Attributes["nzbEpisode"]), Ordering: r.Attributes["nzbOrdering"]}
}
