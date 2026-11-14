package sourceavailability

import (
	"errors"
	"strings"
	"time"

	"novastream/models"
)

const MaxVisibleTitles = 24
const deliveredTitleTTL = 30 * time.Minute
const maxDeliveredTitles = 2000

type TitleCoordinate struct {
	ID        string `json:"titleId"`
	MediaType string `json:"mediaType"`
}
type TitleHint struct {
	TitleCoordinate
	Availability *models.SourceAvailability `json:"sourceAvailability,omitempty"`
}
type TitleViewResponse struct {
	Enabled bool        `json:"enabled"`
	Version int64       `json:"version"`
	Items   []TitleHint `json:"items"`
}
type deliveredTitle struct {
	Title   models.Title
	Updated time.Time
}

func titleCoordinate(title models.Title) TitleCoordinate {
	media := title.MediaType
	if media == "tv" {
		media = "series"
	}
	return TitleCoordinate{ID: title.ID, MediaType: media}
}
func deliveredKey(profile, client string, coord TitleCoordinate) string {
	return strings.Join([]string{profile, client, coord.MediaType, coord.ID}, "\x00")
}

// rememberDelivered stores a bounded, immutable search projection, never artwork,
// stream paths or caller-supplied search text. Serving a list schedules no work.
func (s *Service) rememberDelivered(profile, client string, title models.Title) {
	if client == "" || title.Name == "" {
		return
	}
	projection := models.Title{ID: title.ID, Name: title.Name, OriginalName: title.OriginalName, MediaType: titleCoordinate(title).MediaType, Year: title.Year, Language: title.Language, IMDBID: title.IMDBID, TVDBID: title.TVDBID, TMDBID: title.TMDBID, Certification: title.Certification}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivered == nil {
		s.delivered = map[string]deliveredTitle{}
	}
	id := deliveredKey(profile, client, titleCoordinate(title))
	s.delivered[id] = deliveredTitle{projection, s.now()}
	if len(s.delivered) > maxDeliveredTitles {
		oldest := ""
		for k, entry := range s.delivered {
			if k != id && (oldest == "" || entry.Updated.Before(s.delivered[oldest].Updated)) {
				oldest = k
			}
		}
		delete(s.delivered, oldest)
	}
}

// ResolveDeliveredTitles reuses catalogue metadata delivered to this profile and
// client. Missing/expired entries stay unknown until the catalogue is refreshed.
func (s *Service) ResolveDeliveredTitles(profile, client string, coordinates []TitleCoordinate) []models.Title {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []models.Title{}
	for _, coord := range coordinates {
		id := deliveredKey(profile, client, coord)
		if entry, ok := s.delivered[id]; ok {
			if s.now().Sub(entry.Updated) > deliveredTitleTTL {
				delete(s.delivered, id)
				continue
			}
			result = append(result, entry.Title)
		}
	}
	return result
}

// ReplaceTitleView accepts server-resolved metadata after profile authorization.
// Title and episode views share the same lease/capacity/provider budget rules.
func (s *Service) ReplaceTitleView(profile, client, viewID string, version int64, titles []models.Title) (TitleViewResponse, error) {
	response := TitleViewResponse{Enabled: s.EpisodeHintsEnabled(), Version: version, Items: []TitleHint{}}
	if profile == "" || client == "" || viewID == "" || len(viewID) > 64 || version < 1 || len(titles) > MaxVisibleTitles {
		return response, errors.New("invalid visible title demand")
	}
	if !response.Enabled {
		return response, nil
	}
	var resolve func(string) (Selection, error)
	if len(titles) > 0 {
		var err error
		resolve, err = s.scope(profile, client)
		if err != nil {
			return response, err
		}
	}
	pending := []request{}
	desired := map[string]bool{}
	for _, title := range titles {
		coord := titleCoordinate(title)
		if title.ID == "" || title.Name == "" || coord.MediaType != "movie" && coord.MediaType != "series" {
			return response, errors.New("verified catalogue title required")
		}
		selected, err := resolve(title.ID)
		if err != nil {
			return response, err
		}
		value := s.availability(profile, client, title.ID, selected)
		response.Items = append(response.Items, TitleHint{coord, &value})
		item := request{ProfileID: profile, ClientID: client, Title: title, Scope: selected.Key, AudioLanguage: selected.AudioLanguage, LastSeen: s.now()}
		desired[requestID(item)] = true
		pending = append(pending, item)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.replaceViewLocked(strings.Join([]string{"title", profile, client, viewID}, "\x00"), version, desired); err != nil {
		return response, err
	}
	for _, item := range pending {
		s.enqueueLocked(item)
		id := deliveredKey(profile, client, titleCoordinate(item.Title))
		if entry, ok := s.delivered[id]; ok {
			entry.Updated = s.now()
			s.delivered[id] = entry
		}
	}
	return response, nil
}
