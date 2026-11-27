package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

type presetListResponse struct {
	Disabled             bool                      `json:"disabled"`
	Presets              []qualityprofiles.Summary `json:"presets"`
	MoviePreset          string                    `json:"moviePreset"`
	SeriesPreset         string                    `json:"seriesPreset"`
	AllowRequestOverride bool                      `json:"allowRequestOverride"`
	PickerPresets        []string                  `json:"pickerPresets"`
	HiddenByDefault      []string                  `json:"hiddenByDefault"`
	AnimeMoviePreset     string                    `json:"animeMoviePreset"`
	AnimeSeriesPreset    string                    `json:"animeSeriesPreset"`
	AnimeOnlyInPickers   bool                      `json:"animeOnlyInPickers"`
	IgnoreLibraryCopies  bool                      `json:"ignoreLibraryCopies"`
	Sources              []qualityprofiles.Source  `json:"sources"`
	Assignments          map[string]presetAssign   `json:"assignments,omitempty"`
}

type presetAssign struct {
	MoviePreset       *string `json:"moviePreset"`
	SeriesPreset      *string `json:"seriesPreset"`
	AnimeMoviePreset  *string `json:"animeMoviePreset"`
	AnimeSeriesPreset *string `json:"animeSeriesPreset"`
	// PickerPresets is the profile's own list of picker presets (nil: all
	// the server offers).
	PickerPresets *[]string `json:"pickerPresets"`
	// HiddenPresets are offered presets the profile hides from its own
	// pickers (nil: none).
	HiddenPresets *[]string `json:"hiddenPresets"`
	// ShownPresets are presets hidden by default that the profile shows
	// (nil: none).
	ShownPresets *[]string `json:"shownPresets"`
}

// List returns every preset and the current selection.
func (h *QualityPresetsHandler) List(w http.ResponseWriter, r *http.Request) {
	presets, err := h.Service.List(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	master := isMasterRequest(r)
	resp := presetListResponse{Presets: []qualityprofiles.Summary{}, Sources: []qualityprofiles.Source{}}
	if master {
		resp.Sources = h.Service.Sources()
	}
	for _, p := range presets {
		sum := p.Summary()
		if !master && sum.LastError != "" {
			sum.LastError = "the last update check failed"
		}
		if a, err := h.Service.Active(r.Context(), p.ID); err != nil {
			sum.FormatErrors = 1
		} else {
			sum.FormatErrors = len(a.Errors)
		}
		resp.Presets = append(resp.Presets, sum)
	}
	if h.Config != nil {
		if s, err := h.Config.Load(); err == nil {
			resp.Disabled = s.QualityPresets.Disabled
			resp.MoviePreset = s.QualityPresets.MoviePreset
			resp.SeriesPreset = s.QualityPresets.SeriesPreset
			resp.AllowRequestOverride = s.QualityPresets.AllowRequestOverride
			resp.PickerPresets = append([]string{}, s.QualityPresets.PickerPresets...)
			resp.HiddenByDefault = append([]string{}, s.QualityPresets.HiddenByDefault...)
			resp.AnimeMoviePreset = s.QualityPresets.AnimeMoviePreset
			resp.AnimeSeriesPreset = s.QualityPresets.AnimeSeriesPreset
			resp.AnimeOnlyInPickers = s.QualityPresets.AnimeOnlyInPickers
			resp.IgnoreLibraryCopies = s.QualityPresets.IgnoreLibraryCopies
		}
	}
	if users := r.URL.Query()["userId"]; len(users) > 0 && h.UserSettings != nil {
		resp.Assignments = map[string]presetAssign{}
		for _, uid := range users {
			if !h.canAccessProfile(r, uid) {
				continue
			}
			if us, err := h.UserSettings.Get(uid); err == nil && us != nil && us.QualityPresets != nil {
				q := us.QualityPresets
				resp.Assignments[uid] = presetAssign{MoviePreset: q.MoviePreset, SeriesPreset: q.SeriesPreset, AnimeMoviePreset: q.AnimeMoviePreset, AnimeSeriesPreset: q.AnimeSeriesPreset, PickerPresets: q.PickerPresets, HiddenPresets: q.HiddenPresets, ShownPresets: q.ShownPresets}
			} else {
				resp.Assignments[uid] = presetAssign{}
			}
		}
	}
	presetJSON(w, http.StatusOK, resp)
}

type clientPreset struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	App       arr.App `json:"app"`
	MediaType string  `json:"mediaType"`
	// Audience is "" (every title), "anime" or "non-anime".
	Audience qualityprofiles.Audience `json:"audience,omitempty"`
}

