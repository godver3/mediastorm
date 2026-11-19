package qualityprofiles

import (
	"fmt"
	"sort"

	"novastream/internal/arr"
	"novastream/internal/trash"
)

// FormatView describes one format of a resolved preset for display.
type FormatView struct {
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	Score         int      `json:"score"`
	UpstreamScore *int     `json:"upstreamScore,omitempty"`
	Origin        string   `json:"origin"` // profile, group, own
	Group         string   `json:"group,omitempty"`
	Disabled      bool     `json:"disabled,omitempty"`
	Edited        bool     `json:"edited,omitempty"`
	Review        bool     `json:"review,omitempty"`
	Unsupported   []string `json:"unsupported,omitempty"`
	// Source is the snapshot source an upstream format came from;
	// Shadowed lists other sources that define it with lower precedence.
	Source   string   `json:"source,omitempty"`
	Shadowed []string `json:"shadowed,omitempty"`
}

// GroupView describes a CF group of a resolved preset.
type GroupView struct {
	TrashID     string                   `json:"trashId"`
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	Default     bool                     `json:"default"`
	Enabled     bool                     `json:"enabled"`
	Formats     []trash.GroupFormatState `json:"formats"`
}

// Resolved is the effective preset: Upstream + Overrides.
type Resolved struct {
	Profile   arr.Profile        `json:"profile"`
	Formats   []arr.CustomFormat `json:"-"`
	Views     []FormatView       `json:"formats"`
	Groups    []GroupView        `json:"groups,omitempty"`
	Conflicts [][]string         `json:"trashConflicts,omitempty"`
}

