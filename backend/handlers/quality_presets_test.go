package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

type memUserSettings struct {
	m map[string]models.UserSettings
}

func (f *memUserSettings) Get(id string) (*models.UserSettings, error) {
	if v, ok := f.m[id]; ok {
		return &v, nil
	}
	return nil, nil
}
func (f *memUserSettings) Update(id string, s models.UserSettings) error { f.m[id] = s; return nil }

type countingClearer struct{ search, prequeue int }

func (c *countingClearer) ClearSearchCache() { c.search++ }
func (c *countingClearer) DeleteAll()        { c.prequeue++ }

const handlerPresetJSON = `{"app":"sonarr","name":"Handler Test","profile":{"name":"Handler Test","minFormatScore":0,
 "items":[{"name":"WEBDL-1080p","allowed":true},{"name":"HDTV-1080p","allowed":false}],"formatItems":{"German":"g"}},
 "customFormats":[{"trash_id":"g","name":"German","trash_scores":{"default":1000},"specifications":[{"name":"de","implementation":"LanguageSpecification","negate":false,"required":true,"fields":{"value":4}}]}]}`

func newPresetHandler(t *testing.T) (*QualityPresetsHandler, *countingClearer) {
	t.Helper()
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	clear := &countingClearer{}
	svc := qualityprofiles.NewService(qualityprofiles.NewMemoryStore(), nil, nil)
	// As in main: a preset change that affects ranking clears the caches.
	svc.SetOnChange(func(string) { clear.ClearSearchCache(); clear.DeleteAll() })
	return &QualityPresetsHandler{
		Service:       svc,
		Config:        mgr,
		UserSettings:  &memUserSettings{m: map[string]models.UserSettings{}},
		SearchCache:   clear,
		PrequeueStore: clear,
	}, clear
}

func presetRequest(method, target, body string, master bool, account string, vars map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(r.Context(), auth.ContextKeyIsMaster, master)
	ctx = context.WithValue(ctx, auth.ContextKeyAccountID, account)
	r = r.WithContext(ctx)
	if vars != nil {
		r = mux.SetURLVars(r, vars)
	}
	return r
}

func decodeDetail(t *testing.T, rec *httptest.ResponseRecorder) presetDetailJSON {
	t.Helper()
	var d presetDetailJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return d
}

type presetDetailJSON struct {
	Preset struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Origin struct {
			ArrAPIKey string `json:"arrApiKey"`
		} `json:"origin"`
	} `json:"preset"`
	Resolved struct {
		Profile struct {
			FormatScores map[string]int `json:"formatScores"`
		} `json:"profile"`
	} `json:"resolved"`
	Errors []string `json:"errors"`
}