type clientPresetSelection struct {
	// PresetID is "" when the simple ranking applies.
	PresetID string `json:"presetId"`
	Name     string `json:"name"`
	Source   string `json:"source"`
}

type clientPresetList struct {
	// Disabled: presets are switched off server-wide; nothing is offered.
	Disabled             bool           `json:"disabled,omitempty"`
	AllowRequestOverride bool           `json:"allowRequestOverride"`
	Presets              []clientPreset `json:"presets"`
	// Offered are the choices for version pickers, in the admin's order.
	// "simple" (the simple ranking) has no media type and fits both.
	Offered []clientPreset        `json:"offered"`
	Movie   clientPresetSelection `json:"movie"`
	Series  clientPresetSelection `json:"series"`
	// MovieDefault and SeriesDefault are what Movie and Series would be
	// without the preset remembered for the title: picking that preset
	// clears the memory. They equal Movie and Series when nothing is
	// remembered.
	MovieDefault  clientPresetSelection `json:"movieDefault"`
	SeriesDefault clientPresetSelection `json:"seriesDefault"`
	// Anime is set when the request named a title: whether it is anime,
	// and why. Offered and the selection then follow it.
	Anime       *bool  `json:"anime,omitempty"`
	AnimeReason string `json:"animeReason,omitempty"`
	// PreferLibraryCopies: titles a local library has play from there (the
	// "Library copy" choice) unless another preset is picked for them.
	PreferLibraryCopies bool `json:"preferLibraryCopies"`
}

