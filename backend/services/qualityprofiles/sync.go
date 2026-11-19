package qualityprofiles

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/internal/arr"
	"novastream/internal/trash"
)

// Plan is the outcome of comparing a preset with a fetched upstream state.
type Plan struct {
	Next       Upstream        `json:"-"`
	FromCommit string          `json:"fromCommit"`
	ToCommit   string          `json:"toCommit"`
	Changes    []Change        `json:"changes"`
	Conflicts  []Conflict      `json:"conflicts,omitempty"`
	NewFormats []string        `json:"newFormats,omitempty"`
	Removed    []RemovedFormat `json:"removed,omitempty"`
}

// Empty reports whether applying the plan changes nothing for the preset:
// no upstream change and no new conflict. The commit may still differ (the
// source moved on in files the preset does not use).
func (p *Plan) Empty() bool {
	return len(p.Changes) == 0 && len(p.Conflicts) == 0
}

func jsonString(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func copyFormat(cf arr.CustomFormat) arr.CustomFormat {
	var out arr.CustomFormat
	_ = json.Unmarshal([]byte(jsonString(cf)), &out)
	return out
}

func specsJSON(cf arr.CustomFormat) string { return jsonString(cf.Specifications) }

func (s SyncSettings) followConditions(key string) bool {
	if pf, ok := s.PerFormat[key]; ok && pf.Conditions != nil {
		return *pf.Conditions
	}
	return s.Follow.Conditions
}

func (s SyncSettings) followScores(key string) bool {
	if pf, ok := s.PerFormat[key]; ok && pf.Scores != nil {
		return *pf.Scores
	}
	return s.Follow.Scores
}

// takeSource copies a format's source attribution from another upstream.
func (u *Upstream) takeSource(from Upstream, key string) {
	if src, ok := from.FormatSources[key]; ok {
		if u.FormatSources == nil {
			u.FormatSources = map[string]string{}
		}
		u.FormatSources[key] = src
	}
	if shadowed := from.Shadowed[key]; len(shadowed) > 0 {
		if u.Shadowed == nil {
			u.Shadowed = map[string][]string{}
		}
		u.Shadowed[key] = append([]string(nil), shadowed...)
	}
}

// mergeUpstream combines the current accepted upstream with a fetched one
// according to the preset's follow policies.
func mergeUpstream(cur, fetched Upstream, s SyncSettings) Upstream {
	next := Upstream{
		Commit:       fetched.Commit,
		FetchedAt:    fetched.FetchedAt,
		Sources:      fetched.Sources,
		ConflictSets: fetched.ConflictSets,
		Formats:      map[string]arr.CustomFormat{},
	}
	if s.Follow.Profile {
		next.Profile = fetched.Profile
		next.ScoreSet = fetched.ScoreSet
	} else {
		next.Profile = cur.Profile
		next.ScoreSet = cur.ScoreSet
	}
	next.Profile.FormatScores = map[string]int{}
	if s.Follow.Sizes {
		next.Profile.SizeLimits = fetched.Profile.SizeLimits
	} else {
		next.Profile.SizeLimits = cur.Profile.SizeLimits
	}
	if s.Follow.Groups {
		next.Groups = fetched.Groups
	} else {
		next.Groups = cur.Groups
	}

	keys := map[string]bool{}
	for k := range cur.Formats {
		keys[k] = true
	}
	for k := range fetched.Formats {
		keys[k] = true
	}
	for key := range keys {
		curCF, inCur := cur.Formats[key]
		newCF, inNew := fetched.Formats[key]
		followC, followS := s.followConditions(key), s.followScores(key)
		switch {
		case inCur && inNew:
			var cf arr.CustomFormat
			if followC {
				cf = copyFormat(newCF)
				next.takeSource(fetched, key)
			} else {
				cf = copyFormat(curCF)
				next.takeSource(cur, key)
			}
			if followS {
				cf.TrashScores = newCF.TrashScores
			} else {
				cf.TrashScores = curCF.TrashScores
			}
			next.Formats[key] = cf
		case inNew:
			next.Formats[key] = copyFormat(newCF)
			next.takeSource(fetched, key)
		case inCur && !followC:
			// Removed upstream but frozen by policy: keep it.
			next.Formats[key] = copyFormat(curCF)
			next.takeSource(cur, key)
		}
	}

	// Base profile scores: follow per key; keys only the current state has
	// survive when scores or conditions are frozen for them.
	scoreKeys := map[string]bool{}
	for k := range cur.Profile.FormatScores {
		scoreKeys[k] = true
	}
	for k := range fetched.Profile.FormatScores {
		scoreKeys[k] = true
	}
	for key := range scoreKeys {
		if _, ok := next.Formats[key]; !ok {
			continue
		}
		curV, inCur := cur.Profile.FormatScores[key]
		newV, inNew := fetched.Profile.FormatScores[key]
		switch {
		case s.followScores(key) && inNew:
			next.Profile.FormatScores[key] = newV
		case !s.followScores(key) && inCur:
			next.Profile.FormatScores[key] = curV
		case inNew && !inCur:
			next.Profile.FormatScores[key] = newV
		case inCur && !inNew && !s.followConditions(key):
			next.Profile.FormatScores[key] = curV
		}
	}
	return next
}

// offeredFormats are the formats a state scores for this preset: the
// profile's own formats plus the selected formats of the groups that are
// on (by default or because the user switched them on).
func offeredFormats(up Upstream, ov Overrides) map[string]bool {
	out := map[string]bool{}
	for k := range up.Profile.FormatScores {
		out[k] = true
	}
	_, selected := groupSelection(up, ov)
	for k := range selected {
		out[k] = true
	}
	return out
}

// upstreamScore is the score a format gets from the upstream layer alone.
func upstreamScore(up Upstream, key string) (int, bool) {
	if v, ok := up.Profile.FormatScores[key]; ok {
		return v, true
	}
	cf, ok := up.Formats[key]
	if !ok {
		return 0, false
	}
	return trash.ScoreFor(cf, up.ScoreSet), true
}

// upstreamValue renders the upstream value behind an override path.
func upstreamValue(up Upstream, path string) string {
	kind, key, _ := strings.Cut(path, ":")
	switch kind {
	case "score":
		if v, ok := upstreamScore(up, key); ok {
			return strconv.Itoa(v)
		}
		return "(none)"
	case "format":
		if cf, ok := up.Formats[key]; ok {
			return specsJSON(cf)
		}
		return "(removed)"
	case "items":
		return jsonString(up.Profile.Items)
	case "minFormatScore":
		return strconv.Itoa(up.Profile.MinFormatScore)
	case "language":
		return string(up.Profile.Language)
	case "propers":
		return string(up.Profile.Propers)
	case "size":
		if l, ok := up.Profile.SizeLimits[key]; ok {
			return jsonString(l)
		}
		return "(none)"
	case "group":
		for _, g := range up.Groups {
			if g.TrashID == key {
				return jsonString(g)
			}
		}
		return "(removed)"
	}
	return ""
}

// overrideValue renders the user's value for an override path.
func overrideValue(ov Overrides, path string) (string, bool) {
	kind, key, _ := strings.Cut(path, ":")
	switch kind {
	case "score":
		if v, ok := ov.Scores[key]; ok {
			return strconv.Itoa(v), true
		}
	case "format":
		if cf, ok := ov.Formats[key]; ok {
			return specsJSON(cf), true
		}
	case "items":
		if ov.Items != nil {
			return jsonString(ov.Items), true
		}
	case "minFormatScore":
		if ov.MinFormatScore != nil {
			return strconv.Itoa(*ov.MinFormatScore), true
		}
	case "language":
		if ov.Language != nil {
			return string(*ov.Language), true
		}
	case "propers":
		if ov.Propers != nil {
			return string(*ov.Propers), true
		}
	case "size":
		if l, ok := ov.SizeLimits[key]; ok {
			return jsonString(l), true
		}
	case "group":
		if c, ok := ov.Groups[key]; ok {
			return jsonString(c), true
		}
	}
	return "", false
}

func formatName(up Upstream, ov Overrides, key string) string {
	if cf, ok := ov.Formats[key]; ok {
		return cf.Name
	}
	if cf, ok := up.Formats[key]; ok {
		return cf.Name
	}
	for _, g := range up.Groups {
		if g.TrashID == key {
			return g.Name
		}
	}
	return key
}

// diffUpstream lists the changes between two upstream states.
func diffUpstream(from, to Upstream) []Change {
	var out []Change
	if jsonString(from.Profile.Items) != jsonString(to.Profile.Items) {
		out = append(out, Change{Kind: "changed", Part: "profile", Name: "qualities", Detail: "quality order or allowed qualities changed"})
	}
	if from.Profile.MinFormatScore != to.Profile.MinFormatScore {
		out = append(out, Change{Kind: "changed", Part: "profile", Name: "minimum score", Detail: fmt.Sprintf("%d → %d", from.Profile.MinFormatScore, to.Profile.MinFormatScore)})
	}
	if from.Profile.Language != to.Profile.Language {
		out = append(out, Change{Kind: "changed", Part: "profile", Name: "language", Detail: fmt.Sprintf("%s → %s", from.Profile.Language, to.Profile.Language)})
	}
	if from.ScoreSet != to.ScoreSet {
		out = append(out, Change{Kind: "changed", Part: "profile", Name: "score set", Detail: fmt.Sprintf("%s → %s", from.ScoreSet, to.ScoreSet)})
	}
	if jsonString(profileOptions(from.Profile)) != jsonString(profileOptions(to.Profile)) {
		out = append(out, Change{Kind: "changed", Part: "profile", Name: "options", Detail: "cutoff, upgrades, propers or other profile options changed"})
	}
	keys := map[string]bool{}
	for k := range from.Formats {
		keys[k] = true
	}
	for k := range to.Formats {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		a, inA := from.Formats[k]
		b, inB := to.Formats[k]
		switch {
		case !inA:
			out = append(out, Change{Kind: "added", Part: "format", Key: k, Name: b.Name})
		case !inB:
			out = append(out, Change{Kind: "removed", Part: "format", Key: k, Name: a.Name})
		default:
			if specsJSON(a) != specsJSON(b) {
				out = append(out, Change{Kind: "changed", Part: "format", Key: k, Name: b.Name, Detail: "conditions changed"})
			}
			sa, _ := upstreamScore(from, k)
			sb, _ := upstreamScore(to, k)
			if sa != sb {
				out = append(out, Change{Kind: "changed", Part: "score", Key: k, Name: b.Name, Detail: fmt.Sprintf("%d → %d", sa, sb)})
			}
		}
	}
	fromGroups := map[string]trash.CFGroup{}
	for _, g := range from.Groups {
		fromGroups[g.TrashID] = g
	}
	toGroups := map[string]bool{}
	for _, g := range to.Groups {
		toGroups[g.TrashID] = true
		old, ok := fromGroups[g.TrashID]
		switch {
		case !ok:
			out = append(out, Change{Kind: "added", Part: "group", Key: g.TrashID, Name: g.Name})
		case jsonString(old) != jsonString(g):
			out = append(out, Change{Kind: "changed", Part: "group", Key: g.TrashID, Name: g.Name, Detail: "members or defaults changed"})
		}
	}
	for id, g := range fromGroups {
		if !toGroups[id] {
			out = append(out, Change{Kind: "removed", Part: "group", Key: id, Name: g.Name})
		}
	}
	if jsonString(from.Profile.SizeLimits) != jsonString(to.Profile.SizeLimits) {
		out = append(out, Change{Kind: "changed", Part: "size", Name: "quality sizes"})
	}
	return out
}

// profileOptions is a profile without the parts diffUpstream reports on
// their own.
func profileOptions(p arr.Profile) arr.Profile {
	p.Items, p.MinFormatScore, p.Language, p.FormatScores, p.SizeLimits = nil, 0, "", nil, nil
	return p
}

// PlanUpdate compares a preset with a fetched upstream state. Conflicts
// already reported for the same upstream value are not reported again.
func PlanUpdate(p *Preset, fetched Upstream, now time.Time) *Plan {
	next := mergeUpstream(p.Upstream, fetched, p.Sync)
	plan := &Plan{Next: next, FromCommit: p.Upstream.Commit, ToCommit: next.Commit, Changes: diffUpstream(p.Upstream, next)}
	reported := map[string]string{}
	for _, c := range p.Conflicts {
		reported[c.Path] = c.UpstreamAfter
	}

	paths := make([]string, 0, len(p.Overrides.Base))
	for path := range p.Overrides.Base {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		yours, has := overrideValue(p.Overrides, path)
		if !has {
			continue
		}
		before := p.Overrides.Base[path]
		after := upstreamValue(next, path)
		if prev, ok := reported[path]; ok && prev == after {
			continue
		}
		if after != before && after != yours {
			kind, key, _ := strings.Cut(path, ":")
			plan.Conflicts = append(plan.Conflicts, Conflict{
				Path: path, Format: formatName(next, p.Overrides, key), Field: kind,
				UpstreamBefore: before, UpstreamAfter: after, Yours: yours, Commit: next.Commit, DetectedAt: now,
			})
		}
	}

	before, after := offeredFormats(p.Upstream, p.Overrides), offeredFormats(next, p.Overrides)
	for k := range after {
		if !before[k] {
			if _, existed := p.Upstream.Formats[k]; !existed {
				plan.NewFormats = append(plan.NewFormats, k)
			}
		}
	}
	sort.Strings(plan.NewFormats)
	for k, cf := range p.Upstream.Formats {
		if _, ok := next.Formats[k]; ok {
			continue
		}
		_, scored := p.Overrides.Scores[k]
		_, edited := p.Overrides.Formats[k]
		plan.Removed = append(plan.Removed, RemovedFormat{Key: k, Name: cf.Name, Commit: next.Commit, RemovedAt: now, Kept: scored || edited})
	}
	sort.Slice(plan.Removed, func(i, j int) bool { return plan.Removed[i].Name < plan.Removed[j].Name })
	return plan
}

// ApplyPlan accepts a plan: the current state goes to history, the merged
// upstream becomes current, new formats follow the preset's policy and
// formats that were removed upstream but customised are kept as own
// formats. Conflicts are recorded; the user's values stay in effect.
func ApplyPlan(p *Preset, plan *Plan, note string, now time.Time) {
	pushHistory(p, note, now)
	for _, r := range plan.Removed {
		if !r.Kept {
			continue
		}
		if p.Overrides.Formats == nil {
			p.Overrides.Formats = map[string]arr.CustomFormat{}
		}
		if _, ok := p.Overrides.Formats[r.Key]; !ok {
			if cf, ok := p.Upstream.Formats[r.Key]; ok {
				p.Overrides.Formats[r.Key] = copyFormat(cf)
			}
		}
		if p.Overrides.Scores == nil {
			p.Overrides.Scores = map[string]int{}
		}
		if _, ok := p.Overrides.Scores[r.Key]; !ok {
			if v, ok := upstreamScore(p.Upstream, r.Key); ok {
				p.Overrides.Scores[r.Key] = v
			}
		}
	}
	p.Removed = append(p.Removed, plan.Removed...)
	if len(p.Removed) > 50 {
		p.Removed = p.Removed[len(p.Removed)-50:]
	}
	switch p.Sync.NewFormats {
	case NewFormatsDisable:
		for _, k := range plan.NewFormats {
			if p.Overrides.Disabled == nil {
				p.Overrides.Disabled = map[string]bool{}
			}
			if _, set := p.Overrides.Disabled[k]; !set {
				p.Overrides.Disabled[k] = true
			}
		}
	case NewFormatsReview:
		seen := map[string]bool{}
		for _, k := range p.ReviewFormats {
			seen[k] = true
		}
		for _, k := range plan.NewFormats {
			if !seen[k] {
				p.ReviewFormats = append(p.ReviewFormats, k)
			}
		}
	}
	byPath := map[string]int{}
	for i, c := range p.Conflicts {
		byPath[c.Path] = i
	}
	for _, c := range plan.Conflicts {
		if i, ok := byPath[c.Path]; ok {
			p.Conflicts[i] = c
		} else {
			p.Conflicts = append(p.Conflicts, c)
		}
	}
	p.Upstream = plan.Next
	p.Pending = nil
	p.UpdatedAt = now
}

func pushHistory(p *Preset, note string, now time.Time) {
	var ov Overrides
	_ = json.Unmarshal([]byte(jsonString(p.Overrides)), &ov)
	entry := HistoryEntry{ID: fmt.Sprintf("h%d", now.UnixNano()), AppliedAt: now, Commit: p.Upstream.Commit, Note: note, Upstream: p.Upstream, Overrides: ov,
		ReviewFormats: append([]string(nil), p.ReviewFormats...)}
	p.History = append([]HistoryEntry{entry}, p.History...)
	if len(p.History) > MaxHistory {
		p.History = p.History[:MaxHistory]
	}
}

// Rollback restores a history entry. The current state is kept in history
// so the rollback itself can be undone.
func Rollback(p *Preset, historyID string, now time.Time) error {
	for _, h := range p.History {
		if h.ID != historyID {
			continue
		}
		target := h
		pushHistory(p, "before rollback to "+short(target.Commit), now)
		p.Upstream = target.Upstream
		p.Overrides = target.Overrides
		p.ReviewFormats = append([]string(nil), target.ReviewFormats...)
		p.Pending = nil
		p.Conflicts = nil
		p.UpdatedAt = now
		return nil
	}
	return fmt.Errorf("history entry %q not found", historyID)
}

func short(commit string) string {
	if i := strings.LastIndex(commit, "@"); i >= 0 {
		commit = commit[i+1:]
	}
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// ConflictChoice resolves a reported conflict.
type ConflictChoice string

const (
	KeepYours    ConflictChoice = "yours"
	TakeUpstream ConflictChoice = "upstream"
	EditValue    ConflictChoice = "edit"
)

// ResolveConflict applies a choice to one conflict. For EditValue, value
// is the new override in the same JSON form the API uses for that field.
func ResolveConflict(p *Preset, path string, choice ConflictChoice, value json.RawMessage, now time.Time) error {
	idx := -1
	for i, c := range p.Conflicts {
		if c.Path == path {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("no conflict for %q", path)
	}
	switch choice {
	case KeepYours:
		recordBase(p, path, true)
	case TakeUpstream:
		clearOverride(p, path)
	case EditValue:
		if err := setOverride(p, path, value); err != nil {
			return err
		}
		recordBase(p, path, true)
	default:
		return fmt.Errorf("unknown choice %q", choice)
	}
	p.Conflicts = append(p.Conflicts[:idx], p.Conflicts[idx+1:]...)
	p.UpdatedAt = now
	return nil
}

// recordBase stores the current upstream value behind an override path.
// With force the stored base is replaced (acknowledging an upstream
// change); otherwise an existing base is kept.
func recordBase(p *Preset, path string, force bool) {
	if p.Overrides.Base == nil {
		p.Overrides.Base = map[string]string{}
	}
	if _, ok := p.Overrides.Base[path]; ok && !force {
		return
	}
	p.Overrides.Base[path] = upstreamValue(p.Upstream, path)
}

func clearOverride(p *Preset, path string) {
	kind, key, _ := strings.Cut(path, ":")
	ov := &p.Overrides
	switch kind {
	case "score":
		delete(ov.Scores, key)
	case "format":
		if _, upstream := p.Upstream.Formats[key]; upstream {
			delete(ov.Formats, key)
		}
	case "items":
		ov.Items = nil
	case "minFormatScore":
		ov.MinFormatScore = nil
	case "language":
		ov.Language = nil
	case "propers":
		ov.Propers = nil
	case "size":
		delete(ov.SizeLimits, key)
	case "group":
		delete(ov.Groups, key)
	}
	delete(ov.Base, path)
}

func setOverride(p *Preset, path string, value json.RawMessage) error {
	kind, key, _ := strings.Cut(path, ":")
	ov := &p.Overrides
	switch kind {
	case "score":
		var v int
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("score must be a number: %w", err)
		}
		if ov.Scores == nil {
			ov.Scores = map[string]int{}
		}
		ov.Scores[key] = v
	case "format":
		var cf arr.CustomFormat
		if err := json.Unmarshal(value, &cf); err != nil {
			return fmt.Errorf("invalid custom format: %w", err)
		}
		if _, err := arr.Compile(p.App, cf); err != nil {
			return err
		}
		if ov.Formats == nil {
			ov.Formats = map[string]arr.CustomFormat{}
		}
		ov.Formats[key] = cf
	case "items":
		var items []arr.ProfileItem
		if err := json.Unmarshal(value, &items); err != nil {
			return err
		}
		ov.Items = items
	case "minFormatScore":
		var v int
		if err := json.Unmarshal(value, &v); err != nil {
			return err
		}
		ov.MinFormatScore = &v
	case "language":
		var v arr.Language
		if err := json.Unmarshal(value, &v); err != nil {
			return err
		}
		ov.Language = &v
	case "propers":
		var v arr.PropersMode
		if err := json.Unmarshal(value, &v); err != nil {
			return err
		}
		ov.Propers = &v
	case "size":
		var l arr.SizeLimit
		if err := json.Unmarshal(value, &l); err != nil {
			return err
		}
		if ov.SizeLimits == nil {
			ov.SizeLimits = map[string]arr.SizeLimit{}
		}
		ov.SizeLimits[key] = l
	case "group":
		var c trash.GroupChoice
		if err := json.Unmarshal(value, &c); err != nil {
			return err
		}
		if ov.Groups == nil {
			ov.Groups = map[string]trash.GroupChoice{}
		}
		ov.Groups[key] = c
	default:
		return fmt.Errorf("unknown override path %q", path)
	}
	return nil
}
