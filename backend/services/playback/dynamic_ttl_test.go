package playback

import (
	"testing"
	"time"

	"novastream/models"
)

func TestDynamicTTL_VCurve(t *testing.T) {
	// Use a fixed "now" by computing air dates relative to the current time
	now := time.Now()

	tests := []struct {
		name        string
		airDateUTC  string // RFC3339
		airDate     string // YYYY-MM-DD
		year        int
		mediaType   string
		expectedTTL time.Duration
	}{
		// V-curve with precise air time (RFC3339)
		{
			name:        "8 days before air date",
			airDateUTC:  now.Add(8 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 6 * time.Hour,
		},
		{
			name:        "5 days before air date",
			airDateUTC:  now.Add(5 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 2 * time.Hour,
		},
		{
			name:        "2 days before air date",
			airDateUTC:  now.Add(2 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 1 * time.Hour,
		},
		{
			name:        "12 hours before air date",
			airDateUTC:  now.Add(12 * time.Hour).Format(time.RFC3339),
			expectedTTL: 30 * time.Minute,
		},
		{
			name:        "3 hours before air date",
			airDateUTC:  now.Add(3 * time.Hour).Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "30 minutes before air date",
			airDateUTC:  now.Add(30 * time.Minute).Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "exactly at air time",
			airDateUTC:  now.Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "30 minutes after air date",
			airDateUTC:  now.Add(-30 * time.Minute).Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "3 hours after air date",
			airDateUTC:  now.Add(-3 * time.Hour).Format(time.RFC3339),
			expectedTTL: 15 * time.Minute,
		},
		{
			name:        "12 hours after air date",
			airDateUTC:  now.Add(-12 * time.Hour).Format(time.RFC3339),
			expectedTTL: 30 * time.Minute,
		},
		{
			name:        "2 days after air date",
			airDateUTC:  now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 45 * time.Minute,
		},
		{
			name:        "5 days after air date",
			airDateUTC:  now.Add(-5 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 1 * time.Hour,
		},
		{
			name:        "2 weeks after air date",
			airDateUTC:  now.Add(-14 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 2 * time.Hour,
		},
		{
			name:        "5 weeks after air date",
			airDateUTC:  now.Add(-35 * 24 * time.Hour).Format(time.RFC3339),
			expectedTTL: 6 * time.Hour,
		},

		// Movie cases
		{
			name:        "movie current year",
			year:        now.Year(),
			mediaType:   "movie",
			expectedTTL: 1 * time.Hour,
		},
		{
			name:        "movie older year",
			year:        2020,
			mediaType:   "movie",
			expectedTTL: 6 * time.Hour,
		},

		// No date info
		{
			name:        "no date info",
			expectedTTL: 30 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DynamicTTL(tt.airDate, tt.airDateUTC, tt.year, tt.mediaType)
			if got != tt.expectedTTL {
				t.Errorf("DynamicTTL() = %v, want %v", got, tt.expectedTTL)
			}
		})
	}
}

func TestDynamicTTL_AirDateTimeUTCPreferredOverAirDate(t *testing.T) {
	now := time.Now()

	// airDateTimeUTC says 3 hours from now (should be 15min TTL)
	// airDate says 5 days ago (should be 1h TTL)
	airDateUTC := now.Add(3 * time.Hour).Format(time.RFC3339)
	airDate := now.Add(-5 * 24 * time.Hour).Format("2006-01-02")

	got := DynamicTTL(airDate, airDateUTC, 0, "series")
	if got != 15*time.Minute {
		t.Errorf("expected 15m (from airDateTimeUTC), got %v", got)
	}
}

func TestDynamicTTL_AirDateFallback(t *testing.T) {
	now := time.Now()

	// Only airDate set, 2 days ago (noon UTC assumption → ~2 days after)
	airDate := now.Add(-2 * 24 * time.Hour).Format("2006-01-02")

	got := DynamicTTL(airDate, "", 0, "series")
	if got != 45*time.Minute {
		t.Errorf("expected 45m for 2 days after air date, got %v", got)
	}
}

func TestDynamicTTL_BoundaryExactly7DaysBefore(t *testing.T) {
	now := time.Now()

	// Exactly 7 days before → distance is -7*24h → hours < -3*24, so 2h
	airDateUTC := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	got := DynamicTTL("", airDateUTC, 0, "series")
	if got != 2*time.Hour {
		t.Errorf("expected 2h at exactly 7 days before, got %v", got)
	}
}

func TestDynamicTTL_BoundaryJustUnder4WeeksAfter(t *testing.T) {
	now := time.Now()

	// Just under 4 weeks after → should be in 2h tier
	airDateUTC := now.Add(-27 * 24 * time.Hour).Format(time.RFC3339)
	got := DynamicTTL("", airDateUTC, 0, "series")
	if got != 2*time.Hour {
		t.Errorf("expected 2h just under 4 weeks after, got %v", got)
	}
}

func TestDynamicTTL_BoundaryOver4WeeksAfter(t *testing.T) {
	now := time.Now()

	// Over 4 weeks after → should be 6h tier
	airDateUTC := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	got := DynamicTTL("", airDateUTC, 0, "series")
	if got != 6*time.Hour {
		t.Errorf("expected 6h over 4 weeks after, got %v", got)
	}
}

func TestPrequeueEntry_DynamicTTL(t *testing.T) {
	now := time.Now()

	entry := &PrequeueEntry{
		MediaType: "series",
		Year:      2024,
		TargetEpisode: &models.EpisodeReference{
			AirDateTimeUTC: now.Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}

	got := entry.DynamicTTL()
	if got != 15*time.Minute {
		t.Errorf("expected 15m for 2h after air, got %v", got)
	}
}

func TestPrequeueEntry_DynamicTTL_NilEpisode(t *testing.T) {
	entry := &PrequeueEntry{
		MediaType: "movie",
		Year:      2020,
	}

	got := entry.DynamicTTL()
	if got != 6*time.Hour {
		t.Errorf("expected 6h for older movie, got %v", got)
	}
}