// ClientList is the app-facing list (GET /api/quality-presets?userId=&clientId=):
// the presets a search can use, whether apps may pick one per search
// (the qualityPreset search parameter), and the preset each media type
// uses for the profile and device right now.
func (h *QualityPresetsHandler) ClientList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	userID := strings.TrimSpace(q.Get("userId"))
	clientID := strings.TrimSpace(q.Get("clientId"))
	if userID != "" && !h.canAccessProfile(r, userID) {
		writeJSONError(w, "profile not found", http.StatusNotFound)
		return
	}
	// A registered device only counts for a profile that uses it, so another
	// profile's device choices are not revealed. A device mediastorm does not
	// know (a Jellyfin client: jellyfin-<device>) has no choices of anyone
	// else and still picks the profile's preset remembered for it.
	if clientID != "" && (userID == "" || h.deviceOfOtherProfile(clientID, userID)) {
		clientID = ""
	}
	presets, err := h.Service.List(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := clientPresetList{Presets: []clientPreset{}}
	byID := map[string]clientPreset{}
	for _, p := range presets {
		sum := p.Summary()
		item := clientPreset{ID: sum.ID, Name: sum.Name, App: sum.App, MediaType: sum.MediaType, Audience: sum.Audience}
		resp.Presets = append(resp.Presets, item)
		byID[item.ID] = item
	}
	titleMedia := strings.TrimSpace(q.Get("mediaType"))
	anime := false
	if (q.Get("titleId") != "" || q.Get("name") != "") && h.TitleAnime != nil && (titleMedia == "movie" || titleMedia == "series") {
		year, _ := strconv.Atoi(q.Get("year"))
		var reason string
		anime, reason = h.TitleAnime(r.Context(), titleMedia, strings.TrimSpace(q.Get("titleId")), strings.TrimSpace(q.Get("name")), year, strings.TrimSpace(q.Get("imdbId")))
		resp.Anime, resp.AnimeReason = &anime, reason
	}
	resp.Offered = []clientPreset{}
	animeOnly := false
	libraryCopy := false // the title's media type has a copy in a local library
	// The profile's own list narrows what the server offers.
	var profilePresets *models.QualityPresetSelection
	if userID != "" && h.UserSettings != nil {
		if us, err := h.UserSettings.Get(userID); err == nil && us != nil {
			profilePresets = us.QualityPresets
		}
	}
	if h.Config != nil {
		if s, err := h.Config.Load(); err == nil {
			resp.Disabled = s.QualityPresets.Disabled
			resp.PreferLibraryCopies = !s.QualityPresets.IgnoreLibraryCopies
			resp.AllowRequestOverride = s.QualityPresets.AllowRequestOverride && !s.QualityPresets.Disabled
			animeOnly = s.QualityPresets.AnimeOnlyInPickers
			offered := s.QualityPresets.PickerPresets
			if s.QualityPresets.Disabled {
				offered = nil // switched off: apps show no picker
			}
			// noLibraryCopy: the client will not play one (the Jellyfin bridge
			// for an episode numbered in another ordering than the files).
			noLibrary, _ := strconv.ParseBool(q.Get("noLibraryCopy"))
			if !s.QualityPresets.Disabled && !s.QualityPresets.IgnoreLibraryCopies && !noLibrary && h.LibraryCopy != nil && (titleMedia == "movie" || titleMedia == "series") {
				year, _ := strconv.Atoi(q.Get("year"))
				// The episode about to play when the client names one (the
				// Jellyfin bridge), else any episode of the series.
				season, _ := strconv.Atoi(q.Get("season"))
				episode, _ := strconv.Atoi(q.Get("episode"))
				absolute, _ := strconv.Atoi(q.Get("absolute"))
				titleID := strings.TrimSpace(q.Get("titleId"))
				if titleID != "" {
					titleID = canonicalPrequeueTitleID(titleMedia, titleID, strings.TrimSpace(q.Get("imdbId")), strings.TrimSpace(q.Get("tmdbId")), strings.TrimSpace(q.Get("tvdbId")))
				}
				found, err := h.LibraryCopy(r, userID, LibraryCopyQuery{MediaType: titleMedia, TitleID: titleID, Title: strings.TrimSpace(q.Get("name")), Year: year,
					IMDBID: strings.TrimSpace(q.Get("imdbId")), TMDBID: strings.TrimSpace(q.Get("tmdbId")), TVDBID: strings.TrimSpace(q.Get("tvdbId")), Season: season, Episode: episode, Absolute: absolute})
				if libraryCopy = err == nil && found != nil; libraryCopy {
					resp.Offered = append(resp.Offered, clientPreset{ID: config.QualityPresetLocal, Name: libraryCopyName, MediaType: titleMedia})
				}
			}
			for _, id := range offered {
				item, ok := byID[id]
				if id == config.QualityPresetSimple {
					item, ok = clientPreset{ID: id, Name: "Simple ranking"}, true
				}
				if ok && (resp.Anime == nil || offeredFor(item, anime, animeOnly)) && (userID == "" || profilePresets.AllowsPickerDefault(s.QualityPresets.HiddenByDefault, id)) {
					resp.Offered = append(resp.Offered, item)
				}
			}
		}
	}
	selection := func(mediaType string, app arr.App, withTitle bool) clientPresetSelection {
		if h.Select == nil {
			return clientPresetSelection{Source: "none"}
		}
		titleAnime := anime && mediaType == titleMedia
		// The title's remembered preset applies to the title's own media type.
		titleID := ""
		if withTitle && mediaType == titleMedia {
			// Remembered under the prequeue's canonical ID, whichever form
			// of the title's ID the app has.
			if raw := strings.TrimSpace(q.Get("titleId")); raw != "" {
				titleID = canonicalPrequeueTitleID(mediaType, raw, strings.TrimSpace(q.Get("imdbId")),
					strings.TrimSpace(q.Get("tmdbId")), strings.TrimSpace(q.Get("tvdbId")))
			}
		}
		id, source := h.Select(mediaType, userID, clientID, titleID, titleAnime)
		// A title a local library has plays from there unless a preset is
		// remembered for it.
		if libraryCopy && mediaType == titleMedia && source != "title" {
			return clientPresetSelection{PresetID: config.QualityPresetLocal, Name: libraryCopyName, Source: "library"}
		}
		// A missing preset, one for the other app or one for another
		// audience falls back to the simple ranking, exactly as the search does.
		if item, ok := byID[id]; ok && item.App == app && item.Audience.Allows(titleAnime) {
			return clientPresetSelection{PresetID: id, Name: item.Name, Source: source}
		}
		return clientPresetSelection{Source: source}
	}
	resp.Movie = selection("movie", arr.AppRadarr, true)
	resp.Series = selection("series", arr.AppSonarr, true)
	resp.MovieDefault = selection("movie", arr.AppRadarr, false)
	resp.SeriesDefault = selection("series", arr.AppSonarr, false)
	presetJSON(w, http.StatusOK, resp)
}

