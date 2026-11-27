package handlers

import (
	"novastream/models"
	"novastream/services/kids"
)

// kidsUserLookup is satisfied by the users service; calendar handlers use it
// to apply kids-profile rating limits.
type kidsUserLookup interface {
	Get(id string) (models.User, bool)
}

// kidsRatingLimits returns the movie/TV max ratings for a rating-mode kids
// profile, or ok=false when no rating filter applies.
func kidsRatingLimits(users kidsUserLookup, userID string) (movie, tv string, ok bool) {
	if users == nil {
		return "", "", false
	}
	user, found := users.Get(userID)
	if !found || !user.IsKidsProfile || user.KidsMode != "rating" {
		return "", "", false
	}
	movie, tv = user.KidsMaxMovieRating, user.KidsMaxTVRating
	if movie == "" && tv == "" && user.KidsMaxRating != "" {
		movie, tv = user.KidsMaxRating, user.KidsMaxRating
	}
	return movie, tv, true
}

// filterKidsCalendarItems drops calendar items above a kids profile's rating
// limits. Watch-party playback can put mature series into a kids profile's
// history; that must not surface them in Coming Up / Recently Aired.
func filterKidsCalendarItems(users kidsUserLookup, userID string, items []models.CalendarItem) []models.CalendarItem {
	movie, tv, ok := kidsRatingLimits(users, userID)
	if !ok || len(items) == 0 {
		return items
	}
	return kids.FilterCalendarByRatings(items, movie, tv)
}
