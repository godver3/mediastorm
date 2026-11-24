package arr

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// ParityObservation is what a Sonarr/Radarr instance reported for one title
// through its /api/v3/parse endpoint. Fixtures of these observations are
// recorded with tools/arrparity and checked in under testdata/parity, so
// parity can be verified without a running instance.
type ParityObservation struct {
	Title         string   `json:"title"`
	Parsed        bool     `json:"parsed"`
	Quality       string   `json:"quality,omitempty"`
	Version       int      `json:"version,omitempty"`
	Real          int      `json:"real,omitempty"`
	IsRepack      bool     `json:"isRepack,omitempty"`
	Languages     []string `json:"languages,omitempty"`
	ReleaseGroup  string   `json:"releaseGroup,omitempty"`
	ReleaseType   string   `json:"releaseType,omitempty"`
	FullSeason    bool     `json:"fullSeason,omitempty"`
	Edition       string   `json:"edition,omitempty"`
	Year          int      `json:"year,omitempty"`
	CustomFormats []string `json:"customFormats,omitempty"`
}

// ParityFixture is a recorded oracle run.
type ParityFixture struct {
	App           App                 `json:"app"`
	OracleVersion string              `json:"oracleVersion"`
	TrashCommit   string              `json:"trashCommit,omitempty"`
	Observations  []ParityObservation `json:"observations"`
}

// LoadParityFixture reads a fixture file (".json" or gzip-compressed
// ".json.gz").
func LoadParityFixture(path string) (*ParityFixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		data, err = io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	var fx ParityFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &fx, nil
}

// Save writes the fixture with stable ordering; a ".gz" path is compressed.
func (fx *ParityFixture) Save(path string) error {
	sort.SliceStable(fx.Observations, func(i, j int) bool { return fx.Observations[i].Title < fx.Observations[j].Title })
	data, err := json.MarshalIndent(fx, "", " ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if strings.HasSuffix(path, ".gz") {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if _, err := zw.Write(data); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		data = buf.Bytes()
	}
	return os.WriteFile(path, data, 0o644)
}

// ParityField names a compared attribute.
type ParityField string

const (
	FieldQuality       ParityField = "quality"
	FieldRevision      ParityField = "revision"
	FieldLanguages     ParityField = "languages"
	FieldReleaseGroup  ParityField = "releaseGroup"
	FieldReleaseType   ParityField = "releaseType"
	FieldEdition       ParityField = "edition"
	FieldCustomFormats ParityField = "customFormats"
)

// ParityMismatch describes one difference.
type ParityMismatch struct {
	Title string      `json:"title"`
	Field ParityField `json:"field"`
	Want  string      `json:"want"`
	Got   string      `json:"got"`
}

// ParityReport summarizes a comparison.
type ParityReport struct {
	App        App                   `json:"app"`
	Total      int                   `json:"total"`
	Compared   map[ParityField]int   `json:"compared"`
	Matched    map[ParityField]int   `json:"matched"`
	Mismatches []ParityMismatch      `json:"mismatches"`
	ByField    map[ParityField][]int `json:"-"`
}

// Rate returns the match rate of a field in [0,1].
func (r *ParityReport) Rate(field ParityField) float64 {
	if r.Compared[field] == 0 {
		return 1
	}
	return float64(r.Matched[field]) / float64(r.Compared[field])
}

// Summary returns a one-line-per-field summary.
func (r *ParityReport) Summary() string {
	fields := []ParityField{FieldQuality, FieldRevision, FieldLanguages, FieldReleaseGroup, FieldReleaseType, FieldEdition, FieldCustomFormats}
	var b strings.Builder
	fmt.Fprintf(&b, "%s parity over %d titles\n", r.App, r.Total)
	for _, f := range fields {
		if r.Compared[f] == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %-14s %6.2f%%  (%d/%d)\n", f, r.Rate(f)*100, r.Matched[f], r.Compared[f])
	}
	return b.String()
}

func joinSorted(values []string) string {
	cp := append([]string(nil), values...)
	sort.Strings(cp)
	return strings.Join(cp, ", ")
}

// CompareParity parses every observed title with this package and compares
// the result with the oracle. When formats is non-nil, matched custom format
// names are compared as well (the oracle must have had the same formats).
func CompareParity(fx *ParityFixture, formats *FormatSet) *ParityReport {
	rep := &ParityReport{App: fx.App, Compared: map[ParityField]int{}, Matched: map[ParityField]int{}}
	check := func(title string, field ParityField, want, got string) {
		rep.Compared[field]++
		if want == got {
			rep.Matched[field]++
			return
		}
		rep.Mismatches = append(rep.Mismatches, ParityMismatch{Title: title, Field: field, Want: want, Got: got})
	}
	for _, obs := range fx.Observations {
		if !obs.Parsed {
			continue
		}
		rep.Total++
		rel := Parse(fx.App, obs.Title)
		check(obs.Title, FieldQuality, obs.Quality, rel.Quality.Name)
		check(obs.Title, FieldRevision,
			fmt.Sprintf("v%d r%d repack=%t", obs.Version, obs.Real, obs.IsRepack),
			fmt.Sprintf("v%d r%d repack=%t", rel.Revision.Version, rel.Revision.Real, rel.Revision.IsRepack))
		langs := make([]string, 0, len(rel.Languages))
		for _, l := range rel.Languages {
			langs = append(langs, string(l))
		}
		check(obs.Title, FieldLanguages, joinSorted(obs.Languages), joinSorted(langs))
		check(obs.Title, FieldReleaseGroup, obs.ReleaseGroup, rel.ReleaseGroup)
		if fx.App == AppSonarr {
			check(obs.Title, FieldReleaseType, obs.ReleaseType, rel.ReleaseType.String())
		} else {
			check(obs.Title, FieldEdition, strings.ToLower(obs.Edition), strings.ToLower(rel.Edition))
		}
		if formats != nil {
			var got []string
			for _, key := range formats.MatchKeys(&rel, nil) {
				if c, ok := formats.Get(key); ok {
					got = append(got, c.Format.Name)
				}
			}
			check(obs.Title, FieldCustomFormats, joinSorted(obs.CustomFormats), joinSorted(got))
		}
	}
	return rep
}