// errPresetNotOffered rejects a profile's own choice of a preset it is not
// offered.
var errPresetNotOffered = errors.New("this preset is not offered to the profile; the administrator chooses the offered presets on the Quality presets page")

// offeredFor decides whether a picker choice is shown for a title:
// anime-only presets only on anime, everything-but-anime presets never on
// anime, and the rest (every title, the simple ranking) everywhere unless
// the admin offers only anime presets on anime titles.
func offeredFor(item clientPreset, anime, animeOnly bool) bool {
	switch item.Audience {
	case qualityprofiles.AudienceAnime:
		return anime
	case qualityprofiles.AudienceNonAnime:
		return !anime
	}
	return !anime || !animeOnly
}

// Selection updates the global preset selection.
func (h *QualityPresetsHandler) Selection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MoviePreset          *string `json:"moviePreset"`
		SeriesPreset         *string `json:"seriesPreset"`
		AllowRequestOverride *bool   `json:"allowRequestOverride"`
		// Enabled switches quality presets on or off server-wide.
		Enabled            *bool     `json:"enabled"`
		PickerPresets      *[]string `json:"pickerPresets"`
		AnimeMoviePreset   *string   `json:"animeMoviePreset"`
		AnimeSeriesPreset  *string   `json:"animeSeriesPreset"`
		AnimeOnlyInPickers *bool     `json:"animeOnlyInPickers"`
		// HiddenByDefault: offered presets profiles see only when they show
		// them themselves; entries that are not offered are dropped.
		HiddenByDefault *[]string `json:"hiddenByDefault"`
		// PreferLibraryCopies plays titles a local library has from there.
		PreferLibraryCopies *bool `json:"preferLibraryCopies"`
	}
	if !decodeStrictPresetBody(w, r, &in) {
		return
	}
	var picker []string
	if in.PickerPresets != nil {
		seen := map[string]bool{}
		for _, id := range *in.PickerPresets {
			id = strings.TrimSpace(id)
			if strings.EqualFold(id, config.QualityPresetSimple) {
				id = config.QualityPresetSimple
			} else if id != "" {
				if _, err := h.Service.Get(r.Context(), id); err != nil {
					presetError(w, err)
					return
				}
			}
			if id != "" && !seen[id] {
				seen[id] = true
				picker = append(picker, id)
			}
		}
	}
	slots := []struct {
		id    *string
		app   arr.App
		anime bool
	}{
		{in.MoviePreset, arr.AppRadarr, false}, {in.SeriesPreset, arr.AppSonarr, false},
		{in.AnimeMoviePreset, arr.AppRadarr, true}, {in.AnimeSeriesPreset, arr.AppSonarr, true},
	}
	for _, slot := range slots {
		if slot.id == nil {
			continue
		}
		if err := h.checkSlot(r.Context(), *slot.id, slot.app, slot.anime); err != nil {
			presetError(w, err)
			return
		}
	}
	settings, err := h.Config.Load()
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if in.MoviePreset != nil {
		settings.QualityPresets.MoviePreset = strings.TrimSpace(*in.MoviePreset)
	}
	if in.SeriesPreset != nil {
		settings.QualityPresets.SeriesPreset = strings.TrimSpace(*in.SeriesPreset)
	}
	if in.AllowRequestOverride != nil {
		settings.QualityPresets.AllowRequestOverride = *in.AllowRequestOverride
	}
	if in.Enabled != nil {
		settings.QualityPresets.Disabled = !*in.Enabled
	}
	if in.PreferLibraryCopies != nil {
		settings.QualityPresets.IgnoreLibraryCopies = !*in.PreferLibraryCopies
	}
	if in.PickerPresets != nil {
		settings.QualityPresets.PickerPresets = picker
	}
	if in.AnimeMoviePreset != nil {
		settings.QualityPresets.AnimeMoviePreset = strings.TrimSpace(*in.AnimeMoviePreset)
	}
	if in.AnimeSeriesPreset != nil {
		settings.QualityPresets.AnimeSeriesPreset = strings.TrimSpace(*in.AnimeSeriesPreset)
	}
	// Only offered presets can be hidden by default; a shorter offer drops
	// the rest.
	if in.HiddenByDefault != nil || in.PickerPresets != nil {
		want := settings.QualityPresets.HiddenByDefault
		if in.HiddenByDefault != nil {
			want = *in.HiddenByDefault
		}
		var hidden []string
		seen := map[string]bool{}
		for _, id := range want {
			id = strings.TrimSpace(id)
			if strings.EqualFold(id, config.QualityPresetSimple) {
				id = config.QualityPresetSimple
			}
			if id != "" && !seen[id] && settings.QualityPresets.OffersInPicker(id) {
				seen[id] = true
				hidden = append(hidden, id)
			}
		}
		settings.QualityPresets.HiddenByDefault = hidden
	}
	if in.AnimeOnlyInPickers != nil {
		settings.QualityPresets.AnimeOnlyInPickers = *in.AnimeOnlyInPickers
	}
	if err := h.Config.Save(settings); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.clearCaches()
	presetJSON(w, http.StatusOK, settings.QualityPresets)
}

