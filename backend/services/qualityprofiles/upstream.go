package qualityprofiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"novastream/internal/arr"
	"novastream/internal/arr/arrclient"
	"novastream/internal/trash"
)

// trashUpstream builds the upstream layer of a TRaSH profile. The warnings
// name formats the profile or its groups use that the snapshot lacks; they
// are left out.
func trashUpstream(snap *trash.Snapshot, app arr.App, origin Origin, now time.Time) (Upstream, string, []string, error) {
	data := snap.App(app)
	qp, ok := data.Profiles[origin.ProfileTrashID]
	if !ok {
		return Upstream{}, "", nil, fmt.Errorf("TRaSH %s profile %q not found in snapshot %s", app, origin.ProfileTrashID, snap.Commit)
	}
	built, err := snap.BuildProfile(app, origin.ProfileTrashID, trash.BuildOptions{ScoreSet: origin.ScoreSet, QualitySize: origin.QualitySize})
	if err != nil {
		return Upstream{}, "", nil, err
	}
	scoreSet := built.ScoreSet
	up := Upstream{
		Commit:       snap.Commit,
		FetchedAt:    now,
		Formats:      map[string]arr.CustomFormat{},
		ScoreSet:     scoreSet,
		ConflictSets: data.Conflicts,
	}
	up.Sources = snap.SourceNames()
	noteSource := func(id string) {
		src, shadowed := snap.FormatSource(app, id)
		if src == "" {
			return
		}
		if up.FormatSources == nil {
			up.FormatSources = map[string]string{}
		}
		up.FormatSources[id] = src
		if len(shadowed) > 0 {
			if up.Shadowed == nil {
				up.Shadowed = map[string][]string{}
			}
			up.Shadowed[id] = append([]string(nil), shadowed...)
		}
	}
	base := built.Profile
	// Only the formats the profile lists itself belong to the base scores;
	// group formats are added at resolve time from the user's selection.
	base.FormatScores = map[string]int{}
	for _, id := range qp.FormatItems {
		if cf, ok := data.CustomFormats[id]; ok {
			base.FormatScores[id] = trash.ScoreFor(cf, scoreSet)
			up.Formats[id] = cf
			noteSource(id)
		}
	}
	for _, g := range snap.GroupsFor(app, origin.ProfileTrashID) {
		up.Groups = append(up.Groups, g)
		for _, f := range g.CustomFormats {
			if cf, ok := data.CustomFormats[f.TrashID]; ok {
				up.Formats[f.TrashID] = cf
				noteSource(f.TrashID)
			}
		}
	}
	up.Profile = base
	var warnings []string
	if len(built.Invalid) > 0 {
		// A left-out blocker (negative score) no longer blocks: say so.
		warnings = append(warnings, fmt.Sprintf("%d scored custom format(s) cannot be evaluated by mediastorm and are left out: %s", len(built.Invalid), strings.Join(built.Invalid, ", ")))
	}
	if len(built.Missing) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d custom format(s) of the profile are missing from the snapshot and left out: %s", len(built.Missing), strings.Join(built.Missing, ", ")))
	}
	return up, qp.Name, warnings, nil
}

