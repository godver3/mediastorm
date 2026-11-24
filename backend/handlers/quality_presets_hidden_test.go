package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/services/qualityprofiles"
)

// A profile hides offered presets from its own pickers: it can narrow, but
// never widen, what the administrator offers it, and a hidden preset counts
// as not offered (not pickable, not a default).
func TestProfileHiddenPresets(t *testing.T) {
	h, _ := newPresetHandler(t)
	h.ProfileOwner = func(string) string { return "acc-1" }
	a := importPreset(t, h, "German HD", qualityprofiles.AudienceAll)
	b := importPreset(t, h, "English HD", qualityprofiles.AudienceAll)
	c := importPreset(t, h, "German UHD", qualityprofiles.AudienceAll)
	rec := httptest.NewRecorder()
	h.Selection(rec, presetRequest(http.MethodPut, "/x", `{"moviePreset":"","seriesPreset":"","pickerPresets":["`+a+`","`+b+`","`+c+`"]}`, true, "acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("selection: %d", rec.Code)
	}
	assign := func(body string, master bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Assign(rec, presetRequest(http.MethodPut, "/x", body, master, "acc-1", nil))
		return rec
	}
	offered := func(user string) string {
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
		return strings.Join(names, ",")
	}

	// The administrator offers kid two of the three; kid hides one of those.
	if rec := assign(`{"userId":"kid","pickerPresets":["`+a+`","`+b+`"]}`, true); rec.Code != http.StatusOK {
		t.Fatalf("admin list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := assign(`{"userId":"kid","hiddenPresets":["`+b+`"]}`, false); rec.Code != http.StatusOK {
		t.Fatalf("profile hides a preset: %d %s", rec.Code, rec.Body.String())
	}
	if got := offered("kid"); got != "German HD" {
		t.Errorf("kid offered %q, want German HD", got)
	}
	// Hiding never widens: c stays off although kid did not hide it.
	us, _ := h.UserSettings.Get("kid")
	if us.QualityPresets.PickerPresets == nil || len(*us.QualityPresets.PickerPresets) != 2 {
		t.Errorf("hiding changed the administrator's list: %+v", us.QualityPresets)
	}
	// A hidden preset is not a default the profile can choose.
	if rec := assign(`{"userId":"kid","seriesPreset":"`+b+`"}`, false); rec.Code != http.StatusForbidden {
		t.Errorf("a hidden preset was accepted as default: %d", rec.Code)
	}
	// Changing the defaults keeps the hidden list; null shows everything again.
	if rec := assign(`{"userId":"kid","seriesPreset":"`+a+`"}`, false); rec.Code != http.StatusOK {
		t.Fatalf("own default: %d %s", rec.Code, rec.Body.String())
	}
	if us, _ = h.UserSettings.Get("kid"); us.QualityPresets.HiddenPresets == nil {
		t.Errorf("changing a default dropped the hidden list: %+v", us.QualityPresets)
	}
	if rec := assign(`{"userId":"kid","hiddenPresets":null}`, false); rec.Code != http.StatusOK {
		t.Fatalf("show all: %d", rec.Code)
	}
	if got := offered("kid"); got != "German HD,English HD" {
		t.Errorf("after showing all kid is offered %q", got)
	}
	// Another profile is untouched; unknown presets and devices are refused.
	if got := offered("other"); got != "German HD,English HD,German UHD" {
		t.Errorf("other offered %q", got)
	}
	if rec := assign(`{"userId":"kid","hiddenPresets":["no-such-preset"]}`, false); rec.Code == http.StatusOK {
		t.Error("an unknown preset was accepted")
	}
	if rec := assign(`{"userId":"kid","clientId":"tv","hiddenPresets":["`+a+`"]}`, false); rec.Code == http.StatusOK {
		t.Error("a device-level hidden list was accepted")
	}
}

// The administrator hides offered presets by default; a profile shows one
// for itself, and following the recommendation again drops its choice.
func TestPresetsHiddenByDefault(t *testing.T) {
	h, _ := newPresetHandler(t)
	h.ProfileOwner = func(string) string { return "acc-1" }
	a := importPreset(t, h, "German HD", qualityprofiles.AudienceAll)
	b := importPreset(t, h, "German UHD", qualityprofiles.AudienceAll)
	selection := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Selection(rec, presetRequest(http.MethodPut, "/x", body, true, "acc-1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("selection %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	assign := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Assign(rec, presetRequest(http.MethodPut, "/x", body, false, "acc-1", nil))
		return rec
	}
	offered := func(user string) string {
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
		return strings.Join(names, ",")
	}

	selection(`{"pickerPresets":["` + a + `","` + b + `"],"hiddenByDefault":["` + b + `","not-offered"]}`)
	settings, _ := h.Config.Load()
	if got := strings.Join(settings.QualityPresets.HiddenByDefault, ","); got != b {
		t.Fatalf("hidden by default = %q, want only the offered UHD preset", got)
	}
	if got := offered("kid"); got != "German HD" {
		t.Errorf("kid offered %q, want German HD (UHD hidden by default)", got)
	}
	// Hidden by default: not a default the profile can choose.
	if rec := assign(`{"userId":"kid","seriesPreset":"` + b + `"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a preset hidden by default was accepted as default: %d", rec.Code)
	}
	// The profile shows it for itself.
	if rec := assign(`{"userId":"kid","shownPresets":["` + b + `"]}`); rec.Code != http.StatusOK {
		t.Fatalf("show: %d %s", rec.Code, rec.Body.String())
	}
	if got := offered("kid"); got != "German HD,German UHD" {
		t.Errorf("after showing kid is offered %q", got)
	}
	if got := offered("other"); got != "German HD" {
		t.Errorf("another profile is offered %q", got)
	}
	// Its choice outlives a change of the recommendation...
	selection(`{"hiddenByDefault":[]}`)
	selection(`{"hiddenByDefault":["` + b + `"]}`)
	if got := offered("kid"); got != "German HD,German UHD" {
		t.Errorf("kid's own choice did not survive: %q", got)
	}
	// ...until it follows the recommendation again.
	if rec := assign(`{"userId":"kid","shownPresets":null,"hiddenPresets":null}`); rec.Code != http.StatusOK {
		t.Fatalf("follow: %d", rec.Code)
	}
	if got := offered("kid"); got != "German HD" {
		t.Errorf("after following the recommendation kid is offered %q", got)
	}
	// No longer offering a preset drops it from the defaults too.
	selection(`{"pickerPresets":["` + a + `"]}`)
	if settings, _ = h.Config.Load(); len(settings.QualityPresets.HiddenByDefault) != 0 {
		t.Errorf("a preset no longer offered is still hidden by default: %v", settings.QualityPresets.HiddenByDefault)
	}
}