// checkSelectable verifies that id is "", "simple" or a preset of app.
// checkSlot is checkSelectable for a normal or an anime slot: anime slots
// take presets for anime or for every title, normal slots presets for
// every title or for everything but anime.
func (h *QualityPresetsHandler) checkSlot(ctx context.Context, id string, app arr.App, anime bool) error {
	if err := h.checkSelectable(ctx, id, app); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" || strings.EqualFold(id, config.QualityPresetSimple) {
		return nil
	}
	p, err := h.Service.Get(ctx, id)
	if err != nil {
		return err
	}
	if anime && p.Audience == qualityprofiles.AudienceNonAnime {
		return fmt.Errorf("preset %q is for everything but anime", p.DisplayName())
	}
	if !anime && p.Audience == qualityprofiles.AudienceAnime {
		return fmt.Errorf("preset %q is for anime only; use an anime slot", p.DisplayName())
	}
	return nil
}

func (h *QualityPresetsHandler) checkSelectable(ctx context.Context, id string, app arr.App) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.EqualFold(id, config.QualityPresetSimple) {
		return nil
	}
	p, err := h.Service.Get(ctx, id)
	if err != nil {
		return err
	}
	if p.App != app {
		return fmt.Errorf("preset %q is for %s, not %s", p.DisplayName(), p.App.MediaType(), app.MediaType())
	}
	return nil
}

