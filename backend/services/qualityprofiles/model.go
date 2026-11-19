// Package qualityprofiles manages Sonarr/Radarr-compatible quality presets
// (quality profiles plus custom formats), imported from TRaSH-Guides, from
// a Sonarr/Radarr instance or created by hand, and keeps them in sync with
// their upstream source.
//
// Every preset is stored as three layers:
//
//   - Upstream: the accepted state of the source at one commit
//   - Overrides: the user's changes, stored as a diff keyed by trash_id
//   - Resolved: Upstream + Overrides, computed on demand; the only layer
//     ranking reads
//
// Sync modes, field policies, dry runs and rollbacks are rules for how the
// layers combine, so they never touch the user's overrides directly.
package qualityprofiles

import (
	"strings"
	"time"

	"novastream/internal/arr"
	"novastream/internal/trash"
)

// OriginKind says where a preset comes from.
type OriginKind string

const (
	OriginTrash  OriginKind = "trash"  // TRaSH-Guides JSON (GitHub, URL or bundled)
	OriginArr    OriginKind = "arr"    // a Sonarr/Radarr instance
	OriginManual OriginKind = "manual" // created or pasted by hand; never syncs
)

// Origin records how to refresh a preset's upstream layer.
type Origin struct {
	Kind OriginKind `json:"kind"`
	// TRaSH
	ProfileTrashID string `json:"profileTrashId,omitempty"`
	ScoreSet       string `json:"scoreSet,omitempty"`
	QualitySize    string `json:"qualitySize,omitempty"`
	// Sonarr/Radarr instance
	ArrURL       string `json:"arrUrl,omitempty"`
	ArrAPIKey    string `json:"arrApiKey,omitempty"`
	ArrProfileID int    `json:"arrProfileId,omitempty"`
}

// Upstream is the accepted state of a preset's source.
type Upstream struct {
	// Commit identifies the source state: "source@sha[,source@sha]" for
	// TRaSH, a content hash for other origins.
	Commit    string    `json:"commit"`
	FetchedAt time.Time `json:"fetchedAt"`
	// Profile holds the base profile: quality items, scores of the formats
	// the profile itself lists, limits and options.
	Profile arr.Profile `json:"profile"`
	// Formats holds every format the profile or one of its groups can use,
	// keyed by arr.CustomFormat.Key().
	Formats map[string]arr.CustomFormat `json:"formats"`
	// Groups are the TRaSH CF groups offered for the profile.
	Groups []trash.CFGroup `json:"groups,omitempty"`
	// ScoreSet is the TRaSH score set used for group formats.
	ScoreSet string `json:"scoreSet,omitempty"`
	// Sources lists the snapshot sources that contributed, highest
	// precedence first.
	Sources []string `json:"sources,omitempty"`
	// FormatSources maps formats to the source they were taken from;
	// Shadowed lists other, lower-precedence sources defining the same
	// trash_id.
	FormatSources map[string]string   `json:"formatSources,omitempty"`
	Shadowed      map[string][]string `json:"shadowed,omitempty"`
	// ConflictSets are TRaSH formats that should not be enabled together.
	ConflictSets []trash.Conflict `json:"conflictSets,omitempty"`
}

// Overrides are the user's changes on top of Upstream.
type Overrides struct {
	Name              *string                      `json:"name,omitempty"`
	Scores            map[string]int               `json:"scores,omitempty"`
	Disabled          map[string]bool              `json:"disabled,omitempty"`
	Formats           map[string]arr.CustomFormat  `json:"formats,omitempty"` // edited upstream formats or own formats
	Groups            map[string]trash.GroupChoice `json:"groups,omitempty"`
	Items             []arr.ProfileItem            `json:"items,omitempty"`
	MinFormatScore    *int                         `json:"minFormatScore,omitempty"`
	Language          *arr.Language                `json:"language,omitempty"`
	Propers           *arr.PropersMode             `json:"propers,omitempty"`
	EnforceSizeLimits *bool                        `json:"enforceSizeLimits,omitempty"`
	SizeLimits        map[string]arr.SizeLimit     `json:"sizeLimits,omitempty"`
	PreferredProtocol *arr.Protocol                `json:"preferredProtocol,omitempty"`
	MultiLanguages    []arr.Language               `json:"multiLanguages,omitempty"`
	// Base records the upstream value of every overridden field at the time
	// it was overridden ("score:<key>", "format:<key>", "items", ...), so a
	// later upstream change to the same field is reported as a conflict.
	Base map[string]string `json:"base,omitempty"`
}

