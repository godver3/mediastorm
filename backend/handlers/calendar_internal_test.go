package handlers

import (
	"testing"
	"time"

	"novastream/models"
	"novastream/services/calendar"
)

func TestLimitCalendarForHomePreservesRecentItemsPerSource(t *testing.T) {
	loc := time.UTC
	now := time.Now().In(loc)

	var items []models.CalendarItem
	for i := 0; i < 80; i++ {
		items = append(items, models.CalendarItem{
			MediaType: "series",
			Source:    "trending",
			Title:     "Trending",
			AirDate:   now.AddDate(0, 0, -80+i).Format("2006-01-02"),
		})
	}
	for i := 0; i < 8; i++ {
		items = append(items, models.CalendarItem{
			MediaType: "series",
			Source:    "watchlist",
			Title:     "Watchlist",
			AirDate:   now.AddDate(0, 0, -80+i).Format("2006-01-02"),
		})
	}

	got := calendar.LimitForHomeShelf(items, loc, 5)

	watchlistCount := 0
	trendingCount := 0
	for _, item := range got {
		switch item.Source {
		case "watchlist":
			watchlistCount++
		case "trending":
			trendingCount++
		}
	}

	if watchlistCount != 5 {
		t.Fatalf("expected 5 recent watchlist items to survive source limiting, got %d", watchlistCount)
	}
	if trendingCount != 5 {
		t.Fatalf("expected 5 recent trending items to survive source limiting, got %d", trendingCount)
	}
}
