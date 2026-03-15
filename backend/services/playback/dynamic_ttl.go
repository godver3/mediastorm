package playback

import (
	"time"
)

// DynamicTTL computes TTL based on distance from air date (V-shaped curve).
// Uses airDateTimeUTC (RFC3339) if available, falls back to airDate (YYYY-MM-DD),
// then year+mediaType, then a 30-minute default.
func DynamicTTL(airDate string, airDateTimeUTC string, year int, mediaType string) time.Duration {
	now := time.Now()

	// Try to parse precise air time first (RFC3339)
	if airDateTimeUTC != "" {
		if t, err := time.Parse(time.RFC3339, airDateTimeUTC); err == nil {
			return ttlFromDistance(now.Sub(t))
		}
	}

	// Fall back to date-only (assume noon UTC as midpoint)
	if airDate != "" {
		if t, err := time.Parse("2006-01-02", airDate); err == nil {
			midday := t.Add(12 * time.Hour)
			return ttlFromDistance(now.Sub(midday))
		}
	}

	// Fall back to year + media type
	if year > 0 {
		currentYear := now.Year()
		if mediaType == "movie" {
			if year >= currentYear {
				return 1 * time.Hour // Current year movie
			}
			return 6 * time.Hour // Older movie
		}
		// Series with year but no air date
		if year >= currentYear {
			return 1 * time.Hour
		}
		return 6 * time.Hour
	}

	// No date info at all
	return 30 * time.Minute
}

// ttlFromDistance returns TTL based on signed distance from air date.
// Negative = before air date, positive = after air date.
func ttlFromDistance(distance time.Duration) time.Duration {
	// Convert to hours for cleaner comparisons
	hours := distance.Hours()

	// Before air date (negative distance)
	if hours < -7*24 { // > 7 days before
		return 6 * time.Hour
	}
	if hours < -3*24 { // 3-7 days before
		return 2 * time.Hour
	}
	if hours < -24 { // 1-3 days before
		return 1 * time.Hour
	}
	if hours < -6 { // 6-24h before
		return 30 * time.Minute
	}
	if hours < -1 { // 1-6h before
		return 15 * time.Minute
	}

	// Peak volatility: ±1h of air time through 6h after
	if hours <= 6 { // -1h to +6h
		return 15 * time.Minute
	}

	// After air date (positive distance)
	if hours <= 24 { // 6-24h after
		return 30 * time.Minute
	}
	if hours <= 3*24 { // 1-3 days after
		return 45 * time.Minute
	}
	if hours <= 7*24 { // 3-7 days after
		return 1 * time.Hour
	}
	if hours <= 4*7*24 { // 1-4 weeks after
		return 2 * time.Hour
	}

	// > 4 weeks after
	return 6 * time.Hour
}

// DynamicTTL is a convenience method on PrequeueEntry that extracts fields
// and delegates to the package-level DynamicTTL function.
func (e *PrequeueEntry) DynamicTTL() time.Duration {
	var airDate, airDateTimeUTC string
	if e.TargetEpisode != nil {
		airDate = e.TargetEpisode.AirDate
		airDateTimeUTC = e.TargetEpisode.AirDateTimeUTC
	}
	return DynamicTTL(airDate, airDateTimeUTC, e.Year, e.MediaType)
}
