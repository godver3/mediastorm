package trash

import (
	"strings"
	"testing"

	"novastream/internal/arr"
)

func mergeTestSnapshot(source, commit string, formats ...arr.CustomFormat) *Snapshot {
	data := &AppData{CustomFormats: map[string]arr.CustomFormat{}}
	for _, cf := range formats {
		data.CustomFormats[cf.TrashID] = cf
	}
	return &Snapshot{Source: source, Commit: commit, Apps: map[arr.App]*AppData{arr.AppSonarr: data}}
}

func titleFormat(id, pattern string) arr.CustomFormat {
	return arr.CustomFormat{TrashID: id, Name: id, Specifications: []arr.Specification{
		{Name: "t", Implementation: arr.SpecReleaseTitle, Required: true, Fields: map[string]any{"value": pattern}},
	}}
}

func TestMergeAttributesFormatsToSources(t *testing.T) {
	fork := mergeTestSnapshot("fork", "f1", titleFormat("a", `\bA\b`), titleFormat("b", `\bfork\b`))
	community := mergeTestSnapshot("community", "c1", titleFormat("b", `\bcommunity\b`), titleFormat("c", `\bC\b`))
	trash := mergeTestSnapshot("trash", "t1", titleFormat("a", `\bA\b`), titleFormat("b", `\btrash\b`), titleFormat("c", `\bC\b`), titleFormat("d", `\bD\b`))

	m := Merge(fork, community, trash)
	if got := strings.Join(m.SourceNames(), ","); got != "fork,community,trash" {
		t.Errorf("parts %q", got)
	}
	if m.Commit != "fork@f1,community@c1,trash@t1" || m.Source != "merged" {
		t.Errorf("commit %q source %q", m.Commit, m.Source)
	}
	cases := []struct {
		key, source, shadowed, pattern string
	}{
		{"a", "fork", "", `\bA\b`},                   // identical copy in trash is not an override
		{"b", "fork", "community,trash", `\bfork\b`}, // highest precedence wins, others listed in order
		{"c", "community", "", `\bC\b`},              // identical again
		{"d", "trash", "", `\bD\b`},                  // only one source has it
	}
	for _, c := range cases {
		src, shadowed := m.FormatSource(arr.AppSonarr, c.key)
		if src != c.source || strings.Join(shadowed, ",") != c.shadowed {
			t.Errorf("%s: source %q shadowed %v, want %q %q", c.key, src, shadowed, c.source, c.shadowed)
		}
		if got := m.App(arr.AppSonarr).CustomFormats[c.key].Specifications[0].Fields["value"]; got != c.pattern {
			t.Errorf("%s: winning definition %v", c.key, got)
		}
	}

	single := Merge(trash)
	if single.Commit != "t1" || single.Source != "trash" {
		t.Errorf("single source keeps its identity: %q %q", single.Commit, single.Source)
	}
	if src, _ := single.FormatSource(arr.AppSonarr, "d"); src != "trash" {
		t.Errorf("single source attribution %q", src)
	}
	plain := mergeTestSnapshot("bundled", "x")
	if src, _ := plain.FormatSource(arr.AppSonarr, "zzz"); src != "bundled" {
		t.Errorf("unmerged snapshot falls back to its source, got %q", src)
	}
}

// A fork repeating upstream's profile groups and conflict sets does not
// list them twice; the fork's version of a group wins.
func TestMergeDoesNotDuplicateGroupsAndConflicts(t *testing.T) {
	fork := mergeTestSnapshot("fork", "f1")
	trash := mergeTestSnapshot("trash", "t1")
	fork.Apps[arr.AppSonarr].ProfileGroups = []ProfileGroup{{Name: "German", Profiles: map[string]string{"x": "fork"}}}
	trash.Apps[arr.AppSonarr].ProfileGroups = []ProfileGroup{{Name: "German", Profiles: map[string]string{"x": "trash"}}, {Name: "Anime"}}
	conflict := Conflict{Formats: map[string]string{"a": "A", "b": "B"}}
	fork.Apps[arr.AppSonarr].Conflicts = []Conflict{conflict}
	trash.Apps[arr.AppSonarr].Conflicts = []Conflict{conflict, {Formats: map[string]string{"c": "C", "d": "D"}}}

	data := Merge(fork, trash).Apps[arr.AppSonarr]
	if len(data.ProfileGroups) != 2 || data.ProfileGroups[0].Profiles["x"] != "fork" {
		t.Errorf("profile groups %+v", data.ProfileGroups)
	}
	if len(data.Conflicts) != 2 {
		t.Errorf("conflicts %+v", data.Conflicts)
	}
}