func TestQualityPresetAPIFlow(t *testing.T) {
	h, clear := newPresetHandler(t)

	rec := httptest.NewRecorder()
	h.ImportJSON(rec, presetRequest(http.MethodPost, "/api/quality-presets/import/json?app=sonarr", handlerPresetJSON, true, "acc", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeDetail(t, rec).Preset.ID

	rec = httptest.NewRecorder()
	h.List(rec, presetRequest(http.MethodGet, "/api/quality-presets", "", true, "acc", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Handler Test"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Override(rec, presetRequest(http.MethodPut, "/x", `{"path":"score:g","value":1234}`, true, "acc", map[string]string{"id": id}))
	if rec.Code != http.StatusOK || decodeDetail(t, rec).Resolved.Profile.FormatScores["g"] != 1234 {
		t.Fatalf("override: %d %s", rec.Code, rec.Body.String())
	}
	if clear.search == 0 || clear.prequeue == 0 {
		t.Error("a ranking change must clear the search and prequeue caches")
	}

	rec = httptest.NewRecorder()
	h.Override(rec, presetRequest(http.MethodPut, "/x", `{"path":"bogus:x","value":1}`, true, "acc", map[string]string{"id": id}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid override path should be rejected, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Explain(rec, presetRequest(http.MethodPost, "/x", `{"titles":["Dark.S01E01.GERMAN.1080p.WEB.h264-A","Dark.S01E01.1080p.HDTV.x264-B"],"originalLanguage":"en","detail":true}`, false, "acc", map[string]string{"id": id}))
	var explained []qualityprofiles.TitleResult
	if err := json.Unmarshal(rec.Body.Bytes(), &explained); err != nil || len(explained) != 2 {
		t.Fatalf("explain: %d %s", rec.Code, rec.Body.String())
	}
	if explained[0].Title != "Dark.S01E01.GERMAN.1080p.WEB.h264-A" || !explained[0].Evaluation.Allowed || explained[1].Evaluation.Allowed {
		t.Errorf("explain ranking %+v", explained)
	}
	if len(explained[0].Formats) == 0 {
		t.Error("detail=true should include condition results")
	}

	rec = httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"moviePreset":"`+id+`","seriesPreset":""}`, true, "acc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a series preset must not be selectable for movies, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"moviePreset":"simple","seriesPreset":"`+id+`","allowRequestOverride":false}`, true, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("selection: %d %s", rec.Code, rec.Body.String())
	}
	s, _ := h.Config.Load()
	if s.QualityPresets.SeriesPreset != id || s.QualityPresets.AllowRequestOverride {
		t.Errorf("selection not saved: %+v", s.QualityPresets)
	}

	rec = httptest.NewRecorder()
	h.Export(rec, presetRequest(http.MethodGet, "/x", "", true, "acc", map[string]string{"id": id}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "Handler-Test.json") {
		t.Errorf("export: %d %v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, presetRequest(http.MethodDelete, "/x", "", true, "acc", map[string]string{"id": id}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	s, _ = h.Config.Load()
	if s.QualityPresets.SeriesPreset != "" {
		t.Error("deleting the active preset must fall back to the simple ranking")
	}
	rec = httptest.NewRecorder()
	h.Get(rec, presetRequest(http.MethodGet, "/x", "", true, "acc", map[string]string{"id": id}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("deleted preset: %d", rec.Code)
	}
}

func TestQualityPresetAssignOwnership(t *testing.T) {
	h, _ := newPresetHandler(t)
	h.ProfileOwner = func(profileID string) string {
		if profileID == "mine" {
			return "acc-1"
		}
		return "acc-2"
	}

	rec := httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"theirs","seriesPreset":"simple"}`, false, "acc-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("another account's profile must look missing, got %d", rec.Code)
	}
	// A profile may only choose what the administrator offers.
	rec = httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"mine","seriesPreset":"simple"}`, false, "acc-1", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("a preset that is not offered must be refused, got %d", rec.Code)
	}
	settings, _ := h.Config.Load()
	settings.QualityPresets.PickerPresets = []string{config.QualityPresetSimple}
	if err := h.Config.Save(settings); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"mine","seriesPreset":"simple","moviePreset":"inherit"}`, false, "acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("own profile: %d %s", rec.Code, rec.Body.String())
	}
	us, _ := h.UserSettings.Get("mine")
	if us == nil || us.QualityPresets == nil || us.QualityPresets.SeriesPreset == nil || *us.QualityPresets.SeriesPreset != "simple" || us.QualityPresets.MoviePreset != nil {
		t.Errorf("assignment stored wrong: %+v", us)
	}
	rec = httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"mine","seriesPreset":"does-not-exist"}`, true, "acc-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown preset: %d", rec.Code)
	}
}

func TestQualityPresetSourcesValidation(t *testing.T) {
	h, _ := newPresetHandler(t)
	rec := httptest.NewRecorder()
	h.Sources(rec, presetRequest(http.MethodPut, "/x", `[{"id":"Bad ID","type":"github","repo":"x/y","enabled":true}]`, true, "acc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid source id accepted: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Sources(rec, presetRequest(http.MethodPut, "/x", `[{"id":"fork","name":"Fork","type":"github","repo":"me/guides","ref":"main","priority":0,"enabled":true},{"id":"bundled","name":"Bundled","type":"bundled","priority":5,"enabled":true}]`, true, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sources: %d %s", rec.Code, rec.Body.String())
	}
	s, _ := h.Config.Load()
	got := SourcesFromConfig(s.QualityPresets.Sources)
	if len(got) != 2 || got[0].Repo != "me/guides" || got[1].Type != qualityprofiles.SourceBundled {
		t.Errorf("sources not stored: %+v", got)
	}
	if def := SourcesFromConfig(nil); len(def) != 1 || def[0].Repo != "TRaSH-Guides/Guides" {
		t.Errorf("default sources %+v", def)
	}
}

func TestQualityPresetsPageRenders(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	handler := NewAdminUIHandler(settingsPath, "", nil, nil, nil, config.NewManager(settingsPath))
	if handler.presetsTemplate == nil {
		t.Fatal("quality presets template failed to load")
	}
	for _, admin := range []bool{true, false} {
		var out bytes.Buffer
		err := handler.presetsTemplate.ExecuteTemplate(&out, "base", AdminPageData{
			CurrentPath: "/admin/quality-presets", BasePath: "/admin", IsAdmin: admin,
			Users: []models.User{{ID: "u1", Name: "Alice <script>"}},
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		html := out.String()
		for _, marker := range []string{"Quality presets", "/api/quality-presets';", "qpDetail", `aria-current="page"`} {
			if !strings.Contains(html, marker) {
				t.Errorf("admin=%v: page missing %q", admin, marker)
			}
		}
		if strings.Contains(html, "Alice <script>") {
			t.Error("profile names must be escaped")
		}
	}
}

func TestQualityPresetRoutesNotShadowed(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	admin := NewAdminUIHandler(settingsPath, "", nil, nil, nil, config.NewManager(settingsPath))
	h, _ := newPresetHandler(t)
	r := mux.NewRouter()
	admin.RegisterQualityPresets(r, h)
	checked := 0
	err := r.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		tpl, err := route.GetPathTemplate()
		if err != nil || strings.Contains(tpl, "{") {
			return nil
		}
		methods, _ := route.GetMethods()
		for _, m := range methods {
			var match mux.RouteMatch
			if !r.Match(httptest.NewRequest(m, tpl, nil), &match) {
				t.Errorf("%s %s does not match any route", m, tpl)
				continue
			}
			if got, _ := match.Route.GetPathTemplate(); got != tpl {
				t.Errorf("%s %s is shadowed by %s", m, tpl, got)
			}
			checked++
		}
		return nil
	})
	if err != nil || checked < 15 {
		t.Fatalf("walk: %v, checked %d", err, checked)
	}
}

func TestQualityPresetClientList(t *testing.T) {
	h, _ := newPresetHandler(t)
	rec := httptest.NewRecorder()
	h.ImportJSON(rec, presetRequest(http.MethodPost, "/x?app=sonarr", handlerPresetJSON, true, "acc", nil))
	id := decodeDetail(t, rec).Preset.ID
	var gotUser, gotClient string
	h.Select = func(mediaType, userID, clientID, _ string, anime bool) (string, string) {
		gotUser, gotClient = userID, clientID
		// The movie side points at the series preset: the search would
		// fall back to the simple ranking, and so must the list.
		return id, "profile"
	}
	h.ProfileOwner = func(string) string { return "acc" }
	h.Devices = fakeDevices{"c1": "u1"}

	rec = httptest.NewRecorder()
	h.ClientList(rec, presetRequest(http.MethodGet, "/api/quality-presets?userId=u1&clientId=c1", "", false, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("client list: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		AllowRequestOverride bool `json:"allowRequestOverride"`
		Presets              []struct {
			ID, Name, App, MediaType string
		} `json:"presets"`
		Movie, Series struct {
			PresetID, Name, Source string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Presets) != 1 || got.Presets[0].ID != id || got.Presets[0].MediaType != "series" || got.Presets[0].App != "sonarr" {
		t.Errorf("presets %+v", got.Presets)
	}
	if got.Series.PresetID != id || got.Series.Name != "Handler Test" || got.Series.Source != "profile" {
		t.Errorf("series selection %+v", got.Series)
	}
	if got.Movie.PresetID != "" || got.Movie.Source != "profile" {
		t.Errorf("a series preset selected for movies means simple ranking: %+v", got.Movie)
	}
	if gotUser != "u1" || gotClient != "c1" {
		t.Errorf("selector got user %q client %q", gotUser, gotClient)
	}
	if !got.AllowRequestOverride {
		t.Error("per-search override defaults to allowed")
	}
	if strings.Contains(rec.Body.String(), "origin") || strings.Contains(rec.Body.String(), "sources") {
		t.Error("the app list should not expose admin details")
	}
}

// registeredOnlyClients keeps settings only for known devices, like the real
// client settings service does on save.
type registeredOnlyClients struct {
	known map[string]bool
	m     map[string]models.ClientFilterSettings
}

func (c *registeredOnlyClients) Get(clientID, userID string) (*models.ClientFilterSettings, error) {
	if v, ok := c.m[clientID+"|"+userID]; ok {
		return &v, nil
	}
	return nil, nil
}

func (c *registeredOnlyClients) Update(clientID, userID string, s models.ClientFilterSettings) error {
	if c.known[clientID] {
		c.m[clientID+"|"+userID] = s
	}
	return nil
}

// fakeDevices maps registered device IDs to the profile that uses them.
type fakeDevices map[string]string

func (d fakeDevices) Get(id string) (*models.Client, error) {
	if u, ok := d[id]; ok {
		return &models.Client{ID: id, UserID: u}, nil
	}
	return nil, nil
}
func (d fakeDevices) HasProfile(clientID, userID string) bool { return d[clientID] == userID }

// Accounts read and change only their own profiles and those profiles'
// devices; anything else looks missing.
func TestQualityPresetProfileAndDeviceAccess(t *testing.T) {
	h, _ := newPresetHandler(t)
	clients := &registeredOnlyClients{known: map[string]bool{"tv": true, "their-tv": true}, m: map[string]models.ClientFilterSettings{}}
	h.ClientSettings = clients
	h.Devices = fakeDevices{"tv": "mine", "their-tv": "theirs"}
	h.ProfileOwner = func(profileID string) string {
		if profileID == "mine" {
			return "acc-1"
		}
		return "acc-2"
	}
	simple := "simple"
	_ = h.UserSettings.Update("theirs", models.UserSettings{QualityPresets: &models.QualityPresetSelection{SeriesPreset: &simple}})

	rec := httptest.NewRecorder()
	h.List(rec, presetRequest(http.MethodGet, "/x?userId=mine&userId=theirs", "", false, "acc-1", nil))
	if strings.Contains(rec.Body.String(), `"theirs"`) || !strings.Contains(rec.Body.String(), `"mine"`) {
		t.Errorf("list must only include own profiles: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ClientList(rec, presetRequest(http.MethodGet, "/x?userId=theirs", "", false, "acc-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("another account's effective presets: %d", rec.Code)
	}
	var gotClient string
	h.Select = func(_, _, clientID, _ string, _ bool) (string, string) { gotClient = clientID; return "", "none" }
	rec = httptest.NewRecorder()
	h.ClientList(rec, presetRequest(http.MethodGet, "/x?userId=mine&clientId=their-tv", "", false, "acc-1", nil))
	if rec.Code != http.StatusOK || gotClient != "" {
		t.Errorf("a device of another profile must be ignored: %d client %q", rec.Code, gotClient)
	}

	settings, _ := h.Config.Load()
	settings.QualityPresets.PickerPresets = []string{config.QualityPresetSimple}
	if err := h.Config.Save(settings); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"mine","clientId":"their-tv","seriesPreset":"simple"}`, false, "acc-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("writing another profile's device: %d", rec.Code)
	}
	if len(clients.m) != 0 {
		t.Errorf("nothing may be stored: %+v", clients.m)
	}
	rec = httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"mine","clientId":"tv","seriesPreset":"simple"}`, false, "acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("own device: %d %s", rec.Code, rec.Body.String())
	}
}

func TestQualityPresetAssignDevice(t *testing.T) {
	h, _ := newPresetHandler(t)
	clients := &registeredOnlyClients{known: map[string]bool{"tv": true}, m: map[string]models.ClientFilterSettings{}}
	h.ClientSettings = clients
	h.Devices = fakeDevices{"tv": "u1"}

	rec := httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"u1","clientId":"tv","seriesPreset":"simple"}`, true, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("registered device: %d %s", rec.Code, rec.Body.String())
	}
	if cs, _ := clients.Get("tv", "u1"); cs == nil || cs.QualityPresetSeries == nil || *cs.QualityPresetSeries != "simple" {
		t.Errorf("device choice not stored: %+v", cs)
	}

	rec = httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"u1","clientId":"ghost","seriesPreset":"simple"}`, true, "acc", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unregistered device must be reported, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Assign(rec, presetRequest(http.MethodPut, "/x", `{"userId":"u1","clientId":"tv","seriesPreset":"inherit"}`, true, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing: %d %s", rec.Code, rec.Body.String())
	}
	if cs, _ := clients.Get("tv", "u1"); cs != nil && cs.QualityPresetSeries != nil {
		t.Errorf("inherit must clear the device choice: %+v", cs)
	}
}

func TestQualityPresetPickerChoices(t *testing.T) {
	h, _ := newPresetHandler(t)
	rec := httptest.NewRecorder()
	h.ImportJSON(rec, presetRequest(http.MethodPost, "/x?app=sonarr", handlerPresetJSON, true, "acc", nil))
	id := decodeDetail(t, rec).Preset.ID

	rec = httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"moviePreset":"simple","seriesPreset":"","pickerPresets":["missing"]}`, true, "acc", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown preset must not be offered, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"moviePreset":"simple","seriesPreset":"","allowRequestOverride":false,"pickerPresets":["`+id+`"," SIMPLE ","`+id+`",""]}`, true, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("selection: %d %s", rec.Code, rec.Body.String())
	}
	s, _ := h.Config.Load()
	if got := strings.Join(s.QualityPresets.PickerPresets, ","); got != id+",simple" {
		t.Fatalf("picker presets = %q, want deduplicated %s,simple", got, id)
	}
	if !s.QualityPresets.AllowsRequestPreset(id) || s.QualityPresets.AllowsRequestPreset("other") {
		t.Error("offered presets must be requestable even with per-search choice off; others must not")
	}

	// The admin list reports the offered presets; the app list resolves them.
	rec = httptest.NewRecorder()
	h.List(rec, presetRequest(http.MethodGet, "/x", "", true, "acc", nil))
	if !strings.Contains(rec.Body.String(), `"pickerPresets":["`+id+`","simple"]`) {
		t.Errorf("admin list: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ClientList(rec, presetRequest(http.MethodGet, "/x", "", false, "acc", nil))
	var list struct {
		Offered []struct{ ID, Name, MediaType string } `json:"offered"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Offered) != 2 || list.Offered[0].ID != id || list.Offered[0].Name != "Handler Test" || list.Offered[0].MediaType != "series" ||
		list.Offered[1].ID != "simple" || list.Offered[1].MediaType != "" {
		t.Fatalf("offered = %+v", list.Offered)
	}

	// Deleting a preset stops offering it.
	rec = httptest.NewRecorder()
	h.Delete(rec, presetRequest(http.MethodDelete, "/x", "", true, "acc", map[string]string{"id": id}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	s, _ = h.Config.Load()
	if got := strings.Join(s.QualityPresets.PickerPresets, ","); got != "simple" {
		t.Errorf("picker presets after delete = %q", got)
	}
}

// importPreset imports handlerPresetJSON under a name and audience.
func importPreset(t *testing.T, h *QualityPresetsHandler, name string, audience qualityprofiles.Audience) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ImportJSON(rec, presetRequest(http.MethodPost, "/x?app=sonarr", strings.Replace(handlerPresetJSON, `"name":"Handler Test"`, `"name":"`+name+`"`, 1), true, "acc", nil))
	id := decodeDetail(t, rec).Preset.ID
	if _, err := h.Service.UpdateSettings(context.Background(), id, qualityprofiles.SettingsUpdate{Audience: &audience}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAnimeSlotsAndPickers(t *testing.T) {
	h, _ := newPresetHandler(t)
	anime := importPreset(t, h, "Anime HD", qualityprofiles.AudienceAnime)
	normal := importPreset(t, h, "German HD", qualityprofiles.AudienceNonAnime)
	all := importPreset(t, h, "Any", qualityprofiles.AudienceAll)

	put := func(body string) int {
		rec := httptest.NewRecorder()
		h.Selection(rec, presetRequest(http.MethodPut, "/x", body, true, "acc", nil))
		return rec.Code
	}
	if code := put(`{"moviePreset":"","seriesPreset":"` + anime + `"}`); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Errorf("anime-only preset in a normal slot: %d", code)
	}
	if code := put(`{"moviePreset":"","seriesPreset":"","animeSeriesPreset":"` + normal + `"}`); code < 400 {
		t.Errorf("non-anime preset in an anime slot: %d", code)
	}
	if code := put(`{"moviePreset":"","seriesPreset":"` + normal + `","animeSeriesPreset":"` + anime + `","animeOnlyInPickers":true,"pickerPresets":["simple","` + all + `","` + anime + `","` + normal + `"]}`); code != http.StatusOK {
		t.Fatalf("selection: %d", code)
	}
	s, _ := h.Config.Load()
	if s.QualityPresets.AnimeSeriesPreset != anime || !s.QualityPresets.AnimeOnlyInPickers {
		t.Fatalf("anime settings not stored: %+v", s.QualityPresets)
	}

	// Profile assignment: anime slots are kept when left out, cleared by "inherit".
	assign := func(body string) int {
		rec := httptest.NewRecorder()
		h.Assign(rec, presetRequest(http.MethodPut, "/x", body, true, "acc", nil))
		return rec.Code
	}
	if code := assign(`{"userId":"u1","seriesPreset":"` + normal + `","animeSeriesPreset":"` + anime + `"}`); code != http.StatusOK {
		t.Fatalf("assign: %d", code)
	}
	if code := assign(`{"userId":"u1","seriesPreset":"simple"}`); code != http.StatusOK {
		t.Fatalf("assign without anime: %d", code)
	}
	us, _ := h.UserSettings.Get("u1")
	if us.QualityPresets.AnimeSeriesPreset == nil || *us.QualityPresets.AnimeSeriesPreset != anime {
		t.Fatalf("an omitted anime slot must be kept: %+v", us.QualityPresets)
	}
	if code := assign(`{"userId":"u1","seriesPreset":"simple","animeSeriesPreset":"inherit"}`); code != http.StatusOK {
		t.Fatalf("assign inherit: %d", code)
	}
	us, _ = h.UserSettings.Get("u1")
	if us.QualityPresets.AnimeSeriesPreset != nil {
		t.Fatal("inherit must clear the anime slot")
	}
	if code := assign(`{"userId":"u1","animeSeriesPreset":"` + normal + `"}`); code < 400 {
		t.Errorf("non-anime preset accepted in a profile anime slot: %d", code)
	}

	// Pickers follow the title.
	var gotAnime bool
	h.Select = func(mediaType, userID, clientID, _ string, isAnime bool) (string, string) {
		gotAnime = isAnime
		if isAnime {
			return anime, "global-anime"
		}
		return normal, "global"
	}
	list := func(isAnime bool, withTitle bool) (names []string, auto string, reported *bool) {
		t.Helper()
		h.TitleAnime = func(ctx context.Context, mediaType, titleID, name string, year int, imdbID string) (bool, string) {
			return isAnime, "test"
		}
		target := "/x"
		if withTitle {
			target = "/x?mediaType=series&titleId=tvdb:series:1&name=Show"
		}
		rec := httptest.NewRecorder()
		h.ClientList(rec, presetRequest(http.MethodGet, target, "", false, "acc", nil))
		var resp struct {
			Offered []struct{ Name string } `json:"offered"`
			Series  struct{ Name string }   `json:"series"`
			Anime   *bool                   `json:"anime"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		for _, o := range resp.Offered {
			names = append(names, o.Name)
		}
		return names, resp.Series.Name, resp.Anime
	}
	names, auto, reported := list(true, true)
	if strings.Join(names, ",") != "Anime HD" || auto != "Anime HD" || reported == nil || !*reported || !gotAnime {
		t.Errorf("anime title, anime-only pickers: offered %q auto %q anime %v", names, auto, reported)
	}
	names, auto, _ = list(false, true)
	if strings.Join(names, ",") != "Simple ranking,Any,German HD" || auto != "German HD" || gotAnime {
		t.Errorf("non-anime title: offered %q auto %q", names, auto)
	}
	put(`{"moviePreset":"","seriesPreset":"` + normal + `","animeOnlyInPickers":false}`)
	if names, _, _ = list(true, true); strings.Join(names, ",") != "Simple ranking,Any,Anime HD" {
		t.Errorf("anime title, all pickers: offered %q", names)
	}
	if names, _, reported = list(true, false); len(names) != 4 || reported != nil {
		t.Errorf("without a title every offered preset is listed: %q %v", names, reported)
	}

	// Deleting a preset clears the anime slot that used it.
	rec := httptest.NewRecorder()
	h.Delete(rec, presetRequest(http.MethodDelete, "/x", "", true, "acc", map[string]string{"id": anime}))
	if s, _ = h.Config.Load(); s.QualityPresets.AnimeSeriesPreset != "" {
		t.Errorf("anime slot after delete = %q", s.QualityPresets.AnimeSeriesPreset)
	}
}

// The administrator narrows per profile which offered presets it sees in
// pickers and may choose; the app list and assignments follow that list.
func TestProfilePickerPresets(t *testing.T) {
	h, _ := newPresetHandler(t)
	h.ProfileOwner = func(string) string { return "acc-1" }
	a := importPreset(t, h, "German HD", qualityprofiles.AudienceAll)
	b := importPreset(t, h, "English HD", qualityprofiles.AudienceAll)
	rec := httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"moviePreset":"","seriesPreset":"","pickerPresets":["simple","`+a+`","`+b+`"]}`, true, "acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("selection: %d", rec.Code)
	}
	assign := func(body string, master bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Assign(rec, presetRequest(http.MethodPut, "/x", body, master, "acc-1", nil))
		return rec
	}
	if rec := assign(`{"userId":"kid","pickerPresets":["`+a+`"]}`, false); rec.Code != http.StatusForbidden {
		t.Errorf("a profile owner must not change its own picker list: %d", rec.Code)
	}
	if rec := assign(`{"userId":"kid","pickerPresets":["`+a+`"]}`, true); rec.Code != http.StatusOK {
		t.Fatalf("admin sets picker list: %d %s", rec.Code, rec.Body.String())
	}
	offered := func(user string) []string {
		rec := httptest.NewRecorder()
		h.ClientList(rec, presetRequest(http.MethodGet, "/x?userId="+user, "", false, "acc-1", nil))
		var resp struct {
			Offered []struct{ Name string } `json:"offered"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		var names []string
		for _, o := range resp.Offered {
			names = append(names, o.Name)
		}
		return names
	}
	if got := strings.Join(offered("kid"), ","); got != "German HD" {
		t.Errorf("kid offered %q, want only German HD", got)
	}
	if got := strings.Join(offered("other"), ","); got != "Simple ranking,German HD,English HD" {
		t.Errorf("profile without a list offered %q", got)
	}
	// The profile picks its own default among its presets only.
	if rec := assign(`{"userId":"kid","seriesPreset":"`+b+`"}`, false); rec.Code != http.StatusForbidden {
		t.Errorf("a preset outside the profile's list must be refused: %d", rec.Code)
	}
	if rec := assign(`{"userId":"kid","seriesPreset":"`+a+`"}`, false); rec.Code != http.StatusOK {
		t.Fatalf("own default: %d %s", rec.Code, rec.Body.String())
	}
	us, _ := h.UserSettings.Get("kid")
	if us.QualityPresets.PickerPresets == nil || len(*us.QualityPresets.PickerPresets) != 1 {
		t.Errorf("changing the default must keep the picker list: %+v", us.QualityPresets)
	}
	// null restores every offered preset.
	if rec := assign(`{"userId":"kid","seriesPreset":"`+a+`","pickerPresets":null}`, true); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	if got := len(offered("kid")); got != 3 {
		t.Errorf("after reset kid is offered %d presets, want 3", got)
	}
}

// Slots a request leaves out are kept, unknown fields are rejected, and a
// choice the administrator made may be sent back unchanged by the profile.
func TestQualityPresetAssignKeepsAndValidates(t *testing.T) {
	h, _ := newPresetHandler(t)
	h.ProfileOwner = func(string) string { return "acc-1" }
	a := importPreset(t, h, "German HD", qualityprofiles.AudienceAll)
	assign := func(body string, master bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Assign(rec, presetRequest(http.MethodPut, "/x", body, master, "acc-1", nil))
		return rec
	}
	// The administrator assigns a preset the profile is not offered.
	if rec := assign(`{"userId":"kid","seriesPreset":"`+a+`","moviePreset":"simple"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("admin assign: %d %s", rec.Code, rec.Body.String())
	}
	settings, _ := h.Config.Load()
	settings.QualityPresets.PickerPresets = []string{config.QualityPresetSimple}
	if err := h.Config.Save(settings); err != nil {
		t.Fatal(err)
	}
	// The page sends every slot; the unchanged series preset must not block
	// changing the movie preset.
	if rec := assign(`{"userId":"kid","seriesPreset":"`+a+`","moviePreset":"inherit"}`, false); rec.Code != http.StatusOK {
		t.Fatalf("unchanged slot blocked the change: %d %s", rec.Code, rec.Body.String())
	}
	if rec := assign(`{"userId":"kid","animeSeriesPreset":"simple"}`, false); rec.Code != http.StatusOK {
		t.Fatalf("anime only: %d %s", rec.Code, rec.Body.String())
	}
	us, _ := h.UserSettings.Get("kid")
	q := us.QualityPresets
	if q.SeriesPreset == nil || *q.SeriesPreset != a || q.MoviePreset != nil || q.AnimeSeriesPreset == nil {
		t.Errorf("left-out slots must be kept: %+v", q)
	}
	if rec := assign(`{"userId":"kid","seriesPresets":"simple"}`, false); rec.Code != http.StatusBadRequest {
		t.Errorf("a misspelled field must be rejected: %d", rec.Code)
	}

	rec := httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"seriesPreset":"`+a+`"}`, true, "acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("selection: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"allowRequestOverride":true}`, true, "acc-1", nil))
	if s, _ := h.Config.Load(); rec.Code != http.StatusOK || s.QualityPresets.SeriesPreset != a {
		t.Errorf("selection without seriesPreset must keep it: %d %q", rec.Code, s.QualityPresets.SeriesPreset)
	}
}

// The update check task appears with the first syncing preset, once.
func TestQualityPresetSyncTaskAddedOnImport(t *testing.T) {
	h, _ := newPresetHandler(t)
	count := func() int {
		s, _ := h.Config.Load()
		n := 0
		for _, task := range s.ScheduledTasks.Tasks {
			if task.Type == config.ScheduledTaskTypeQualityPresetSync {
				n++
			}
		}
		return n
	}
	if count() != 0 {
		t.Fatal("no task before any preset")
	}
	h.ensureSyncTask()
	h.ensureSyncTask()
	if count() != 1 {
		t.Errorf("tasks = %d, want 1", count())
	}
}

// The app list looks a title's remembered preset up under the prequeue's
// canonical ID, whichever form of the ID the app sends.
func TestClientListUsesCanonicalTitleID(t *testing.T) {
	h, _ := newPresetHandler(t)
	var titles []string
	h.Select = func(mediaType, userID, clientID, titleID string, _ bool) (string, string) {
		titles = append(titles, mediaType+"="+titleID)
		return "", "none"
	}
	rec := httptest.NewRecorder()
	h.ClientList(rec, presetRequest(http.MethodGet, "/x?mediaType=series&titleId=tvdb:series:336522&tmdbId=74577&name=Show", "", false, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Join(titles, ",") != "movie=,series=tmdb:tv:74577,movie=,series=" {
		t.Fatalf("selections asked for %q", titles)
	}
}
