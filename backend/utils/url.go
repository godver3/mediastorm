package utils

import (
	"fmt"
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

	// Build URL with properly encoded path and query, preserving userinfo if present
	encoded := parsedURL.Scheme + "://"
	if parsedURL.User != nil {
		encoded += parsedURL.User.String() + "@"
	}
	encoded += parsedURL.Host + parsedURL.EscapedPath()
	if parsedURL.RawQuery != "" {
		// Encode spaces in query string as %20
		encodedQuery := strings.ReplaceAll(parsedURL.RawQuery, " ", "%20")
		encoded += "?" + encodedQuery
	}
	return encoded, nil
}

// ValidateMediaURL ensures a media URL uses only safe protocols.
// Accepts empty strings, "pipe:0", and http/https URLs.
// Rejects file://, gopher://, ftp://, and other potentially dangerous schemes.
func ValidateMediaURL(rawURL string) error {
	if rawURL == "" || rawURL == "pipe:0" {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("disallowed URL scheme %q: only http and https are permitted", scheme)
	}
}
