// Package urlredact strips API keys passed as URL query parameters from
// strings and errors so they never reach logs or API responses.
package urlredact

import (
	"errors"
	"net/url"
	"regexp"
)

var keyParamRe = regexp.MustCompile(`(?i)([?&](?:api_?key|key|token|access_token)=)[^&\s"'<>)]*`)

// String replaces the value of api_key/apikey/key/token query parameters.
func String(s string) string {
	return keyParamRe.ReplaceAllString(s, "${1}REDACTED")
}

// Error returns err with any API key removed from the request URL it carries.
// The original error is kept in the chain where possible (url.Error is copied
// with its URL redacted); other errors are rewritten only if they leak a key.
func Error(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		cp := *ue
		cp.URL = String(ue.URL)
		cp.Err = Error(ue.Err)
		return &cp
	}
	if msg := err.Error(); String(msg) != msg {
		return errors.New(String(msg))
	}
	return err
}
