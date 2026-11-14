package metadata

import (
	"net/url"
	"testing"
	"time"
)

func TestRegionalDiscoveryDatesFollowLocalCalendarAndDST(t *testing.T) {
	now := time.Date(2026, 3, 29, 22, 30, 0, 0, time.UTC)
	values := url.Values{"date_timezone": {"Europe/Berlin"}, "release_date.gte": {"today-1d"}, "release_date.lte": {"today+30d"}}
	resolved, err := resolveRelativeDiscoveryDates(values, now)
	if err != nil || resolved.Get("release_date.gte") != "2026-03-29" || resolved.Get("release_date.lte") != "2026-04-29" || resolved.Has("date_timezone") {
		t.Fatalf("wrong local dates: %v %v", resolved, err)
	}
	for _, raw := range []string{"today-9999d", "today-broken"} {
		if _, err := resolveRelativeDiscoveryDates(url.Values{"date.gte": {raw}}, now); err == nil {
			t.Fatal("invalid expression accepted")
		}
	}
	if _, err := resolveRelativeDiscoveryDates(url.Values{"date_timezone": {"Invalid/Zone"}}, now); err == nil {
		t.Fatal("unknown timezone accepted")
	}
}
