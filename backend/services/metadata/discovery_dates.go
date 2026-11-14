package metadata

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var relativeDiscoveryDate = regexp.MustCompile(`^today(?:([+-])([0-9]{1,4})d)?$`)

// Calendar-day arithmetic respects the preset's local date across DST changes.
func resolveRelativeDiscoveryDates(values url.Values, now time.Time) (url.Values, error) {
	location := time.UTC
	if zone := values.Get("date_timezone"); zone != "" {
		var err error
		location, err = time.LoadLocation(zone)
		if err != nil {
			return nil, fmt.Errorf("invalid discovery date timezone")
		}
	}
	values.Del("date_timezone")
	for _, key := range []string{"date.gte", "date.lte", "release_date.gte", "release_date.lte", "primary_release_date.gte", "primary_release_date.lte", "first_air_date.gte", "first_air_date.lte", "air_date.gte", "air_date.lte"} {
		raw := values.Get(key)
		if !strings.HasPrefix(raw, "today") {
			continue
		}
		match := relativeDiscoveryDate.FindStringSubmatch(raw)
		if match == nil {
			return nil, fmt.Errorf("invalid relative discovery date")
		}
		days := 0
		if match[2] != "" {
			days, _ = strconv.Atoi(match[2])
			if days > 3660 {
				return nil, fmt.Errorf("relative discovery date exceeds ten years")
			}
			if match[1] == "-" {
				days = -days
			}
		}
		values.Set(key, now.In(location).AddDate(0, 0, days).Format("2006-01-02"))
	}
	return values, nil
}

func IsCountryChartQuery(raw string) bool {
	values, err := url.ParseQuery(raw)
	return err == nil && values.Get("country_chart") != ""
}

func regionalDiscoveryMaxAge(filters url.Values) time.Duration {
	if filters.Get("watch_region") != "" || filters.Get("region") != "" {
		return 6 * time.Hour
	}
	return 0
}
