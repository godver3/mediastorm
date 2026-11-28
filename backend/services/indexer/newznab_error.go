package indexer

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// newznabErrorResponse models the error element that newznab/torznab indexers
// return — with an HTTP 200 status — when a request cannot be served (bad API
// key, daily request limit reached, missing parameter, etc). XMLName captures
// the root element name so an error document can be told apart from a normal
// <rss> feed without xml.Unmarshal failing on the root mismatch.
type newznabErrorResponse struct {
	XMLName     xml.Name
	Code        string `xml:"code,attr"`
	Description string `xml:"description,attr"`
}

// ParseNewznabError inspects an indexer response body for a newznab/torznab
// <error> element and returns a readable message when one is present.
func ParseNewznabError(body []byte) (string, bool) {
	var e newznabErrorResponse
	if err := xml.Unmarshal(body, &e); err != nil {
		return "", false
	}
	if !strings.EqualFold(e.XMLName.Local, "error") {
		return "", false
	}
	desc := strings.TrimSpace(e.Description)
	code := strings.TrimSpace(e.Code)
	switch {
	case desc != "" && code != "":
		return fmt.Sprintf("API error %s: %s", code, desc), true
	case desc != "":
		return desc, true
	case code != "":
		return fmt.Sprintf("API error %s", code), true
	default:
		return "indexer returned an API error", true
	}
}

// newznabErrorTripsBreaker reports whether an <error> document describes a
// failure that should back the indexer off (request limit
// reached) rather than a config error or one-off bad request.
func newznabErrorTripsBreaker(body []byte) bool {
	var e newznabErrorResponse
	if err := xml.Unmarshal(body, &e); err != nil || !strings.EqualFold(e.XMLName.Local, "error") {
		return false
	}
	switch strings.TrimSpace(e.Code) {
	case "429", "500":
		return true
	}
	return strings.Contains(strings.ToLower(e.Description), "limit")
}
