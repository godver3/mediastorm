package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"novastream/models"
)

type sourceAvailabilityService interface {
	AnnotateTitles(profileID, clientID string, titles []models.Title) []models.Title
	Revision(profileID, clientID string) string
	ObservePlaybackStart(profileID, clientID, titleID, mediaType string) func(bool)
}

func availabilityTrending(service sourceAvailabilityService, profileID, clientID string, items []models.TrendingItem) []models.TrendingItem {
	if service == nil || len(items) == 0 {
		return items
	}
	titles := make([]models.Title, len(items))
	for i := range items {
		titles[i] = items[i].Title
	}
	titles = service.AnnotateTitles(profileID, clientID, titles)
	out := append([]models.TrendingItem(nil), items...)
	for i := range out {
		out[i].Title = titles[i]
	}
	return out
}

func availabilityWatchlist(service sourceAvailabilityService, profileID, clientID string, items []models.WatchlistItem) []models.WatchlistItem {
	if service == nil || len(items) == 0 {
		return items
	}
	titles := make([]models.Title, len(items))
	for i, item := range items {
		titles[i] = models.Title{ID: item.ID, Name: item.Name, MediaType: item.MediaType, Year: item.Year}
	}
	titles = service.AnnotateTitles(profileID, clientID, titles)
	out := append([]models.WatchlistItem(nil), items...)
	for i := range out {
		out[i].SourceAvailability = titles[i].SourceAvailability
		out[i].CardSubtitle = titles[i].CardSubtitle
		out[i].ForceTitleOverlay = titles[i].ForceTitleOverlay
	}
	return out
}

// Only presentation copies are annotated; shared metadata/recommendation cache
// entries must never acquire another profile's source status or queue context.
func (h *MetadataHandler) encodeMetadata(w http.ResponseWriter, r *http.Request, payload any) error {
	profile := strings.TrimSpace(r.URL.Query().Get("userId"))
	if profile == "" {
		profile = mux.Vars(r)["userID"]
	}
	client := requestClientID(r)
	annotate := func(items []models.TrendingItem) []models.TrendingItem {
		return availabilityTrending(h.SourceAvailability, profile, client, items)
	}
	if h.SourceAvailability != nil && profile != "" {
		switch value := payload.(type) {
		case []models.TrendingItem:
			payload = annotate(value)
		case []models.SearchResult:
			copy := append([]models.SearchResult(nil), value...)
			titles := make([]models.Title, len(value))
			for i := range value {
				titles[i] = value[i].Title
			}
			titles = h.SourceAvailability.AnnotateTitles(profile, client, titles)
			for i := range copy {
				copy[i].Title = titles[i]
			}
			if value != nil && copy == nil {
				copy = []models.SearchResult{}
			}
			payload = copy
		case DiscoverNewResponse:
			value.Items = annotate(value.Items)
			payload = value
		case CustomListResponse:
			value.Items = annotate(value.Items)
			payload = value
		case *CustomListResponse:
			if value != nil {
				copy := *value
				copy.Items = annotate(copy.Items)
				payload = &copy
			}
		case TraktShelfResponse:
			value.Items = annotate(value.Items)
			payload = value
		case TopTenResponse:
			value.Items = annotate(value.Items)
			payload = value
		case PopularOnServerResponse:
			value.Items = annotate(value.Items)
			payload = value
		case RecentlyWatchedResponse:
			value.Items = annotate(value.Items)
			payload = value
		case PersonalizedRecommendationsResponse:
			value.Items = annotate(value.Items)
			value.Movies = annotate(value.Movies)
			value.Series = annotate(value.Series)
			payload = value
		case *PersonalizedRecommendationsResponse:
			if value != nil {
				copy := *value
				copy.Items = annotate(copy.Items)
				copy.Movies = annotate(copy.Movies)
				copy.Series = annotate(copy.Series)
				payload = &copy
			}
		case map[string]interface{}:
			if items, ok := value["items"].([]models.TrendingItem); ok {
				copy := make(map[string]interface{}, len(value))
				for k, v := range value {
					copy[k] = v
				}
				copy["items"] = annotate(items)
				payload = copy
			}
		}
	}
	return json.NewEncoder(w).Encode(payload)
}

func (h *MetadataHandler) SetSourceAvailability(service sourceAvailabilityService) {
	h.SourceAvailability = service
}
func (h *StartupHandler) SetSourceAvailability(service sourceAvailabilityService) {
	h.sourceAvailability = service
}
func (h *PrequeueHandler) SetSourceAvailability(service sourceAvailabilityService) {
	h.sourceAvailability = service
}