func copyScores(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// groupSelection returns the enabled groups and selected format keys.
func groupSelection(up Upstream, ov Overrides) ([]GroupView, map[string]string) {
	selected := map[string]string{} // format key -> group name
	var views []GroupView
	for _, g := range up.Groups {
		choice, has := ov.Groups[g.TrashID]
		enabled := bool(g.Default)
		if has {
			enabled = choice.Enabled
		}
		view := GroupView{TrashID: g.TrashID, Name: g.Name, Description: g.TrashDescription, Default: bool(g.Default), Enabled: enabled}
		for _, f := range g.CustomFormats {
			def := bool(f.Required) || (f.Default != nil && bool(*f.Default))
			sel := def
			if has && choice.Formats != nil && !bool(f.Required) {
				if v, ok := choice.Formats[f.TrashID]; ok {
					sel = v
				}
			}
			score := 0
			if cf, ok := up.Formats[f.TrashID]; ok {
				score = trash.ScoreFor(cf, up.ScoreSet)
			}
			view.Formats = append(view.Formats, trash.GroupFormatState{TrashID: f.TrashID, Name: f.Name, Required: bool(f.Required), Default: def, Selected: sel, Score: score})
			if enabled && sel {
				if _, dup := selected[f.TrashID]; !dup {
					selected[f.TrashID] = g.Name
				}
			}
		}
		views = append(views, view)
	}
	return views, selected
}

// Resolve computes the effective profile and formats of a preset.
func Resolve(p *Preset) (*Resolved, error) {
	up := p.Upstream
	ov := p.Overrides
	profile := up.Profile
	profile.App = p.App
	profile.Name = p.DisplayName()
	profile.Items = append([]arr.ProfileItem(nil), up.Profile.Items...)
	profile.FormatScores = copyScores(up.Profile.FormatScores)
	if up.Profile.SizeLimits != nil {
		profile.SizeLimits = make(map[string]arr.SizeLimit, len(up.Profile.SizeLimits))
		for k, v := range up.Profile.SizeLimits {
			profile.SizeLimits[k] = v
		}
	}
	origin := map[string]string{}
	for k := range profile.FormatScores {
		origin[k] = "profile"
	}

	groups, selected := groupSelection(up, ov)
	groupOf := map[string]string{}
	for key, group := range selected {
		if _, inProfile := profile.FormatScores[key]; inProfile {
			continue
		}
		if cf, ok := up.Formats[key]; ok {
			profile.FormatScores[key] = trash.ScoreFor(cf, up.ScoreSet)
			origin[key] = "group"
			groupOf[key] = group
		}
	}
	upstreamScores := copyScores(profile.FormatScores)

	pool := make(map[string]arr.CustomFormat, len(up.Formats)+len(ov.Formats))
	for k, cf := range up.Formats {
		pool[k] = cf
	}
	edited := map[string]bool{}
	for k, cf := range ov.Formats {
		if _, ok := pool[k]; ok {
			edited[k] = true
		} else {
			origin[k] = "own"
		}
		pool[k] = cf
	}
	for k, v := range ov.Scores {
		profile.FormatScores[k] = v
		if _, ok := origin[k]; !ok {
			origin[k] = "own"
		}
	}
	review := map[string]bool{}
	for _, k := range p.ReviewFormats {
		review[k] = true
	}
	disabled := map[string]bool{}
	for k, off := range ov.Disabled {
		if off {
			disabled[k] = true
		}
	}

	if ov.Items != nil {
		profile.Items = append([]arr.ProfileItem(nil), ov.Items...)
	}
	if ov.MinFormatScore != nil {
		profile.MinFormatScore = *ov.MinFormatScore
	}
	if ov.Language != nil {
		profile.Language = *ov.Language
	}
	if ov.Propers != nil {
		profile.Propers = *ov.Propers
	}
	if ov.EnforceSizeLimits != nil {
		profile.EnforceSizeLimits = *ov.EnforceSizeLimits
	}
	if len(ov.SizeLimits) > 0 {
		if profile.SizeLimits == nil {
			profile.SizeLimits = map[string]arr.SizeLimit{}
		}
		for k, v := range ov.SizeLimits {
			profile.SizeLimits[k] = v
		}
	}
	if ov.PreferredProtocol != nil {
		profile.PreferredProtocol = *ov.PreferredProtocol
	}
	if ov.MultiLanguages != nil {
		profile.MultiLanguages = append([]arr.Language(nil), ov.MultiLanguages...)
	}

	res := &Resolved{Groups: groups}
	effective := map[string]int{}
	for key, score := range profile.FormatScores {
		cf, ok := pool[key]
		if !ok {
			continue
		}
		view := FormatView{Key: key, Name: cf.Name, Score: score, Origin: origin[key], Group: groupOf[key], Edited: edited[key]}
		if origin[key] != "own" {
			view.Source, view.Shadowed = p.Upstream.FormatSources[key], p.Upstream.Shadowed[key]
		}
		if v, ok := upstreamScores[key]; ok {
			v := v
			view.UpstreamScore = &v
		}
		if disabled[key] || review[key] {
			view.Disabled = disabled[key]
			view.Review = review[key]
			res.Views = append(res.Views, view)
			continue
		}
		effective[key] = score
		res.Formats = append(res.Formats, cf)
		res.Views = append(res.Views, view)
	}
	profile.FormatScores = effective
	sort.Slice(res.Views, func(i, j int) bool {
		if res.Views[i].Score != res.Views[j].Score {
			return res.Views[i].Score > res.Views[j].Score
		}
		return res.Views[i].Name < res.Views[j].Name
	})
	sort.Slice(res.Formats, func(i, j int) bool { return res.Formats[i].Name < res.Formats[j].Name })
	if err := arr.ValidateProfile(profile); err != nil {
		return nil, fmt.Errorf("preset %q: %w", p.DisplayName(), err)
	}
	enabled := map[string]bool{}
	for k := range effective {
		enabled[k] = true
	}
	for _, c := range up.ConflictSets {
		var hit []string
		for id, name := range c.Formats {
			if enabled[id] {
				hit = append(hit, name)
			}
		}
		if len(hit) > 1 {
			sort.Strings(hit)
			res.Conflicts = append(res.Conflicts, hit)
		}
	}
	res.Profile = profile
	return res, nil
}

// Evaluator compiles a resolved preset. Compile errors (for example an
// invalid regex in an own format) are returned alongside a usable evaluator
// that skips the broken formats.
func (r *Resolved) Evaluator() (*arr.Evaluator, []error) {
	ev, errs := arr.NewEvaluator(r.Profile, r.Formats)
	if ev != nil {
		for i := range r.Views {
			if u, ok := ev.Unsupported[r.Views[i].Name]; ok {
				r.Views[i].Unsupported = u
			}
		}
	}
	return ev, errs
}
