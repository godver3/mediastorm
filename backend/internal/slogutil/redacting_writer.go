package slogutil

import (
	"io"
	"regexp"
)

var sensitiveLogPatterns = []struct {
	pattern     *regexp.Regexp
	replacement []byte
}{
	{
		pattern:     regexp.MustCompile(`(?i)("x-account-password"\s*:\s*)(?:"(?:[^"\\]|\\.)*"|\[\s*(?:"(?:[^"\\]|\\.)*"\s*(?:,\s*"(?:[^"\\]|\\.)*"\s*)*)?\])`),
		replacement: []byte(`${1}"[redacted]"`),
	},
	{
		pattern:     regexp.MustCompile(`(?im)(^[^\r\n]*?\bx-account-password\s*[:=]\s*)[^\r\n]*`),
		replacement: []byte(`${1}[redacted]`),
	},
	{
		pattern:     regexp.MustCompile(`(?i)([?&](?:token|api[_-]?key|key|auth|authorization|x-pin)=)[^&\s"'<>]*`),
		replacement: []byte(`${1}[redacted]`),
	},
	{
		pattern:     regexp.MustCompile(`(?i)((?:%3f|%26)(?:token|api[_-]?key|key|auth|authorization|x-pin)%3d)[^&\s"'<>]*`),
		replacement: []byte(`${1}[redacted]`),
	},
	{
		pattern:     regexp.MustCompile(`(?i)("(?:token|api[_-]?key|key|auth|authorization|x-pin|x-account-password|xtreamUsername|homeWifiSSID|currentSSID|ssid)"\s*:\s*")[^"]*`),
		replacement: []byte(`${1}[redacted]`),
	},
	{
		pattern:     regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[a-z0-9._~+/=-]+`),
		replacement: []byte(`${1}[redacted]`),
	},
	{
		pattern:     regexp.MustCompile(`(?i)("path"\s*:\s*"[^"]*homeWifiSSID"[^}\r\n]*"before"\s*:\s*")[^"]*`),
		replacement: []byte(`${1}[redacted]`),
	},
	{
		pattern:     regexp.MustCompile(`(?i)("path"\s*:\s*"[^"]*homeWifiSSID"[^}\r\n]*"after"\s*:\s*")[^"]*`),
		replacement: []byte(`${1}[redacted]`),
	},
}

type redactingWriter struct {
	destination io.Writer
}

// NewRedactingWriter removes common credential forms from complete log writes
// before forwarding them to the configured console/file destinations.
func NewRedactingWriter(destination io.Writer) io.Writer {
	return &redactingWriter{destination: destination}
}

func (w *redactingWriter) Write(data []byte) (int, error) {
	redacted := append([]byte(nil), data...)
	for _, entry := range sensitiveLogPatterns {
		redacted = entry.pattern.ReplaceAll(redacted, entry.replacement)
	}
	_, err := w.destination.Write(redacted)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}
