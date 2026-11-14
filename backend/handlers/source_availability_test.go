package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"novastream/models"
)

type availabilityPresentationFake struct{ profile, client string }

func (f *availabilityPresentationFake) AnnotateTitles(profile, client string, titles []models.Title) []models.Title {
	f.profile, f.client = profile, client
	copy := append([]models.Title(nil), titles...)
	for i := range copy {
		copy[i].SourceAvailability = &models.SourceAvailability{State: "not_found", Complete: true}
		copy[i].CardSubtitle = "No matching source in checked indexers"
		copy[i].ForceTitleOverlay = true
	}
	return copy
}
func (*availabilityPresentationFake) Revision(string, string) string { return "revision" }
func (*availabilityPresentationFake) ObservePlaybackStart(string, string, string, string) func(bool) {
	return func(bool) {}
}

func TestAvailabilityResponseClonesCachedRecommendations(t *testing.T) {
	service := &availabilityPresentationFake{}
	handler := &MetadataHandler{SourceAvailability: service}
	response := PersonalizedRecommendationsResponse{Items: []models.TrendingItem{{Title: models.Title{ID: "a", MediaType: "movie"}}}}
	r := httptest.NewRequest("GET", "/api/recommendations?userId=stefan&clientId=firetv", nil)
	w := httptest.NewRecorder()
	if err := handler.encodeMetadata(w, r, &response); err != nil {
		t.Fatal(err)
	}
	var got PersonalizedRecommendationsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if service.profile != "stefan" || service.client != "firetv" || got.Items[0].Title.SourceAvailability == nil {
		t.Fatal("profile/device status missing")
	}
	if response.Items[0].Title.SourceAvailability != nil || response.Items[0].Title.CardSubtitle != "" {
		t.Fatal("profile state entered shared recommendation cache")
	}
}

func TestAvailabilityWatchlistConversionRetainsStatusAndOverlay(t *testing.T) {
	items := availabilityWatchlist(&availabilityPresentationFake{}, "stefan", "tv", []models.WatchlistItem{{ID: "a", MediaType: "movie", Name: "Example"}})
	got := watchlistItemsToTrending(items)[0].Title
	if got.SourceAvailability == nil || got.SourceAvailability.State != "not_found" || !got.ForceTitleOverlay || got.CardSubtitle == "" {
		t.Fatal("watchlist shelf dropped source status")
	}
}