// arrUpstream reads a quality profile and its formats from an instance.
func arrUpstream(ctx context.Context, client *arrclient.Client, profileID int, now time.Time) (Upstream, string, arr.App, error) {
	status, err := client.SystemStatus(ctx)
	if err != nil {
		return Upstream{}, "", "", fmt.Errorf("connect: %w", err)
	}
	app, err := status.App()
	if err != nil {
		return Upstream{}, "", "", err
	}
	profiles, err := client.ListQualityProfiles(ctx)
	if err != nil {
		return Upstream{}, "", "", fmt.Errorf("quality profiles: %w", err)
	}
	var remote *arrclient.RemoteProfile
	for i := range profiles {
		if profiles[i].ID == profileID {
			remote = &profiles[i]
		}
	}
	if remote == nil {
		return Upstream{}, "", "", fmt.Errorf("quality profile %d not found on %s", profileID, status.AppName)
	}
	formats, err := client.ListCustomFormats(ctx)
	if err != nil {
		return Upstream{}, "", "", fmt.Errorf("custom formats: %w", err)
	}
	byRemoteID := map[int]arr.CustomFormat{}
	for _, f := range formats {
		byRemoteID[f.ID] = f.Format
	}
	// A failed call must not look like quality sizes removed upstream.
	defs, err := client.ListQualityDefinitions(ctx)
	if err != nil {
		return Upstream{}, "", "", fmt.Errorf("quality definitions: %w", err)
	}

	profile := arr.Profile{
		App:                   app,
		Name:                  remote.Name,
		UpgradeAllowed:        remote.UpgradeAllowed,
		MinFormatScore:        remote.MinFormatScore,
		CutoffFormatScore:     remote.CutoffFormatScore,
		MinUpgradeFormatScore: remote.MinUpgradeFormatScore,
		FormatScores:          map[string]int{},
		Propers:               arr.PropersDoNotPrefer,
	}
	if remote.Language != nil {
		if l, ok := arr.LanguageFromID(app, remote.Language.ID); ok {
			profile.Language = l
		}
	}
	// The API lists items from least to most preferred.
	for i := len(remote.Items) - 1; i >= 0; i-- {
		item := remote.Items[i]
		if item.Quality != nil {
			profile.Items = append(profile.Items, arr.ProfileItem{Name: item.Quality.Name, Allowed: item.Allowed})
			continue
		}
		group := arr.ProfileItem{Name: item.Name, Allowed: item.Allowed}
		for j := len(item.Items) - 1; j >= 0; j-- {
			if q := item.Items[j].Quality; q != nil {
				group.Qualities = append(group.Qualities, q.Name)
			}
		}
		if len(group.Qualities) > 0 {
			profile.Items = append(profile.Items, group)
		}
	}
	if len(defs) > 0 {
		profile.SizeLimits = map[string]arr.SizeLimit{}
		for _, d := range defs {
			l := arr.SizeLimit{Preferred: d.PreferredSize, Max: d.MaxSize}
			if d.MinSize != nil {
				l.Min = *d.MinSize
			}
			profile.SizeLimits[d.Quality.Name] = l
		}
	}
	up := Upstream{FetchedAt: now, Formats: map[string]arr.CustomFormat{}}
	for _, fi := range remote.FormatItems {
		cf, ok := byRemoteID[fi.Format]
		if !ok {
			continue
		}
		key := cf.Key()
		up.Formats[key] = cf
		if fi.Score != 0 {
			profile.FormatScores[key] = fi.Score
		}
	}
	up.Profile = profile
	up.Commit = contentHash(profile, up.Formats)
	up.Sources = []string{"arr:" + client.BaseURL}
	return up, remote.Name, app, nil
}

