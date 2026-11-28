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

// Error returns err with any API key removed from the messages it carries.
// The error chain is kept where possible: a url.Error is copied with its URL
// redacted, and wrapping errors keep their own message (redacted) around the
// cleaned inner error, so errors.Is/As still work.
func Error(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := String(msg)
	if redacted == msg {
		return err
	}
	if ue, ok := err.(*url.Error); ok {
		cp := *ue
		cp.URL = String(ue.URL)
		cp.Err = Error(ue.Err)
		return &cp
	}
	if inner := errors.Unwrap(err); inner != nil {
		return &redactedError{msg: redacted, inner: Error(inner)}
	}
	return errors.New(redacted)
}

type redactedError struct {
	msg   string
	inner error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.inner }
