package utils

import (
	"net/url"
	"strings"
)

// EncodeURLWithSpaces properly encodes a URL that may contain unencoded spaces.
// Some external services provide URLs with raw spaces which need to be %20 encoded for HTTP.
func EncodeURLWithSpaces(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	// Build URL with properly encoded path and query
	encoded := parsedURL.Scheme + "://" + parsedURL.Host + parsedURL.EscapedPath()
	if parsedURL.RawQuery != "" {
		// Encode spaces in query string as %20
		encodedQuery := strings.ReplaceAll(parsedURL.RawQuery, " ", "%20")
		encoded += "?" + encodedQuery
	}
	return encoded, nil
}
