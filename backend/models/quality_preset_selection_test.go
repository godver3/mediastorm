package models

import "testing"

func TestAllowsPickerWithHiddenPresets(t *testing.T) {
	list := func(ids ...string) *[]string { return &ids }
	cases := []struct {
		name string
		sel  *QualityPresetSelection
		id   string
		want bool
	}{
		{"no settings", nil, "a", true},
		{"no lists", &QualityPresetSelection{}, "a", true},
		{"hidden", &QualityPresetSelection{HiddenPresets: list("a")}, "a", false},
		{"hidden, other preset", &QualityPresetSelection{HiddenPresets: list("a")}, "b", true},
		{"hidden ignores case and spaces", &QualityPresetSelection{HiddenPresets: list(" Simple ")}, "simple", false},
		{"admin list allows", &QualityPresetSelection{PickerPresets: list("a", "b")}, "b", true},
		{"admin list refuses", &QualityPresetSelection{PickerPresets: list("a")}, "b", false},
		{"hidden beats admin list", &QualityPresetSelection{PickerPresets: list("a", "b"), HiddenPresets: list("b")}, "b", false},
	}
	for _, tc := range cases {
		if got := tc.sel.AllowsPicker(tc.id); got != tc.want {
			t.Errorf("%s: AllowsPicker(%q) = %v", tc.name, tc.id, got)
		}
	}
	if (&QualityPresetSelection{HiddenPresets: list("a")}).IsEmpty() {
		t.Error("a selection with only hidden presets counts as empty")
	}
}

// Presets the administrator hides by default count as hidden unless the
// profile shows them; the profile's own hide still wins.
func TestAllowsPickerDefault(t *testing.T) {
	list := func(ids ...string) *[]string { return &ids }
	def := []string{"uhd"}
	cases := []struct {
		name string
		sel  *QualityPresetSelection
		id   string
		want bool
	}{
		{"no settings, hidden by default", nil, "uhd", false},
		{"no settings, other preset", nil, "hd", true},
		{"profile shows it", &QualityPresetSelection{ShownPresets: list("uhd")}, "uhd", true},
		{"profile hides another", &QualityPresetSelection{HiddenPresets: list("hd")}, "hd", false},
		{"shown but not on the admin list", &QualityPresetSelection{PickerPresets: list("hd"), ShownPresets: list("uhd")}, "uhd", false},
		{"hidden beats shown", &QualityPresetSelection{ShownPresets: list("uhd"), HiddenPresets: list("uhd")}, "uhd", false},
	}
	for _, tc := range cases {
		if got := tc.sel.AllowsPickerDefault(def, tc.id); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if (&QualityPresetSelection{ShownPresets: list("uhd")}).IsEmpty() {
		t.Error("a selection with only shown presets counts as empty")
	}
}