// SyncMode controls how upstream updates are applied.
type SyncMode string

const (
	SyncAuto   SyncMode = "auto"   // apply on every check, report conflicts
	SyncNotify SyncMode = "notify" // stage the update and wait for approval
	SyncManual SyncMode = "manual" // only check when asked
	SyncPinned SyncMode = "pinned" // stay at PinnedCommit
)

// Schedule controls how often a preset is checked automatically.
type Schedule string

const (
	ScheduleDaily  Schedule = "daily"
	ScheduleWeekly Schedule = "weekly"
	ScheduleManual Schedule = "manual"
)

// NewFormatPolicy controls formats that appear upstream after import.
type NewFormatPolicy string

const (
	NewFormatsEnable  NewFormatPolicy = "enable"
	NewFormatsDisable NewFormatPolicy = "disable"
	NewFormatsReview  NewFormatPolicy = "review"
)

// Follow selects which parts of the preset follow upstream. A part that
// does not follow keeps its last accepted upstream value.
type Follow struct {
	Profile    bool `json:"profile"`    // quality order/groups, min score, cutoff, language
	Conditions bool `json:"conditions"` // format specifications (regexes etc.)
	Scores     bool `json:"scores"`     // format scores
	Groups     bool `json:"groups"`     // CF group membership and defaults
	Sizes      bool `json:"sizes"`      // quality size limits
}

// FieldPolicy overrides Follow.Conditions/Scores for one format.
type FieldPolicy struct {
	Conditions *bool `json:"conditions,omitempty"`
	Scores     *bool `json:"scores,omitempty"`
}

// SyncSettings configure updates for one preset.
type SyncSettings struct {
	Mode         SyncMode               `json:"mode"`
	PinnedCommit string                 `json:"pinnedCommit,omitempty"`
	Schedule     Schedule               `json:"schedule"`
	Follow       Follow                 `json:"follow"`
	PerFormat    map[string]FieldPolicy `json:"perFormat,omitempty"`
	NewFormats   NewFormatPolicy        `json:"newFormats"`
}

// DefaultSyncSettings follow everything, check daily and apply updates
// automatically (conflicts with the user's changes are reported).
func DefaultSyncSettings() SyncSettings {
	return SyncSettings{
		Mode:       SyncAuto,
		Schedule:   ScheduleDaily,
		Follow:     Follow{Profile: true, Conditions: true, Scores: true, Groups: true, Sizes: true},
		NewFormats: NewFormatsEnable,
	}
}

// Conflict is an upstream change to a field the user has overridden.
type Conflict struct {
	Path           string    `json:"path"`
	Format         string    `json:"format,omitempty"`
	Field          string    `json:"field"`
	UpstreamBefore string    `json:"upstreamBefore"`
	UpstreamAfter  string    `json:"upstreamAfter"`
	Yours          string    `json:"yours"`
	Commit         string    `json:"commit"`
	DetectedAt     time.Time `json:"detectedAt"`
}