// Assign sets the preset for one profile (or device) and media type.
// Accounts may change their own profiles; the administrator any profile.
func (h *QualityPresetsHandler) Assign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID       string  `json:"userId"`
		ClientID     string  `json:"clientId,omitempty"`
		MoviePreset  *string `json:"moviePreset"`
		SeriesPreset *string `json:"seriesPreset"`
		// Anime slots: nil keeps the current value, "inherit" clears it.
		AnimeMoviePreset  *string `json:"animeMoviePreset"`
		AnimeSeriesPreset *string `json:"animeSeriesPreset"`
		// PickerPresets (admin only): left out keeps the list, null offers
		// every preset the server offers, a list limits the profile to it.
		PickerPresets json.RawMessage `json:"pickerPresets"`
		// HiddenPresets (the profile itself or the admin): offered presets
		// the profile hides from its own pickers; left out keeps the list,
		// null or [] hides none.
		HiddenPresets json.RawMessage `json:"hiddenPresets"`
		// ShownPresets (the profile itself or the admin): presets the
		// administrator hides by default that the profile shows; left out
		// keeps the list, null or [] shows none of them.
		ShownPresets json.RawMessage `json:"shownPresets"`
	}
	if !decodeStrictPresetBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.UserID) == "" {
		writeJSONError(w, "userId is required", http.StatusBadRequest)
		return
	}
	master := isMasterRequest(r)
	if !h.canAccessProfile(r, in.UserID) {
		writeJSONError(w, "profile not found", http.StatusNotFound)
		return
	}
	if in.ClientID != "" && !h.deviceOfProfile(in.ClientID, in.UserID) {
		writeJSONError(w, fmt.Sprintf("device %q is not registered for this profile", in.ClientID), http.StatusNotFound)
		return
	}
	pickerGiven := len(in.PickerPresets) > 0
	var picker *[]string
	if pickerGiven {
		if !master {
			writeJSONError(w, "only the server administrator chooses which presets a profile is offered", http.StatusForbidden)
			return
		}
		var list []string
		if err := json.Unmarshal(in.PickerPresets, &list); err != nil {
			writeJSONError(w, "pickerPresets must be a list or null", http.StatusBadRequest)
			return
		}
		if string(in.PickerPresets) != "null" {
			clean := []string{}
			for _, id := range list {
				id = strings.TrimSpace(id)
				if strings.EqualFold(id, config.QualityPresetSimple) {
					id = config.QualityPresetSimple
				} else if _, err := h.Service.Get(r.Context(), id); err != nil {
					presetError(w, err)
					return
				}
				clean = append(clean, id)
			}
			picker = &clean
		}
	}
	// The profile's own overrides of what its pickers show: null or [] drops
	// them (the profile follows the administrator's defaults again).
	overrideList := func(raw json.RawMessage, field string) (*[]string, bool) {
		if in.ClientID != "" {
			writeJSONError(w, field+" belong to the profile, not to a device", http.StatusBadRequest)
			return nil, false
		}
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			writeJSONError(w, field+" must be a list or null", http.StatusBadRequest)
			return nil, false
		}
		seen := map[string]bool{}
		clean := []string{}
		for _, id := range list {
			id = strings.TrimSpace(id)
			if strings.EqualFold(id, config.QualityPresetSimple) {
				id = config.QualityPresetSimple
			} else if _, err := h.Service.Get(r.Context(), id); err != nil {
				presetError(w, err)
				return nil, false
			}
			if !seen[id] {
				seen[id] = true
				clean = append(clean, id)
			}
		}
		if len(clean) == 0 {
			return nil, true
		}
		return &clean, true
	}
	hiddenGiven, shownGiven := len(in.HiddenPresets) > 0, len(in.ShownPresets) > 0
	var hidden, shown *[]string
	if hiddenGiven {
		var ok bool
		if hidden, ok = overrideList(in.HiddenPresets, "hiddenPresets"); !ok {
			return
		}
	}
	if shownGiven {
		var ok bool
		if shown, ok = overrideList(in.ShownPresets, "shownPresets"); !ok {
			return
		}
	}
	// Profiles choose their own defaults only among the presets they are
	// offered; the administrator may assign any preset.
	allowed := func(id string) bool { return true }
	if !master {
		var settings config.Settings
		if h.Config != nil {
			settings, _ = h.Config.Load()
		}
		var profile *models.QualityPresetSelection
		if h.UserSettings != nil {
			if us, err := h.UserSettings.Get(in.UserID); err == nil && us != nil {
				profile = us.QualityPresets
			}
		}
		allowed = func(id string) bool {
			return settings.QualityPresets.OffersInPicker(id) && profile.AllowsPickerDefault(settings.QualityPresets.HiddenByDefault, id)
		}
	}
	// The stored choices: a slot sent back unchanged is kept even when it
	// is no longer offered (the administrator may have assigned it).
	var stored models.QualityPresetSelection
	if in.ClientID != "" {
		if h.ClientSettings != nil {
			if cs, err := h.ClientSettings.Get(in.ClientID, in.UserID); err == nil && cs != nil {
				stored = models.QualityPresetSelection{MoviePreset: cs.QualityPresetMovie, SeriesPreset: cs.QualityPresetSeries,
					AnimeMoviePreset: cs.QualityPresetAnimeMovie, AnimeSeriesPreset: cs.QualityPresetAnimeSeries}
			}
		}
	} else if h.UserSettings != nil {
		if us, err := h.UserSettings.Get(in.UserID); err == nil && us != nil && us.QualityPresets != nil {
			stored = *us.QualityPresets
		}
	}
	norm := func(v, current *string, app arr.App, anime bool) (*string, error) {
		if v == nil {
			return nil, nil
		}
		s := strings.TrimSpace(*v)
		if s == "" || s == "inherit" {
			return nil, nil
		}
		if current != nil && *current == s {
			return &s, nil
		}
		if !allowed(s) {
			return nil, errPresetNotOffered
		}
		if err := h.checkSlot(r.Context(), s, app, anime); err != nil {
			return nil, err
		}
		return &s, nil
	}
	movie, err := norm(in.MoviePreset, stored.MoviePreset, arr.AppRadarr, false)
	if err != nil {
		presetError(w, err)
		return
	}
	series, err := norm(in.SeriesPreset, stored.SeriesPreset, arr.AppSonarr, false)
	if err != nil {
		presetError(w, err)
		return
	}
	animeMovie, err := norm(in.AnimeMoviePreset, stored.AnimeMoviePreset, arr.AppRadarr, true)
	if err != nil {
		presetError(w, err)
		return
	}
	animeSeries, err := norm(in.AnimeSeriesPreset, stored.AnimeSeriesPreset, arr.AppSonarr, true)
	if err != nil {
		presetError(w, err)
		return
	}
	// keep returns the stored slot when the request left it out.
	keep := func(requested *string, chosen, stored *string) *string {
		if requested == nil {
			return stored
		}
		return chosen
	}
	if in.ClientID != "" {
		if h.ClientSettings == nil {
			writeJSONError(w, "device settings unavailable", http.StatusServiceUnavailable)
			return
		}
		cs, err := h.ClientSettings.Get(in.ClientID, in.UserID)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var next models.ClientFilterSettings
		if cs != nil {
			next = *cs
		}
		next.QualityPresetMovie = keep(in.MoviePreset, movie, next.QualityPresetMovie)
		next.QualityPresetSeries = keep(in.SeriesPreset, series, next.QualityPresetSeries)
		next.QualityPresetAnimeMovie = keep(in.AnimeMoviePreset, animeMovie, next.QualityPresetAnimeMovie)
		next.QualityPresetAnimeSeries = keep(in.AnimeSeriesPreset, animeSeries, next.QualityPresetAnimeSeries)
		movie, series = next.QualityPresetMovie, next.QualityPresetSeries
		animeMovie, animeSeries = next.QualityPresetAnimeMovie, next.QualityPresetAnimeSeries
		if err := h.ClientSettings.Update(in.ClientID, in.UserID, next); err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Settings are only kept for registered devices and profiles; anything
		// else is dropped on save, which must not look like success.
		if movie != nil || series != nil || animeMovie != nil || animeSeries != nil {
			if saved, err := h.ClientSettings.Get(in.ClientID, in.UserID); err != nil || saved == nil || (saved.QualityPresetMovie == nil && saved.QualityPresetSeries == nil && saved.QualityPresetAnimeMovie == nil && saved.QualityPresetAnimeSeries == nil) {
				writeJSONError(w, fmt.Sprintf("device %q is not registered for this profile", in.ClientID), http.StatusNotFound)
				return
			}
		}
	} else {
		if h.UserSettings == nil {
			writeJSONError(w, "profile settings unavailable", http.StatusServiceUnavailable)
			return
		}
		us, err := h.UserSettings.Get(in.UserID)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var next models.UserSettings
		if us != nil {
			next = *us
		}
		if !pickerGiven {
			picker = stored.PickerPresets
		}
		if !hiddenGiven {
			hidden = stored.HiddenPresets
		}
		if !shownGiven {
			shown = stored.ShownPresets
		}
		sel := &models.QualityPresetSelection{PickerPresets: picker, HiddenPresets: hidden, ShownPresets: shown,
			MoviePreset: keep(in.MoviePreset, movie, stored.MoviePreset), SeriesPreset: keep(in.SeriesPreset, series, stored.SeriesPreset),
			AnimeMoviePreset: keep(in.AnimeMoviePreset, animeMovie, stored.AnimeMoviePreset), AnimeSeriesPreset: keep(in.AnimeSeriesPreset, animeSeries, stored.AnimeSeriesPreset)}
		movie, series = sel.MoviePreset, sel.SeriesPreset
		animeMovie, animeSeries = sel.AnimeMoviePreset, sel.AnimeSeriesPreset
		if sel.IsEmpty() {
			sel = nil
		}
		next.QualityPresets = sel
		if err := h.UserSettings.Update(in.UserID, next); err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	h.clearCaches()
	presetJSON(w, http.StatusOK, presetAssign{MoviePreset: movie, SeriesPreset: series, AnimeMoviePreset: animeMovie, AnimeSeriesPreset: animeSeries, PickerPresets: picker, HiddenPresets: hidden, ShownPresets: shown})
}

