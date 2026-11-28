package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"novastream/models"
	"novastream/services/kids"
	metadatapkg "novastream/services/metadata"
)

// Kids-profile content limits are strict and enforced on the server:
//
//   - rating mode: listings drop titles above (or without) a certification
//     within the profile's caps, and opening such a title is refused.
//   - content_list mode: only titles on the profile's allowed MDBList lists
//     are visible. Discovery listings (trending, discover, external lists,
//     recommendations, search, calendar) are empty; an allowed list's own
//     shelf is shown unfiltered; opening any other title is refused.
//
// Requests that omit userId on a kids-only account have one injected by
// profileaccess.DefaultKidsProfile, so these checks also apply to them.

const kidsModeContentList = "content_list"

// kidsBlockAllRating is a rating cap no certification satisfies (an unknown
// cap is fail-closed in kids.IsRatingAllowed), used to hide everything.
const kidsBlockAllRating = "BLOCK-ALL"

// kidsAllowedListKey marks a request whose shelf is one of the curated
// profile's own allowed lists.
type kidsAllowedListKey struct{}

// kidsProfile returns userID's profile when it is a kids profile.
func (h *MetadataHandler) kidsProfile(userID string) (models.User, bool) {
	userID = strings.TrimSpace(userID)
	if userID == "" || h.UsersService == nil {
		return models.User{}, false
	}
	user, ok := h.UsersService.Get(userID)
	if !ok || !user.IsKidsProfile {
		return models.User{}, false
	}
	return user, true
}

func isCuratedKidsUser(user models.User) bool {
	return user.IsKidsProfile && user.KidsMode == kidsModeContentList
}

// kidsFilterActive reports whether listings for userID are kids-filtered.
func (h *MetadataHandler) kidsFilterActive(userID string) bool {
	if user, ok := h.kidsProfile(userID); ok && isCuratedKidsUser(user) {
		return true
	}
	_, _, ok := h.kidsRatingLimits(userID)
	return ok
}

// normalizeKidsListURL matches the form CustomList requests use.
func normalizeKidsListURL(listURL string) string {
	listURL = strings.TrimRight(strings.TrimSpace(listURL), "/")
	listURL = strings.TrimSuffix(listURL, "/json")
	return strings.ToLower(strings.TrimRight(listURL, "/"))
}

func kidsListAllowed(user models.User, listURL string) bool {
	target := normalizeKidsListURL(listURL)
	if target == "" {
		return false
	}
	for _, allowed := range user.KidsAllowedLists {
		if normalizeKidsListURL(allowed) == target {
			return true
		}
	}
	return false
}

// withKidsAllowedList marks the request when listURL is one of a curated kids
// profile's allowed lists, so the shelf is served unfiltered.
func (h *MetadataHandler) withKidsAllowedList(r *http.Request, userID, listURL string) *http.Request {
	if user, ok := h.kidsProfile(userID); ok && isCuratedKidsUser(user) && kidsListAllowed(user, listURL) {
		return r.WithContext(context.WithValue(r.Context(), kidsAllowedListKey{}, true))
	}
	return r
}

func isKidsAllowedListRequest(ctx context.Context) bool {
	allowed, _ := ctx.Value(kidsAllowedListKey{}).(bool)
	return allowed
}

// FilterTitlesForKids applies userID's kids-profile limits to titles.
func (h *MetadataHandler) FilterTitlesForKids(ctx context.Context, userID string, titles []models.Title) []models.Title {
	return h.filterTitlesByKids(ctx, userID, h.serviceForUser(userID), titles)
}

// KidsAllowsTitle reports whether profile userID may open title. Non-kids
// profiles may open anything.
func (h *MetadataHandler) KidsAllowsTitle(ctx context.Context, userID string, title models.Title) bool {
	user, ok := h.kidsProfile(userID)
	if !ok {
		return true
	}
	service := h.serviceForUser(userID)
	if isCuratedKidsUser(user) {
		return h.titleOnAllowedLists(ctx, service, user, title)
	}
	movieRating, tvRating, limited := h.kidsRatingLimits(userID)
	if !limited {
		return true
	}
	titles := []models.Title{title}
	if strings.TrimSpace(title.Certification) == "" && service != nil {
		service.EnrichTitleCertifications(ctx, titles)
	}
	return len(kids.FilterTitlesByRatings(titles, movieRating, tvRating)) == 1
}

func (h *MetadataHandler) titleOnAllowedLists(ctx context.Context, service metadataService, user models.User, title models.Title) bool {
	if service == nil {
		return false
	}
	for _, listURL := range user.KidsAllowedLists {
		listURL = strings.TrimRight(strings.TrimSpace(listURL), "/")
		if listURL == "" {
			continue
		}
		if !strings.HasSuffix(listURL, "/json") {
			listURL += "/json"
		}
		items, _, _, err := service.GetCustomList(ctx, listURL, metadatapkg.CustomListOptions{UserID: user.ID})
		if err != nil {
			continue
		}
		for _, item := range items {
			if sameKidsTitle(item.Title, title) {
				return true
			}
		}
	}
	return false
}

func kidsMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return "movie"
	case "":
		return ""
	default:
		return "series"
	}
}

// sameKidsTitle matches titles by any shared external ID or the canonical ID.
func sameKidsTitle(a, b models.Title) bool {
	if ma, mb := kidsMediaType(a.MediaType), kidsMediaType(b.MediaType); ma != "" && mb != "" && ma != mb {
		return false
	}
	switch {
	case a.ID != "" && a.ID == b.ID:
		return true
	case a.TMDBID > 0 && a.TMDBID == b.TMDBID:
		return true
	case a.TVDBID > 0 && a.TVDBID == b.TVDBID:
		return true
	case a.IMDBID != "" && strings.EqualFold(a.IMDBID, b.IMDBID):
		return true
	}
	return false
}

// writeKidsTitleBlocked writes the 403 returned when a kids profile opens a
// title outside its limits.
func writeKidsTitleBlocked(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "This title is not available on this kids profile",
		"code":  "kids_title_restricted",
	})
}

// kidsTitleFromQuery builds the lookup title for a details request.
func kidsTitleFromQuery(mediaType, titleID, imdbID string, tmdbID, tvdbID int64) models.Title {
	return models.Title{ID: strings.TrimSpace(titleID), MediaType: mediaType, IMDBID: strings.TrimSpace(imdbID), TMDBID: tmdbID, TVDBID: tvdbID}
}

func parseKidsInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed
}
