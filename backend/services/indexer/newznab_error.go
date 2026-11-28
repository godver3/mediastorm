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

// NewznabErrorTripsBreaker reports whether an <error> document describes a
// request/download limit that should back the indexer off, rather than a
// config error or one-off bad request. Newznab codes: 100-series are account
// errors and 200-series are parameter errors, so neither trips ("Incorrect
// parameter: limit" is code 201). 429 and 501 (download limit) trip. 500 is
// the spec's request limit but some indexers use it for plain server errors,
// so it trips only with an empty description or one that mentions a
// limit/quota. Other codes trip only on an explicit limit phrase.
func NewznabErrorTripsBreaker(body []byte) bool {
	var e newznabErrorResponse
	if err := xml.Unmarshal(body, &e); err != nil || !strings.EqualFold(e.XMLName.Local, "error") {
		return false
	}
	code := strings.TrimSpace(e.Code)
	desc := strings.ToLower(strings.TrimSpace(e.Description))
	switch code {
	case "429", "501":
		return true
	case "500":
		return desc == "" || containsAny(desc, "limit", "quota", "exceed", "too many", "hits")
	}
	if strings.HasPrefix(code, "1") || strings.HasPrefix(code, "2") {
		return false
	}
	return containsAny(desc, "request limit", "download limit", "api limit", "grab limit", "limit reached", "limit exceeded", "quota")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