// canAccessProfile: the administrator may read and change every profile,
// an account only its own.
func (h *QualityPresetsHandler) canAccessProfile(r *http.Request, profileID string) bool {
	if isMasterRequest(r) {
		return true
	}
	account := requestAccountID(r)
	return account != "" && h.ProfileOwner != nil && h.ProfileOwner(profileID) == account
}

// deviceOfProfile reports whether a registered device has been used by the
// profile; device choices are only read and written for such pairs.
func (h *QualityPresetsHandler) deviceOfProfile(clientID, profileID string) bool {
	if h.Devices == nil {
		return false
	}
	c, err := h.Devices.Get(clientID)
	if err != nil || c == nil {
		return false
	}
	return c.UserID == profileID || h.Devices.HasProfile(clientID, profileID)
}

// deviceOfOtherProfile reports a registered device the profile has never
// used. An unknown device is nobody else's; a lookup error counts as
// someone else's, so nothing is revealed.
func (h *QualityPresetsHandler) deviceOfOtherProfile(clientID, profileID string) bool {
	if h.Devices == nil {
		return false
	}
	c, err := h.Devices.Get(clientID)
	if err != nil {
		return true
	}
	if c == nil {
		return false
	}
	return c.UserID != profileID && !h.Devices.HasProfile(clientID, profileID)
}

