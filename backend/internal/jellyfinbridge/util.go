package jellyfinbridge

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

const ticksPerSecond = 10_000_000

// queryValue looks up a query parameter case-insensitively (ASP.NET model
// binding is case-insensitive and clients mix ParentId / parentId).
func queryValue(r *http.Request, name string) string {
	q := r.URL.Query()
	if v := q.Get(name); v != "" {
		return v
	}
	for key, values := range q {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

// queryList returns a comma-separated (or repeated) query parameter as a list.
func queryList(r *http.Request, name string) []string {
	var out []string
	for key, values := range r.URL.Query() {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, v := range values {
			for _, part := range strings.Split(v, ",") {
				if part = strings.TrimSpace(part); part != "" {
					out = append(out, part)
				}
			}
		}
	}
	return out
}

func queryBool(r *http.Request, name string) (bool, bool) {
	v := queryValue(r, name)
	if v == "" {
		return false, false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, false
	}
	return b, true
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }

func pathVar(r *http.Request, name string) string { return mux.Vars(r)[name] }

func intPtr(n int) *int { return &n }

func int64Ptr(n int64) *int64 { return &n }

func boolPtr(b bool) *bool { return &b }

func floatPtr(f float64) *float64 { return &f }

func timePtr(t time.Time) *time.Time { return &t }

func secondsToTicks(sec float64) int64 { return int64(sec * ticksPerSecond) }

func ticksToSeconds(ticks int64) float64 { return float64(ticks) / ticksPerSecond }

// imageTag is a short stable hash of an image URL, used as Jellyfin's ImageTag
// (clients use it as a cache key).
func imageTag(url string) string {
	if url == "" {
		return ""
	}
	// Invalidate images cached under the old mismatched DTO/source mapping.
	sum := sha1.Sum([]byte("artwork-v2\x00" + url))
	return hex.EncodeToString(sum[:8])
}

// parseDate parses mediastorm's YYYY-MM-DD (or RFC3339) dates.
func parseDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Clients send zoned and zone-less timestamps (Android TV's guide uses
	// LocalDateTime); zone-less values are taken as UTC.
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

// sortName strips leading articles the way Jellyfin does for SortName.
func sortName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, article) {
			return strings.TrimSpace(lower[len(article):])
		}
	}
	return lower
}

func jsonDecode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}