// contentHash identifies an upstream state that has no commit.
func contentHash(profile arr.Profile, formats map[string]arr.CustomFormat) string {
	keys := make([]string, 0, len(formats))
	for k := range formats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	_ = json.NewEncoder(h).Encode(profile)
	for _, k := range keys {
		_ = json.NewEncoder(h).Encode(formats[k])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// ImportDocument is the JSON accepted by the paste/upload importer. Any of
// these shapes is accepted:
//
//   - an exported mediastorm preset ({"app":..., "upstream":..., ...})
//   - {"app":"sonarr","profile":<TRaSH quality profile>,"customFormats":[...]}
//   - {"app":"sonarr","customFormats":[...]} or a bare array of formats
//     (creates a profile allowing every quality, scoring formats by their
//     TRaSH default score)
type ImportDocument struct {
	App           arr.App               `json:"app"`
	Name          string                `json:"name,omitempty"`
	Profile       *trash.QualityProfile `json:"profile,omitempty"`
	CustomFormats []arr.CustomFormat    `json:"customFormats,omitempty"`
	Upstream      *Upstream             `json:"upstream,omitempty"`
	Overrides     *Overrides            `json:"overrides,omitempty"`
	Sync          *SyncSettings         `json:"sync,omitempty"`
	Audience      Audience              `json:"audience,omitempty"`
}

// importAudience is the audience of pasted JSON: an exported preset's own,
// otherwise guessed from the profile name.
func importAudience(data []byte, profileName string) Audience {
	var doc struct {
		Audience *Audience `json:"audience"`
	}
	if json.Unmarshal(data, &doc) == nil && doc.Audience != nil && doc.Audience.Valid() {
		return *doc.Audience
	}
	return AudienceForProfileName(profileName)
}

// parseImport decodes pasted JSON into a manual preset's layers.
func parseImport(data []byte, fallbackApp arr.App, now time.Time) (Upstream, Overrides, string, arr.App, error) {
	trimmed := strings.TrimSpace(string(data))
	var doc ImportDocument
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &doc.CustomFormats); err != nil {
			return Upstream{}, Overrides{}, "", "", fmt.Errorf("custom format list: %w", err)
		}
	} else if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return Upstream{}, Overrides{}, "", "", fmt.Errorf("invalid JSON: %w", err)
	}
	app := doc.App
	if app == "" {
		app = fallbackApp
	}
	if !app.Valid() {
		return Upstream{}, Overrides{}, "", "", fmt.Errorf("app must be sonarr or radarr")
	}
	if doc.Upstream != nil {
		up := *doc.Upstream
		up.Profile.App = app
		ov := Overrides{}
		if doc.Overrides != nil {
			ov = *doc.Overrides
		}
		name := doc.Name
		if name == "" {
			name = up.Profile.Name
		}
		if up.Formats == nil {
			up.Formats = map[string]arr.CustomFormat{}
		}
		return up, ov, name, app, nil
	}
	up := Upstream{FetchedAt: now, Formats: map[string]arr.CustomFormat{}}
	for _, cf := range doc.CustomFormats {
		if _, err := arr.Compile(app, cf); err != nil {
			return Upstream{}, Overrides{}, "", "", err
		}
		up.Formats[cf.Key()] = cf
	}
	var profile arr.Profile
	name := doc.Name
	if doc.Profile != nil {
		qp := doc.Profile
		if name == "" {
			name = qp.Name
		}
		profile = arr.Profile{App: app, Name: qp.Name, Cutoff: qp.Cutoff, UpgradeAllowed: bool(qp.UpgradeAllowed), MinFormatScore: qp.MinFormatScore,
			CutoffFormatScore: qp.CutoffFormatScore, MinUpgradeFormatScore: qp.MinUpgradeFormatScore, FormatScores: map[string]int{}, Propers: arr.PropersDoNotPrefer}
		if qp.Language != "" {
			profile.Language = arr.Language(qp.Language)
		}
		for _, it := range qp.Items {
			profile.Items = append(profile.Items, arr.ProfileItem{Name: it.Name, Allowed: bool(it.Allowed), Qualities: append([]string(nil), it.Items...)})
		}
		for fname, id := range qp.FormatItems {
			cf, ok := up.Formats[id]
			if !ok {
				for _, c := range up.Formats {
					if strings.EqualFold(c.Name, fname) {
						cf, ok = c, true
						break
					}
				}
			}
			if ok {
				profile.FormatScores[cf.Key()] = trash.ScoreFor(cf, qp.TrashScoreSet)
			}
		}
	} else {
		if name == "" {
			name = "Imported formats"
		}
		profile = arr.Profile{App: app, Name: name, FormatScores: map[string]int{}, Propers: arr.PropersDoNotPrefer}
		defs := arr.Qualities(app)
		for i := len(defs) - 1; i >= 0; i-- {
			profile.Items = append(profile.Items, arr.ProfileItem{Name: defs[i].Quality.Name, Allowed: defs[i].Quality.Name != "Unknown"})
		}
		for key, cf := range up.Formats {
			profile.FormatScores[key] = trash.ScoreFor(cf, "")
		}
	}
	if err := arr.ValidateProfile(profile); err != nil {
		return Upstream{}, Overrides{}, "", "", err
	}
	up.Profile = profile
	up.Commit = contentHash(profile, up.Formats)
	return up, Overrides{}, name, app, nil
}

// Export returns a JSON document that parseImport accepts.
func Export(p *Preset) ([]byte, error) {
	ov := heldFormatsSwitchedOff(p)
	doc := ImportDocument{App: p.App, Name: p.DisplayName(), Upstream: &p.Upstream, Overrides: &ov, Sync: &p.Sync, Audience: p.Audience}
	return json.MarshalIndent(doc, "", "  ")
}