// keepUserQualityPresets keeps a profile's stored preset choices when its
// settings are replaced as a whole document. They change only through
// /quality-presets/assign, which checks what the caller may choose; clients
// that send back settings without them must not clear them either.
func keepUserQualityPresets(settings *models.UserSettings, previous *models.UserSettings) {
	settings.QualityPresets = nil
	if previous != nil && previous.QualityPresets != nil {
		kept := *previous.QualityPresets
		settings.QualityPresets = &kept
	}
}

// keepClientQualityPresets is keepUserQualityPresets for a device's settings.
func keepClientQualityPresets(settings *models.ClientFilterSettings, previous *models.ClientFilterSettings) {
	settings.QualityPresetMovie, settings.QualityPresetSeries = nil, nil
	settings.QualityPresetAnimeMovie, settings.QualityPresetAnimeSeries = nil, nil
	if previous != nil {
		settings.QualityPresetMovie, settings.QualityPresetSeries = previous.QualityPresetMovie, previous.QualityPresetSeries
		settings.QualityPresetAnimeMovie, settings.QualityPresetAnimeSeries = previous.QualityPresetAnimeMovie, previous.QualityPresetAnimeSeries
	}
}

// libraryCopyName names the "Library copy" choice (config.QualityPresetLocal).
const libraryCopyName = "Library copy"