// Change is one changelog entry between two upstream states.
type Change struct {
	Kind   string `json:"kind"` // added, removed, changed
	Part   string `json:"part"` // format, score, profile, group, size
	Key    string `json:"key,omitempty"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Pending is a staged upstream update.
type Pending struct {
	FromCommit string     `json:"fromCommit"`
	ToCommit   string     `json:"toCommit"`
	CheckedAt  time.Time  `json:"checkedAt"`
	Upstream   Upstream   `json:"upstream"`
	Changes    []Change   `json:"changes"`
	Conflicts  []Conflict `json:"conflicts,omitempty"`
	// NewFormats lists formats awaiting review (NewFormatsReview).
	NewFormats []string `json:"newFormats,omitempty"`
}

// HistoryEntry is a previously applied state used for rollback.
type HistoryEntry struct {
	ID        string    `json:"id"`
	AppliedAt time.Time `json:"appliedAt"`
	Commit    string    `json:"commit"`
	Note      string    `json:"note,omitempty"`
	Upstream  Upstream  `json:"upstream"`
	Overrides Overrides `json:"overrides"`
	// ReviewFormats are the formats that were held for review then.
	ReviewFormats []string `json:"reviewFormats,omitempty"`
}

// RemovedFormat is a format that disappeared upstream.
type RemovedFormat struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Commit    string    `json:"commit"`
	RemovedAt time.Time `json:"removedAt"`
	// Kept is true when the user's overrides referenced the format and it
	// was preserved as an own format.
	Kept bool `json:"kept"`
}

// Audience says which titles a preset is meant for.
type Audience string

const (
	AudienceAll      Audience = ""          // every title
	AudienceAnime    Audience = "anime"     // anime only
	AudienceNonAnime Audience = "non-anime" // everything but anime
)

// Valid reports whether a is a known audience.
func (a Audience) Valid() bool {
	switch a {
	case AudienceAll, AudienceAnime, AudienceNonAnime:
		return true
	}
	return false
}

// Allows reports whether the preset may rank a title.
func (a Audience) Allows(anime bool) bool {
	switch a {
	case AudienceAnime:
		return anime
	case AudienceNonAnime:
		return !anime
	}
	return true
}

// AudienceForProfileName guesses the audience of an imported profile:
// TRaSH and Sonarr/Radarr anime profiles carry "anime" in their name
// ("[German] Anime HD Bluray + WEB", "[Anime] Remux-1080p").
func AudienceForProfileName(name string) Audience {
	if strings.Contains(strings.ToLower(name), "anime") {
		return AudienceAnime
	}
	return AudienceAll
}

// Preset is a stored quality preset.
type Preset struct {
	ID   string  `json:"id"`
	App  arr.App `json:"app"`
	Name string  `json:"name"`
	// Audience limits the preset to anime or to everything else.
	Audience Audience `json:"audience,omitempty"`
	// Fallback ranks a search this preset passes nothing for: another
	// preset's ID, "simple" for the simple ranking, or "" for none.
	Fallback  string       `json:"fallback,omitempty"`
	Origin    Origin       `json:"origin"`
	Upstream  Upstream     `json:"upstream"`
	Overrides Overrides    `json:"overrides"`
	Sync      SyncSettings `json:"sync"`

	Pending        *Pending        `json:"pending,omitempty"`
	Conflicts      []Conflict      `json:"conflicts,omitempty"`
	ReviewFormats  []string        `json:"reviewFormats,omitempty"`
	Removed        []RemovedFormat `json:"removed,omitempty"`
	History        []HistoryEntry  `json:"history,omitempty"`
	LastCheckedAt  *time.Time      `json:"lastCheckedAt,omitempty"`
	LastCheckError string          `json:"lastCheckError,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MaxHistory is how many applied states are kept for rollback.
const MaxHistory = 10

// Summary is the list view of a preset.
type Summary struct {
	ID            string     `json:"id"`
	App           arr.App    `json:"app"`
	MediaType     string     `json:"mediaType"`
	Name          string     `json:"name"`
	Origin        OriginKind `json:"origin"`
	Commit        string     `json:"commit,omitempty"`
	SyncMode      SyncMode   `json:"syncMode"`
	HasPending    bool       `json:"hasPending"`
	Conflicts     int        `json:"conflicts"`
	ReviewFormats int        `json:"reviewFormats"`
	Audience      Audience   `json:"audience"`
	Fallback      string     `json:"fallback,omitempty"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	// FormatErrors counts formats left out because they failed to compile
	// (set by the API from the compiled preset).
	FormatErrors int `json:"formatErrors,omitempty"`
}

// Summary returns the list view.
func (p *Preset) Summary() Summary {
	return Summary{
		ID: p.ID, App: p.App, MediaType: p.App.MediaType(), Name: p.DisplayName(), Origin: p.Origin.Kind,
		Commit: p.Upstream.Commit, SyncMode: p.Sync.Mode, HasPending: p.Pending != nil,
		Conflicts: len(p.Conflicts), ReviewFormats: len(p.ReviewFormats), Audience: p.Audience, Fallback: p.Fallback, UpdatedAt: p.UpdatedAt,
		LastCheckedAt: p.LastCheckedAt, LastError: p.LastCheckError,
	}
}

// DisplayName is the user's name override or the stored name.
func (p *Preset) DisplayName() string {
	if p.Overrides.Name != nil && *p.Overrides.Name != "" {
		return *p.Overrides.Name
	}
	return p.Name
}

// Redacted returns a copy safe to send to clients (no API key).
func (p *Preset) Redacted() *Preset {
	cp := *p
	if cp.Origin.ArrAPIKey != "" {
		cp.Origin.ArrAPIKey = "********"
	}
	return &cp
}
