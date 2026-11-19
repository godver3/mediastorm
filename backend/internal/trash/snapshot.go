// Package trash reads TRaSH-Guides JSON (custom formats, custom format
// groups, quality profiles, quality sizes and conflicts) into a Snapshot.
//
// TRaSH-Guides content is MIT licensed (Copyright (c) TRaSH). A snapshot of
// the JSON is bundled with mediastorm for offline use; see bundle.go.
package trash

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"novastream/internal/arr"
)

// ProfileItem is a quality or quality group in a TRaSH profile. Items are
// ordered from most to least preferred, as shown in the *arr UI.
type ProfileItem struct {
	Name    string   `json:"name"`
	Allowed FlexBool `json:"allowed"`
	Items   []string `json:"items,omitempty"`
}

// QualityProfile is a TRaSH quality profile.
type QualityProfile struct {
	TrashID               string            `json:"trash_id"`
	Name                  string            `json:"name"`
	TrashDescription      string            `json:"trash_description,omitempty"`
	TrashURL              string            `json:"trash_url,omitempty"`
	Group                 int               `json:"group,omitempty"`
	TrashScoreSet         string            `json:"trash_score_set,omitempty"`
	UpgradeAllowed        FlexBool          `json:"upgradeAllowed"`
	Cutoff                string            `json:"cutoff"`
	MinFormatScore        int               `json:"minFormatScore"`
	CutoffFormatScore     int               `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int               `json:"minUpgradeFormatScore"`
	Language              string            `json:"language,omitempty"`
	Items                 []ProfileItem     `json:"items"`
	FormatItems           map[string]string `json:"formatItems"`
}

// GroupFormat is one custom format reference inside a CF group.
type GroupFormat struct {
	Name     string    `json:"name"`
	TrashID  string    `json:"trash_id"`
	Required FlexBool  `json:"required"`
	Default  *FlexBool `json:"default,omitempty"`
}

// CFGroup is an optional bundle of custom formats a profile may enable.
type CFGroup struct {
	TrashID          string        `json:"trash_id"`
	Name             string        `json:"name"`
	TrashDescription string        `json:"trash_description,omitempty"`
	Default          FlexBool      `json:"default,omitempty"`
	CustomFormats    []GroupFormat `json:"custom_formats"`
	QualityProfiles  struct {
		Include map[string]string `json:"include,omitempty"`
		Exclude map[string]string `json:"exclude,omitempty"`
	} `json:"quality_profiles"`
}

// AppliesTo reports whether the group is offered for a profile.
func (g CFGroup) AppliesTo(profileTrashID string) bool {
	for _, id := range g.QualityProfiles.Exclude {
		if id == profileTrashID {
			return false
		}
	}
	if len(g.QualityProfiles.Include) == 0 {
		return true
	}
	for _, id := range g.QualityProfiles.Include {
		if id == profileTrashID {
			return true
		}
	}
	return false
}

// FlexBool decodes JSON booleans that TRaSH occasionally writes as strings.
type FlexBool bool

// UnmarshalJSON accepts true/false and "true"/"false".
func (b *FlexBool) UnmarshalJSON(data []byte) error {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(string(data)), "\"")) {
	case "true", "1", "yes":
		*b = true
	case "false", "0", "no", "", "null":
		*b = false
	default:
		return fmt.Errorf("invalid boolean %s", data)
	}
	return nil
}

// QualitySize is one quality-size definition file.
type QualitySize struct {
	TrashID   string `json:"trash_id"`
	Type      string `json:"type"`
	Qualities []struct {
		Quality   string   `json:"quality"`
		Min       float64  `json:"min"`
		Preferred *float64 `json:"preferred"`
		Max       *float64 `json:"max"`
	} `json:"qualities"`
}

// ProfileGroup is a UI grouping of profiles (Standard, German, ...).
type ProfileGroup struct {
	Name     string            `json:"name"`
	Profiles map[string]string `json:"profiles"`
}

// Conflict is a set of custom formats that should not be enabled together.
type Conflict struct {
	Formats map[string]string `json:"formats"` // trash_id -> name
}

// AppData holds one app's TRaSH content.
type AppData struct {
	CustomFormats map[string]arr.CustomFormat `json:"customFormats"`
	Groups        map[string]CFGroup          `json:"groups"`
	Profiles      map[string]QualityProfile   `json:"profiles"`
	QualitySizes  map[string]QualitySize      `json:"qualitySizes"`
	ProfileGroups []ProfileGroup              `json:"profileGroups,omitempty"`
	Conflicts     []Conflict                  `json:"conflicts,omitempty"`
	// FormatSources maps each custom format to the source it was taken
	// from and Shadowed lists the lower-precedence sources that define the
	// same trash_id too. Merge sets both.
	FormatSources map[string]string   `json:"formatSources,omitempty"`
	Shadowed      map[string][]string `json:"shadowed,omitempty"`
}

// Snapshot is a parsed copy of the TRaSH-Guides JSON at one commit.
type Snapshot struct {
	Commit string               `json:"commit"`
	Source string               `json:"source,omitempty"`
	Apps   map[arr.App]*AppData `json:"apps"`
	// Parts lists the merged sources, highest precedence first.
	Parts []string `json:"parts,omitempty"`
	// Skipped lists files that could not be read ("path: error"); the rest
	// of the snapshot is usable without them.
	Skipped []string `json:"skipped,omitempty"`
}

// FormatSource returns the source a custom format came from and the
// lower-precedence sources that also define it.
func (s *Snapshot) FormatSource(app arr.App, key string) (string, []string) {
	data := s.App(app)
	if src, ok := data.FormatSources[key]; ok {
		return src, data.Shadowed[key]
	}
	return s.Source, nil
}

// SourceNames lists the sources a snapshot is made of, highest precedence
// first.
func (s *Snapshot) SourceNames() []string {
	if len(s.Parts) > 0 {
		return append([]string(nil), s.Parts...)
	}
	if s.Source != "" {
		return []string{s.Source}
	}
	return nil
}

// App returns the data for one app (never nil).
func (s *Snapshot) App(app arr.App) *AppData {
	if s == nil || s.Apps == nil || s.Apps[app] == nil {
		return &AppData{}
	}
	return s.Apps[app]
}

// CustomFormats returns every custom format of an app sorted by name.
func (s *Snapshot) CustomFormats(app arr.App) []arr.CustomFormat {
	data := s.App(app)
	out := make([]arr.CustomFormat, 0, len(data.CustomFormats))
	for _, cf := range data.CustomFormats {
		out = append(out, cf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ProfileList returns an app's profiles sorted by group then name.
func (s *Snapshot) ProfileList(app arr.App) []QualityProfile {
	data := s.App(app)
	out := make([]QualityProfile, 0, len(data.Profiles))
	for _, p := range data.Profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// GroupsFor returns the CF groups offered for a profile, sorted by name.
func (s *Snapshot) GroupsFor(app arr.App, profileTrashID string) []CFGroup {
	data := s.App(app)
	var out []CFGroup
	for _, g := range data.Groups {
		if g.AppliesTo(profileTrashID) {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ScoreFor returns a format's score in a score set, falling back to the
// "default" set and then 0, as TRaSH documents.
func ScoreFor(cf arr.CustomFormat, scoreSet string) int {
	if scoreSet != "" {
		if v, ok := cf.TrashScores[scoreSet]; ok {
			return v
		}
	}
	return cf.TrashScores["default"]
}

// ConflictsFor returns, for an enabled format set, every pair of enabled
// formats that TRaSH marks as conflicting.
func (s *Snapshot) ConflictsFor(app arr.App, enabled map[string]bool) [][]string {
	var out [][]string
	for _, c := range s.App(app).Conflicts {
		var hit []string
		for id, name := range c.Formats {
			if enabled[id] {
				hit = append(hit, name)
			}
		}
		if len(hit) > 1 {
			sort.Strings(hit)
			out = append(out, hit)
		}
	}
	return out
}

// fileSystem abstracts the docs/json tree.
type fileSystem interface {
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
}

type osFS struct{ root string }

func (o osFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(filepath.Join(o.root, filepath.FromSlash(name)))
}
func (o osFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(o.root, filepath.FromSlash(name)))
}

type mapFS map[string][]byte

func (m mapFS) ReadDir(name string) ([]fs.DirEntry, error) {
	prefix := strings.TrimSuffix(name, "/") + "/"
	seen := map[string]bool{}
	var out []fs.DirEntry
	for p := range m {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if strings.Contains(rest, "/") || seen[rest] {
			continue
		}
		seen[rest] = true
		out = append(out, fileEntry(rest))
	}
	if len(out) == 0 {
		return nil, fs.ErrNotExist
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (m mapFS) ReadFile(name string) ([]byte, error) {
	if data, ok := m[name]; ok {
		return data, nil
	}
	return nil, fs.ErrNotExist
}

type fileEntry string

func (f fileEntry) Name() string               { return string(f) }
func (f fileEntry) IsDir() bool                { return false }
func (f fileEntry) Type() fs.FileMode          { return 0 }
func (f fileEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

// LoadDir reads a checked-out docs/json directory. A file named COMMIT in the
// directory, when present, provides the snapshot's commit.
func LoadDir(dir string) (*Snapshot, error) {
	snap, err := load(osFS{root: dir})
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(filepath.Join(dir, "COMMIT")); err == nil {
		snap.Commit = strings.TrimSpace(string(data))
	}
	snap.Source = dir
	return snap, nil
}

// MaxArchiveBytes bounds the uncompressed size accepted from an archive.
const MaxArchiveBytes = 64 << 20

// LoadArchive reads a GitHub tar.gz of the Guides repository (or a bundle
// produced by BuildBundle), keeping only docs/json content.
func LoadArchive(r io.Reader, commit string) (*Snapshot, error) {
	files, err := readArchive(r)
	if err != nil {
		return nil, err
	}
	if c, ok := files["COMMIT"]; ok && commit == "" {
		commit = strings.TrimSpace(string(c))
	}
	snap, err := load(files)
	if err != nil {
		return nil, err
	}
	snap.Commit = commit
	return snap, nil
}

// CompactArchive turns a Guides archive into the compact bundle format
// (docs/json only, JSON compacted): what is worth caching of a 25 MB
// repository download is about 120 KB.
func CompactArchive(archive []byte, commit string) ([]byte, error) {
	files, err := readArchive(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	delete(files, "COMMIT")
	var out bytes.Buffer
	if err := writeBundle(files, commit, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// readArchive collects the docs/json files of an archive.
func readArchive(r io.Reader) (mapFS, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("trash archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := mapFS{}
	total := int64(0)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("trash archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.ReplaceAll(hdr.Name, "\\", "/"))
		idx := strings.Index(name, "docs/json/")
		var rel string
		switch {
		case idx >= 0:
			rel = name[idx+len("docs/json/"):]
		case strings.HasPrefix(name, "json/"):
			rel = strings.TrimPrefix(name, "json/")
		default:
			continue
		}
		if !strings.HasSuffix(rel, ".json") && rel != "COMMIT" {
			continue
		}
		total += hdr.Size
		if total > MaxArchiveBytes {
			return nil, fmt.Errorf("trash archive: docs/json exceeds %d bytes", MaxArchiveBytes)
		}
		data, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if err != nil {
			return nil, fmt.Errorf("trash archive %s: %w", name, err)
		}
		files[rel] = data
	}
	return files, nil
}

func readJSON(fsys fileSystem, name string, target any) error {
	data, err := fsys.ReadFile(name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func listJSON(fsys fileSystem, dir string) []string {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, dir+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func load(fsys fileSystem) (*Snapshot, error) {
	snap := &Snapshot{Apps: map[arr.App]*AppData{}}
	for _, app := range []arr.App{arr.AppSonarr, arr.AppRadarr} {
		base := string(app)
		data := &AppData{
			CustomFormats: map[string]arr.CustomFormat{},
			Groups:        map[string]CFGroup{},
			Profiles:      map[string]QualityProfile{},
			QualitySizes:  map[string]QualitySize{},
		}
		// A malformed file is skipped and reported in Snapshot.Skipped
		// rather than failing the whole snapshot: one broken format in a
		// fork must not take every preset down. A profile that references a
		// skipped format reports it in Built.Missing.
		skip := func(p string, err error) {
			snap.Skipped = append(snap.Skipped, fmt.Sprintf("%s: %v", p, err))
		}
		for _, p := range listJSON(fsys, base+"/cf") {
			var cf arr.CustomFormat
			if err := readJSON(fsys, p, &cf); err != nil {
				skip(p, err)
				continue
			}
			if cf.TrashID == "" {
				skip(p, errors.New("custom format without trash_id"))
				continue
			}
			data.CustomFormats[cf.TrashID] = cf
		}
		for _, p := range listJSON(fsys, base+"/cf-groups") {
			var g CFGroup
			if err := readJSON(fsys, p, &g); err != nil {
				skip(p, err)
				continue
			}
			if g.TrashID == "" {
				skip(p, errors.New("custom format group without trash_id"))
				continue
			}
			data.Groups[g.TrashID] = g
		}
		for _, p := range listJSON(fsys, base+"/quality-profiles") {
			var qp QualityProfile
			if err := readJSON(fsys, p, &qp); err != nil {
				skip(p, err)
				continue
			}
			if qp.TrashID == "" {
				skip(p, errors.New("quality profile without trash_id"))
				continue
			}
			data.Profiles[qp.TrashID] = qp
		}
		for _, p := range listJSON(fsys, base+"/quality-size") {
			var qs QualitySize
			if err := readJSON(fsys, p, &qs); err != nil {
				skip(p, err)
				continue
			}
			if qs.Type == "" {
				qs.Type = strings.TrimSuffix(path.Base(p), ".json")
			}
			data.QualitySizes[qs.Type] = qs
		}
		for _, p := range listJSON(fsys, base+"/quality-profile-groups") {
			var groups []ProfileGroup
			if err := readJSON(fsys, p, &groups); err == nil {
				data.ProfileGroups = append(data.ProfileGroups, groups...)
			}
		}
		var conflicts struct {
			CustomFormats []map[string]struct {
				Name string `json:"name"`
			} `json:"custom_formats"`
		}
		if err := readJSON(fsys, base+"/conflicts.json", &conflicts); err == nil {
			for _, set := range conflicts.CustomFormats {
				c := Conflict{Formats: map[string]string{}}
				for id, v := range set {
					c.Formats[id] = v.Name
				}
				data.Conflicts = append(data.Conflicts, c)
			}
		}
		if len(data.CustomFormats) > 0 || len(data.Profiles) > 0 {
			snap.Apps[app] = data
		}
	}
	if len(snap.Apps) == 0 {
		return nil, errors.New("trash: no sonarr or radarr content found (expected docs/json/{sonarr,radarr}/cf)")
	}
	return snap, nil
}

// BuildBundle writes the docs/json content of a snapshot directory as a
// compact tar.gz (used to refresh the embedded bundle).
func BuildBundle(dir, commit string, w io.Writer) error {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		top := strings.SplitN(rel, "/", 2)[0]
		if top != "sonarr" && top != "radarr" {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		files[rel] = data
	}
	return writeBundle(files, commit, w)
}

// writeBundle writes the sonarr/ and radarr/ JSON files, compacted and in
// a stable order, plus the commit.
func writeBundle(files map[string][]byte, commit string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	var paths []string
	for rel := range files {
		if top := strings.SplitN(rel, "/", 2)[0]; top == "sonarr" || top == "radarr" {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	write := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: "json/" + name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	for _, rel := range paths {
		// Malformed JSON is kept as is: load skips it and reports it in
		// Snapshot.Skipped instead of the whole archive failing here.
		data := files[rel]
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err == nil {
			data = compact.Bytes()
		}
		if err := write(rel, data); err != nil {
			return err
		}
	}
	if err := write("COMMIT", []byte(commit+"\n")); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func sameFormat(a, b arr.CustomFormat) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

func sourceName(s *Snapshot) string {
	if s.Source == "" {
		return "source"
	}
	return s.Source
}

// hasConflict reports whether list has a conflict set with c's formats.
func hasConflict(list []Conflict, c Conflict) bool {
	key := func(c Conflict) string {
		ids := make([]string, 0, len(c.Formats))
		for id := range c.Formats {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	want := key(c)
	for _, x := range list {
		if key(x) == want {
			return true
		}
	}
	return false
}

// Merge combines snapshots; earlier snapshots take precedence when two
// contain the same trash_id. The merged commit lists every part as
// "source@commit".
func Merge(snaps ...*Snapshot) *Snapshot {
	out := &Snapshot{Apps: map[arr.App]*AppData{}}
	var commits []string
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		if s == nil {
			continue
		}
		name := sourceName(s)
		for app, data := range s.Apps {
			dst := out.Apps[app]
			if dst == nil {
				dst = &AppData{CustomFormats: map[string]arr.CustomFormat{}, Groups: map[string]CFGroup{}, Profiles: map[string]QualityProfile{}, QualitySizes: map[string]QualitySize{},
					FormatSources: map[string]string{}, Shadowed: map[string][]string{}}
				out.Apps[app] = dst
			}
			for k, v := range data.CustomFormats {
				// Sources are applied from lowest to highest precedence, so
				// the previous owner is the next one down. Identical copies
				// (a fork that did not change a format) are not worth noting.
				if prev, ok := dst.FormatSources[k]; ok && prev != name && !sameFormat(dst.CustomFormats[k], v) {
					dst.Shadowed[k] = append([]string{prev}, dst.Shadowed[k]...)
				}
				dst.CustomFormats[k] = v
				dst.FormatSources[k] = name
			}
			for k, v := range data.Groups {
				dst.Groups[k] = v
			}
			for k, v := range data.Profiles {
				dst.Profiles[k] = v
			}
			for k, v := range data.QualitySizes {
				dst.QualitySizes[k] = v
			}
			// A fork usually repeats upstream's profile groups and conflict
			// sets: a group of the same name is replaced, a conflict set with
			// the same formats kept once.
		groups:
			for _, g := range data.ProfileGroups {
				for i := range dst.ProfileGroups {
					if dst.ProfileGroups[i].Name == g.Name {
						dst.ProfileGroups[i] = g
						continue groups
					}
				}
				dst.ProfileGroups = append(dst.ProfileGroups, g)
			}
			for _, c := range data.Conflicts {
				if !hasConflict(dst.Conflicts, c) {
					dst.Conflicts = append(dst.Conflicts, c)
				}
			}
		}
	}
	for _, s := range snaps {
		if s == nil {
			continue
		}
		name := sourceName(s)
		commits = append(commits, name+"@"+s.Commit)
		out.Parts = append(out.Parts, name)
		for _, sk := range s.Skipped {
			if len(snaps) > 1 {
				sk = name + ": " + sk
			}
			out.Skipped = append(out.Skipped, sk)
		}
	}
	if len(commits) == 1 && snaps[0] != nil {
		out.Commit = snaps[0].Commit
		out.Source = snaps[0].Source
	} else {
		out.Commit = strings.Join(commits, ",")
		out.Source = "merged"
	}
	return out
}
