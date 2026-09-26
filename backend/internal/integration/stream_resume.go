package integration

import (
	"net/http"
	"time"
)

// We advertise Last-Modified for NZB files. A different or unsupported validator
// must yield the full file, so clients cannot append bytes from a changed file.
func matchesStreamIfRange(validator string, modified time.Time) bool {
	if validator == "" {
		return true
	}
	date, err := http.ParseTime(validator)
	return err == nil && date.Unix() == modified.Unix()
}
