package trash

import (
	"fmt"
	"sort"
	"strings"

	"novastream/internal/arr"
)

// GroupChoice records which optional custom formats of a CF group are used.
type GroupChoice struct {
	Enabled bool `json:"enabled"`
	// Formats overrides the default selection of non-required formats by
	// trash_id (true = use, false = skip). Required formats are always used
	// while the group is enabled.
	Formats map[string]bool `json:"formats,omitempty"`
}

// BuildOptions customize how a TRaSH profile is turned into a profile.
type BuildOptions struct {
	// ScoreSet overrides the profile's trash_score_set.
	ScoreSet string
	// Groups overrides the default enablement of CF groups by trash_id.
	Groups map[string]GroupChoice
	// QualitySize picks a quality-size file ("series", "anime", "movie",
	// "sqp-streaming", ...). Empty chooses by profile.
	QualitySize string
}

// GroupState describes a CF group as offered for a profile.
type GroupState struct {
	TrashID     string             `json:"trashId"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Default     bool               `json:"default"`
	Enabled     bool               `json:"enabled"`
	Formats     []GroupFormatState `json:"formats"`
}

// GroupFormatState is one format within a group.
type GroupFormatState struct {
	TrashID  string `json:"trashId"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Default  bool   `json:"default"`
	Selected bool   `json:"selected"`
	Score    int    `json:"score"`
}

// Built is the result of converting a TRaSH profile.
type Built struct {
	Profile  arr.Profile        `json:"profile"`
	Formats  []arr.CustomFormat `json:"formats"`
	Groups   []GroupState       `json:"groups"`
	ScoreSet string             `json:"scoreSet"`
	// Conflicts lists enabled formats TRaSH marks as mutually exclusive.
	Conflicts [][]string `json:"conflicts,omitempty"`
	// Missing lists formats referenced by the profile or its groups that the
	// snapshot does not contain.
	Missing []string `json:"missing,omitempty"`
	// Invalid lists scored formats this engine cannot compile (an unknown
	// condition type from a newer *arr, an invalid regex, ...) as
	// "name (trash_id, score N): reason". arr.NewEvaluator leaves them out
	// (and returns the same errors), so they never add their score: a
	// negative-score blocker among them stops blocking. Callers should
	// refuse or at least warn when this is not empty.
	Invalid []string `json:"invalid,omitempty"`
}

func defaultQualitySize(app arr.App, qp QualityProfile, sizes map[string]QualitySize) string {
	name := strings.ToLower(qp.Name + " " + qp.TrashScoreSet)
	if strings.Contains(name, "anime") {
		if _, ok := sizes["anime"]; ok {
			return "anime"
		}
	}
	if strings.Contains(name, "sqp") {
		if strings.Contains(name, "2160") || strings.Contains(name, "uhd") {
			if _, ok := sizes["sqp-uhd"]; ok {
				return "sqp-uhd"
			}
		}
		if _, ok := sizes["sqp-streaming"]; ok {
			return "sqp-streaming"
		}
	}
	if app == arr.AppSonarr {
		return "series"
	}
	return "movie"
}

// BuildProfile converts a TRaSH quality profile, its own formats and the
// selected CF groups into an engine profile.
func (s *Snapshot) BuildProfile(app arr.App, profileTrashID string, opts BuildOptions) (*Built, error) {
	data := s.App(app)
	qp, ok := data.Profiles[profileTrashID]
	if !ok {
		return nil, fmt.Errorf("trash: %s profile %q not found", app, profileTrashID)
	}
	scoreSet := opts.ScoreSet
	if scoreSet == "" {
		scoreSet = qp.TrashScoreSet
	}
	built := &Built{ScoreSet: scoreSet}
	profile := arr.Profile{
		App:                   app,
		Name:                  qp.Name,
		Cutoff:                qp.Cutoff,
		UpgradeAllowed:        bool(qp.UpgradeAllowed),
		MinFormatScore:        qp.MinFormatScore,
		CutoffFormatScore:     qp.CutoffFormatScore,
		MinUpgradeFormatScore: qp.MinUpgradeFormatScore,
		FormatScores:          map[string]int{},
		Propers:               arr.PropersDoNotPrefer,
	}
	if qp.Language != "" {
		profile.Language = arr.Language(qp.Language)
	}
	for _, item := range qp.Items {
		profile.Items = append(profile.Items, arr.ProfileItem{Name: item.Name, Allowed: bool(item.Allowed), Qualities: append([]string(nil), item.Items...)})
	}

	used := map[string]arr.CustomFormat{}
	addFormat := func(id string, name string) {
		cf, ok := data.CustomFormats[id]
		if !ok {
			built.Missing = append(built.Missing, fmt.Sprintf("%s (%s)", name, id))
			return
		}
		used[id] = cf
		profile.FormatScores[id] = ScoreFor(cf, scoreSet)
	}
	names := make([]string, 0, len(qp.FormatItems))
	for name := range qp.FormatItems {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		addFormat(qp.FormatItems[name], name)
	}

	for _, g := range s.GroupsFor(app, profileTrashID) {
		choice, hasChoice := opts.Groups[g.TrashID]
		enabled := bool(g.Default)
		if hasChoice {
			enabled = choice.Enabled
		}
		state := GroupState{TrashID: g.TrashID, Name: g.Name, Description: g.TrashDescription, Default: bool(g.Default), Enabled: enabled}
		for _, f := range g.CustomFormats {
			def := bool(f.Required) || (f.Default != nil && bool(*f.Default))
			selected := def
			if hasChoice && choice.Formats != nil && !bool(f.Required) {
				if v, ok := choice.Formats[f.TrashID]; ok {
					selected = v
				}
			}
			score := 0
			if cf, ok := data.CustomFormats[f.TrashID]; ok {
				score = ScoreFor(cf, scoreSet)
			}
			state.Formats = append(state.Formats, GroupFormatState{TrashID: f.TrashID, Name: f.Name, Required: bool(f.Required), Default: def, Selected: selected, Score: score})
			if enabled && selected {
				if _, already := used[f.TrashID]; !already {
					addFormat(f.TrashID, f.Name)
				}
			}
		}
		built.Groups = append(built.Groups, state)
	}

	sizeType := opts.QualitySize
	if sizeType == "" {
		sizeType = defaultQualitySize(app, qp, data.QualitySizes)
	}
	if qs, ok := data.QualitySizes[sizeType]; ok {
		profile.SizeLimits = map[string]arr.SizeLimit{}
		for _, q := range qs.Qualities {
			profile.SizeLimits[q.Quality] = arr.SizeLimit{Min: q.Min, Preferred: q.Preferred, Max: q.Max}
		}
	}

	enabled := map[string]bool{}
	for id := range used {
		enabled[id] = true
	}
	built.Conflicts = s.ConflictsFor(app, enabled)
	for id, cf := range used {
		built.Formats = append(built.Formats, cf)
		if score := profile.FormatScores[id]; score != 0 {
			if _, err := arr.Compile(app, cf); err != nil {
				built.Invalid = append(built.Invalid, fmt.Sprintf("%s (%s, score %d): %v", cf.Name, id, score, err))
			}
		}
	}
	sort.Strings(built.Invalid)
	sort.Slice(built.Formats, func(i, j int) bool { return built.Formats[i].Name < built.Formats[j].Name })
	if err := arr.ValidateProfile(profile); err != nil {
		return nil, err
	}
	built.Profile = profile
	return built, nil
}
