package qualityprofiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"novastream/internal/arr"
	"novastream/internal/arr/arrclient"
	"novastream/internal/trash"
)

// ErrNotFound is returned for unknown preset IDs.
var ErrNotFound = errors.New("quality preset not found")

// ErrStorage wraps failures of the preset store (the database), as opposed
// to invalid requests.
var ErrStorage = errors.New("quality preset storage failed")

// SourceProvider returns the configured snapshot sources.
type SourceProvider func() []Source

// Service manages presets.
type Service struct {
	store   Store
	fetcher *Fetcher
	sources SourceProvider
	now     func() time.Time
	arrHTTP func() *arrclient.Client

	mu         sync.Mutex // serializes read-modify-write of presets
	cache      sync.Map   // id -> *Active
	failed     sync.Map   // id -> failedCompile (a preset that did not compile, per generation)
	onChange   func(id string)
	inUse      atomic.Int32  // inUseUnknown, inUseNo or inUseYes
	generation atomic.Uint64 // bumped on every change (see Active)
}

// SetOnChange registers a callback run after a preset is saved or deleted
// (used to clear search and prequeue caches).
func (s *Service) SetOnChange(fn func(id string)) { s.onChange = fn }

// InUse reports whether any preset exists. It is cheap enough for hot
// paths: the answer is kept until a preset is created, changed or deleted.
func (s *Service) InUse(ctx context.Context) bool {
	switch s.inUse.Load() {
	case inUseYes:
		return true
	case inUseNo:
		return false
	}
	gen := s.generation.Load()
	presets, err := s.store.ListPresets(ctx)
	if err != nil {
		return true // unknown: let callers look
	}
	state := int32(inUseNo)
	if len(presets) > 0 {
		state = inUseYes
	}
	s.inUse.Store(state)
	if s.generation.Load() != gen {
		// A preset changed meanwhile (changed bumps the generation before
		// it resets the state): the answer may be stale.
		s.inUse.CompareAndSwap(state, inUseUnknown)
	}
	return state == inUseYes
}

const (
	inUseUnknown int32 = iota
	inUseNo
	inUseYes
)

func (s *Service) changed(id string) {
	s.generation.Add(1)
	s.inUse.Store(inUseUnknown)
	s.cache.Delete(id)
	if s.onChange != nil {
		s.onChange(id)
	}
}

// NewService creates a service. A nil store keeps presets in memory (tests).
func NewService(store Store, fetcher *Fetcher, sources SourceProvider) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	if fetcher == nil {
		fetcher = NewFetcher(store, nil)
	}
	if sources == nil {
		sources = DefaultSources
	}
	return &Service{store: store, fetcher: fetcher, sources: sources, now: time.Now}
}

// SetClock overrides the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Fetcher returns the snapshot fetcher.
func (s *Service) Fetcher() *Fetcher { return s.fetcher }

// Sources returns the configured sources.
func (s *Service) Sources() []Source { return s.sources() }

// List returns every preset.
func (s *Service) List(ctx context.Context) ([]*Preset, error) {
	return s.store.ListPresets(ctx)
}

// Get returns one preset.
func (s *Service) Get(ctx context.Context, id string) (*Preset, error) {
	p, err := s.store.GetPreset(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStorage, err)
	}
	if p == nil {
		return nil, ErrNotFound
	}
	return p, nil
}

func (s *Service) save(ctx context.Context, p *Preset) error {
	return s.persist(ctx, p, true)
}

// persist validates and saves a preset. ranking says whether how it ranks
// changed: only then are UpdatedAt (part of search cache keys), the
// compiled preset and the search caches renewed.
func (s *Service) persist(ctx context.Context, p *Preset, ranking bool) error {
	if _, err := Resolve(p); err != nil {
		return err
	}
	if ranking {
		p.UpdatedAt = s.now()
	}
	if err := s.store.SavePreset(ctx, p); err != nil {
		return fmt.Errorf("%w: %v", ErrStorage, err)
	}
	if ranking {
		s.changed(p.ID)
	}
	return nil
}

// rankingState is everything that changes how a preset ranks or how ranked
// results are labelled. Check times, sync settings, history and upstream
// bookkeeping (commit, sources) are left out.
func rankingState(p *Preset) string {
	up := p.Upstream
	up.Commit, up.FetchedAt, up.Sources, up.FormatSources, up.Shadowed = "", time.Time{}, nil, nil, nil
	return jsonString([]any{p.App, p.Name, p.Audience, p.Fallback, up, p.Overrides, p.ReviewFormats})
}

// Mutate loads a preset, applies fn and saves it if fn succeeds.
func (s *Service) Mutate(ctx context.Context, id string, fn func(p *Preset) error) (*Preset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	before := rankingState(p)
	if err := fn(p); err != nil {
		return nil, err
	}
	if err := s.persist(ctx, p, rankingState(p) != before); err != nil {
		return nil, err
	}
	return p, nil
}

// Delete removes a preset. Presets that fell back to it get no fallback.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if err := s.store.DeletePreset(ctx, id); err != nil {
		return fmt.Errorf("%w: %v", ErrStorage, err)
	}
	s.changed(id)
	if all, err := s.store.ListPresets(ctx); err == nil {
		for _, p := range all {
			if p.Fallback == id {
				p.Fallback = ""
				if err := s.persist(ctx, p, true); err != nil {
					log.Printf("[quality-presets] clearing the fallback of %q: %v", p.DisplayName(), err)
				}
			}
		}
	}
	return nil
}

// SimpleFallback as a preset's fallback ranks with the simple ranking.
const SimpleFallback = "simple"

// MaxFallbacks is how many fallbacks a search tries after its preset.
const MaxFallbacks = 3

// validFallback checks a preset's fallback: none, the simple ranking, or
// another preset of the same app whose own fallbacks do not lead back.
func (s *Service) validFallback(ctx context.Context, p *Preset, fallback string) (string, error) {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return "", nil
	}
	if strings.EqualFold(fallback, SimpleFallback) {
		return SimpleFallback, nil
	}
	if fallback == p.ID {
		return "", fmt.Errorf("a preset cannot fall back to itself")
	}
	next, err := s.Get(ctx, fallback)
	if err != nil {
		return "", fmt.Errorf("fallback preset: %w", err)
	}
	if next.App != p.App {
		return "", fmt.Errorf("the fallback must be a %s preset like %q", p.App, p.DisplayName())
	}
	for hop, cur := 0, next; cur != nil && cur.Fallback != "" && cur.Fallback != SimpleFallback; hop++ {
		if cur.Fallback == p.ID {
			return "", fmt.Errorf("%q already falls back to %q", next.DisplayName(), p.DisplayName())
		}
		if hop >= MaxFallbacks {
			break
		}
		if cur, err = s.Get(ctx, cur.Fallback); err != nil {
			break
		}
	}
	return next.ID, nil
}

func (s *Service) create(ctx context.Context, p *Preset) (*Preset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	p.ID = uuid.NewString()
	p.CreatedAt = now
	if p.Sync.Mode == "" {
		p.Sync = DefaultSyncSettings()
	}
	// Sync settings given on import: fill what is left out, refuse what is
	// invalid (a preset with an unknown schedule would never be checked).
	def := DefaultSyncSettings()
	if p.Sync.Schedule == "" {
		p.Sync.Schedule = def.Schedule
	}
	if p.Sync.NewFormats == "" {
		p.Sync.NewFormats = def.NewFormats
	}
	if p.Origin.Kind == OriginManual {
		p.Sync.Mode = SyncManual
		p.Sync.Schedule = ScheduleManual
	}
	if err := ValidateSync(p.Sync); err != nil {
		return nil, err
	}
	if err := s.save(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// TrashCatalog lists the profiles and groups the merged sources offer.
type TrashCatalog struct {
	Commit   string                       `json:"commit"`
	Warnings []string                     `json:"warnings,omitempty"`
	Profiles map[arr.App][]CatalogProfile `json:"profiles"`
}

// CatalogProfile describes one importable TRaSH profile.
type CatalogProfile struct {
	TrashID     string   `json:"trashId"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Group       string   `json:"group,omitempty"`
	ScoreSet    string   `json:"scoreSet,omitempty"`
	ScoreSets   []string `json:"scoreSets"`
	Groups      int      `json:"groups"`
	Formats     int      `json:"formats"`
}

// Catalog returns the importable TRaSH profiles.
func (s *Service) Catalog(ctx context.Context) (*TrashCatalog, error) {
	snap, warnings, err := s.fetcher.MergedSnapshot(ctx, s.sources(), nil)
	if err != nil {
		return nil, err
	}
	if len(snap.Skipped) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d TRaSH file(s) could not be read and are left out: %s", len(snap.Skipped), strings.Join(snap.Skipped, "; ")))
	}
	out := &TrashCatalog{Commit: snap.Commit, Warnings: warnings, Profiles: map[arr.App][]CatalogProfile{}}
	for _, app := range []arr.App{arr.AppSonarr, arr.AppRadarr} {
		data := snap.App(app)
		groupName := map[string]string{}
		for _, pg := range data.ProfileGroups {
			for _, id := range pg.Profiles {
				groupName[id] = pg.Name
			}
		}
		sets := map[string]bool{}
		for _, cf := range data.CustomFormats {
			for k := range cf.TrashScores {
				sets[k] = true
			}
		}
		setList := make([]string, 0, len(sets))
		for k := range sets {
			setList = append(setList, k)
		}
		sort.Strings(setList)
		for _, qp := range snap.ProfileList(app) {
			out.Profiles[app] = append(out.Profiles[app], CatalogProfile{
				TrashID: qp.TrashID, Name: qp.Name, Description: stripHTML(qp.TrashDescription), Group: groupName[qp.TrashID],
				ScoreSet: qp.TrashScoreSet, ScoreSets: setList, Groups: len(snap.GroupsFor(app, qp.TrashID)), Formats: len(qp.FormatItems),
			})
		}
	}
	return out, nil
}

func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// TrashImport describes a TRaSH profile import.
type TrashImport struct {
	App            arr.App                      `json:"app"`
	ProfileTrashID string                       `json:"profileTrashId"`
	Name           string                       `json:"name,omitempty"`
	ScoreSet       string                       `json:"scoreSet,omitempty"`
	QualitySize    string                       `json:"qualitySize,omitempty"`
	Groups         map[string]trash.GroupChoice `json:"groups,omitempty"`
	Sync           *SyncSettings                `json:"sync,omitempty"`
}

// ImportTrash creates a preset from a TRaSH profile.
func (s *Service) ImportTrash(ctx context.Context, in TrashImport) (*Preset, []string, error) {
	if !in.App.Valid() {
		return nil, nil, fmt.Errorf("app must be sonarr or radarr")
	}
	snap, warnings, err := s.fetcher.MergedSnapshot(ctx, s.sources(), nil)
	if err != nil {
		return nil, nil, err
	}
	origin := Origin{Kind: OriginTrash, ProfileTrashID: in.ProfileTrashID, ScoreSet: in.ScoreSet, QualitySize: in.QualitySize}
	up, name, missing, err := trashUpstream(snap, in.App, origin, s.now())
	warnings = append(warnings, missing...)
	if err != nil {
		return nil, warnings, err
	}
	audience := AudienceForProfileName(name)
	if in.Name != "" {
		name = in.Name
	}
	p := &Preset{App: in.App, Name: name, Audience: audience, Origin: origin, Upstream: up}
	if len(in.Groups) > 0 {
		p.Overrides.Groups = in.Groups
		for id := range in.Groups {
			recordBase(p, "group:"+id, false)
		}
	}
	if in.Sync != nil {
		p.Sync = *in.Sync
	}
	created, err := s.create(ctx, p)
	return created, warnings, err
}

// ArrImport describes an import from a Sonarr/Radarr instance.
type ArrImport struct {
	URL       string        `json:"url"`
	APIKey    string        `json:"apiKey"`
	ProfileID int           `json:"profileId"`
	Name      string        `json:"name,omitempty"`
	Sync      *SyncSettings `json:"sync,omitempty"`
}

// ArrProfiles lists the quality profiles of an instance.
func (s *Service) ArrProfiles(ctx context.Context, rawURL, apiKey string) (arr.App, []arrclient.RemoteProfile, error) {
	client, err := arrclient.New(rawURL, apiKey, s.fetcher.HTTP)
	if err != nil {
		return "", nil, err
	}
	status, err := client.SystemStatus(ctx)
	if err != nil {
		return "", nil, err
	}
	app, err := status.App()
	if err != nil {
		return "", nil, err
	}
	profiles, err := client.ListQualityProfiles(ctx)
	return app, profiles, err
}

// ImportArr creates a preset from an instance's quality profile.
func (s *Service) ImportArr(ctx context.Context, in ArrImport) (*Preset, error) {
	client, err := arrclient.New(in.URL, in.APIKey, s.fetcher.HTTP)
	if err != nil {
		return nil, err
	}
	up, name, app, err := arrUpstream(ctx, client, in.ProfileID, s.now())
	if err != nil {
		return nil, err
	}
	audience := AudienceForProfileName(name)
	if in.Name != "" {
		name = in.Name
	}
	p := &Preset{App: app, Name: name, Audience: audience, Origin: Origin{Kind: OriginArr, ArrURL: client.BaseURL, ArrAPIKey: in.APIKey, ArrProfileID: in.ProfileID}, Upstream: up}
	if in.Sync != nil {
		p.Sync = *in.Sync
	}
	return s.create(ctx, p)
}

// ImportJSON creates a manual preset from pasted JSON.
func (s *Service) ImportJSON(ctx context.Context, data []byte, fallbackApp arr.App, name string) (*Preset, error) {
	up, ov, docName, app, err := parseImport(data, fallbackApp, s.now())
	if err != nil {
		return nil, err
	}
	// An exported preset says its audience; a TRaSH profile is tagged by name.
	audience := importAudience(data, docName)
	if name == "" {
		name = docName
	}
	if name == "" {
		name = "Imported preset"
	}
	return s.create(ctx, &Preset{App: app, Name: name, Audience: audience, Origin: Origin{Kind: OriginManual}, Upstream: up, Overrides: ov})
}

// heldFormatsSwitchedOff returns a copy of p's overrides in which formats
// still waiting for review are switched off. Copies and exports carry no
// review queue, and a format the user has not approved must not become
// active on the way.
func heldFormatsSwitchedOff(p *Preset) Overrides {
	data, _ := json.Marshal(p.Overrides)
	var ov Overrides
	_ = json.Unmarshal(data, &ov)
	for _, k := range p.ReviewFormats {
		if _, set := ov.Disabled[k]; set {
			continue
		}
		if ov.Disabled == nil {
			ov.Disabled = map[string]bool{}
		}
		ov.Disabled[k] = true
	}
	return ov
}

// Duplicate copies a preset (as a manual preset that no longer syncs).
func (s *Service) Duplicate(ctx context.Context, id, name string) (*Preset, error) {
	src, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(src)
	var cp Preset
	_ = json.Unmarshal(data, &cp)
	cp.Name = name
	cp.Origin = Origin{Kind: OriginManual}
	cp.Overrides = heldFormatsSwitchedOff(src)
	cp.Overrides.Name = nil
	cp.Pending, cp.Conflicts, cp.History, cp.Removed, cp.ReviewFormats = nil, nil, nil, nil, nil
	cp.LastCheckedAt, cp.LastCheckError = nil, ""
	if cp.Name == "" {
		cp.Name = src.DisplayName() + " (copy)"
	}
	return s.create(ctx, &cp)
}

// SetOverride sets one override (see setOverride for paths) and records
// the upstream value it replaces.
func (s *Service) SetOverride(ctx context.Context, id, path string, value json.RawMessage) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		if err := setOverride(p, path, value); err != nil {
			return err
		}
		recordBase(p, path, false)
		return nil
	})
}

// ClearOverride reverts one override to upstream.
func (s *Service) ClearOverride(ctx context.Context, id, path string) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		clearOverride(p, path)
		return nil
	})
}

// AddFormat adds an own custom format with a score.
func (s *Service) AddFormat(ctx context.Context, id string, cf arr.CustomFormat, score int) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		if _, err := arr.Compile(p.App, cf); err != nil {
			return err
		}
		if cf.TrashID == "" {
			cf.TrashID = "own-" + uuid.NewString()[:8]
		}
		key := cf.Key()
		if _, exists := p.Upstream.Formats[key]; exists {
			return fmt.Errorf("format %q already exists upstream; edit it instead", cf.Name)
		}
		if p.Overrides.Formats == nil {
			p.Overrides.Formats = map[string]arr.CustomFormat{}
		}
		if p.Overrides.Scores == nil {
			p.Overrides.Scores = map[string]int{}
		}
		p.Overrides.Formats[key] = cf
		p.Overrides.Scores[key] = score
		return nil
	})
}

// RemoveFormat deletes an own format, or reverts an edited upstream format.
func (s *Service) RemoveFormat(ctx context.Context, id, key string) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		if _, upstream := p.Upstream.Formats[key]; upstream {
			clearOverride(p, "format:"+key)
			return nil
		}
		delete(p.Overrides.Formats, key)
		delete(p.Overrides.Scores, key)
		delete(p.Overrides.Disabled, key)
		return nil
	})
}

// SetDisabled enables or disables a format.
func (s *Service) SetDisabled(ctx context.Context, id, key string, disabled bool) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		if p.Overrides.Disabled == nil {
			p.Overrides.Disabled = map[string]bool{}
		}
		if disabled {
			p.Overrides.Disabled[key] = true
		} else {
			delete(p.Overrides.Disabled, key)
		}
		return nil
	})
}

// UpdateSettings replaces the sync settings and plain options.
type SettingsUpdate struct {
	Name              *string          `json:"name,omitempty"`
	Sync              *SyncSettings    `json:"sync,omitempty"`
	EnforceSizeLimits *bool            `json:"enforceSizeLimits,omitempty"`
	PreferredProtocol *arr.Protocol    `json:"preferredProtocol,omitempty"`
	MultiLanguages    *[]arr.Language  `json:"multiLanguages,omitempty"`
	Propers           *arr.PropersMode `json:"propers,omitempty"`
	Audience          *Audience        `json:"audience,omitempty"`
	// Fallback is another preset of the same app, "simple" or "" (none).
	Fallback *string `json:"fallback,omitempty"`
	// Connection moves a Sonarr/Radarr preset to another URL or API key; an
	// empty API key keeps the stored one.
	Connection *ArrConnection `json:"connection,omitempty"`
}

// ArrConnection is a Sonarr/Radarr instance address.
type ArrConnection struct {
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
}

// ValidateSync checks sync settings.
func ValidateSync(s SyncSettings) error {
	switch s.Mode {
	case SyncAuto, SyncNotify, SyncManual, SyncPinned:
	default:
		return fmt.Errorf("invalid sync mode %q", s.Mode)
	}
	if s.Mode == SyncPinned && strings.TrimSpace(s.PinnedCommit) == "" {
		return fmt.Errorf("pinned mode needs a commit")
	}
	switch s.Schedule {
	case ScheduleDaily, ScheduleWeekly, ScheduleManual:
	default:
		return fmt.Errorf("invalid schedule %q", s.Schedule)
	}
	switch s.NewFormats {
	case NewFormatsEnable, NewFormatsDisable, NewFormatsReview:
	default:
		return fmt.Errorf("invalid new-format policy %q", s.NewFormats)
	}
	return nil
}

// UpdateSettings applies a settings update.
func (s *Service) UpdateSettings(ctx context.Context, id string, in SettingsUpdate) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if name == "" {
				return fmt.Errorf("name must not be empty")
			}
			p.Overrides.Name = &name
		}
		if in.Sync != nil {
			if err := ValidateSync(*in.Sync); err != nil {
				return err
			}
			if p.Origin.Kind == OriginManual && in.Sync.Mode != SyncManual {
				return fmt.Errorf("manual presets have no upstream to sync")
			}
			p.Sync = *in.Sync
		}
		if in.EnforceSizeLimits != nil {
			p.Overrides.EnforceSizeLimits = in.EnforceSizeLimits
		}
		if in.PreferredProtocol != nil {
			switch *in.PreferredProtocol {
			case arr.ProtocolUnknown, arr.ProtocolUsenet, arr.ProtocolTorrent:
			default:
				return fmt.Errorf("invalid protocol %q", *in.PreferredProtocol)
			}
			p.Overrides.PreferredProtocol = in.PreferredProtocol
		}
		if in.MultiLanguages != nil {
			p.Overrides.MultiLanguages = *in.MultiLanguages
		}
		if in.Propers != nil {
			// Like the override endpoint: remember upstream's value so a later
			// upstream change is reported as a conflict.
			recordBase(p, "propers", false)
			p.Overrides.Propers = in.Propers
		}
		if in.Audience != nil {
			if !in.Audience.Valid() {
				return fmt.Errorf("invalid audience %q", *in.Audience)
			}
			p.Audience = *in.Audience
		}
		if in.Fallback != nil {
			fallback, err := s.validFallback(ctx, p, *in.Fallback)
			if err != nil {
				return err
			}
			p.Fallback = fallback
		}
		if in.Connection != nil {
			if p.Origin.Kind != OriginArr {
				return fmt.Errorf("only Sonarr/Radarr presets have a connection")
			}
			key := strings.TrimSpace(in.Connection.APIKey)
			client, err := arrclient.New(in.Connection.URL, firstNonEmptyKey(key, p.Origin.ArrAPIKey), nil)
			if err != nil {
				return err
			}
			if key == "" && !sameArrOrigin(client.BaseURL, p.Origin.ArrURL) {
				// The stored key is sent only to the server it was entered
				// for: a new address needs the key again.
				return fmt.Errorf("enter the API key again for a new Sonarr/Radarr address")
			}
			p.Origin.ArrURL, p.Origin.ArrAPIKey = client.BaseURL, client.APIKey
		}
		return nil
	})
}

// ReviewFormats approves (enable=true) or rejects pending new formats.
func (s *Service) ReviewNewFormats(ctx context.Context, id string, keys []string, enable bool) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		drop := map[string]bool{}
		for _, k := range keys {
			drop[k] = true
		}
		var keep []string
		for _, k := range p.ReviewFormats {
			if !drop[k] {
				keep = append(keep, k)
			}
		}
		p.ReviewFormats = keep
		if !enable {
			if p.Overrides.Disabled == nil {
				p.Overrides.Disabled = map[string]bool{}
			}
			for k := range drop {
				p.Overrides.Disabled[k] = true
			}
		}
		return nil
	})
}

// fetchUpstream gets the current source state of a preset.
func (s *Service) fetchUpstream(ctx context.Context, p *Preset) (Upstream, []string, error) {
	switch p.Origin.Kind {
	case OriginTrash:
		var commits map[string]string
		if p.Sync.Mode == SyncPinned {
			commits = ParseCommits(p.Sync.PinnedCommit, s.sources())
		}
		snap, warnings, err := s.fetcher.MergedSnapshotStrict(ctx, s.sources(), commits)
		if err != nil {
			return Upstream{}, warnings, err
		}
		up, _, missing, err := trashUpstream(snap, p.App, p.Origin, s.now())
		warnings = append(warnings, missing...)
		return up, warnings, err
	case OriginArr:
		client, err := arrclient.New(p.Origin.ArrURL, p.Origin.ArrAPIKey, s.fetcher.HTTP)
		if err != nil {
			return Upstream{}, nil, err
		}
		up, _, app, err := arrUpstream(ctx, client, p.Origin.ArrProfileID, s.now())
		if err == nil && app != p.App {
			err = fmt.Errorf("instance is %s but the preset is %s", app, p.App)
		}
		return up, nil, err
	}
	return Upstream{}, nil, fmt.Errorf("manual presets have no upstream")
}

// CheckResult reports what an update check did.
type CheckResult struct {
	Preset   *Preset  `json:"preset"`
	Plan     *Plan    `json:"plan,omitempty"`
	Applied  bool     `json:"applied"`
	Staged   bool     `json:"staged"`
	UpToDate bool     `json:"upToDate"`
	Warnings []string `json:"warnings,omitempty"`
}

// Check fetches upstream and applies or stages the update according to
// the preset's sync mode. apply=true forces applying (a "sync now").
func (s *Service) Check(ctx context.Context, id string, apply bool) (*CheckResult, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Origin.Kind == OriginManual {
		return nil, fmt.Errorf("manual presets have no upstream to sync")
	}
	fetched, warnings, ferr := s.fetchUpstream(ctx, p)
	res := &CheckResult{Warnings: warnings}
	fetchedFor, fetchedFrom := p.Sync, p.Origin
	updated, err := s.Mutate(ctx, id, func(p *Preset) error {
		now := s.now()
		if p.Origin != fetchedFrom {
			// The connection changed while fetching: neither what was
			// fetched nor its error is about the new one.
			ferr = nil
			res.Warnings = append(res.Warnings, "the preset's source changed during the check; nothing applied")
			return nil
		}
		p.LastCheckedAt = &now
		if ferr != nil {
			p.LastCheckError = ferr.Error()
			return nil
		}
		if p.Sync.Mode != fetchedFor.Mode || p.Sync.PinnedCommit != fetchedFor.PinnedCommit {
			// Pinned or unpinned while fetching: what was fetched may be the
			// wrong version; the next check uses the new settings.
			res.Warnings = append(res.Warnings, "sync settings changed during the check; nothing applied")
			return nil
		}
		p.LastCheckError = ""
		plan := PlanUpdate(p, fetched, now)
		res.Plan = plan
		if plan.Empty() {
			// Nothing this preset uses changed: record the new commit
			// without a history entry or a pending update.
			res.UpToDate = true
			p.Pending = nil
			p.Upstream = plan.Next
			return nil
		}
		switch {
		case apply || p.Sync.Mode == SyncAuto || p.Sync.Mode == SyncPinned:
			ApplyPlan(p, plan, "before sync to "+short(plan.ToCommit), now)
			res.Applied = true
		default:
			p.Pending = &Pending{FromCommit: plan.FromCommit, ToCommit: plan.ToCommit, CheckedAt: now, Upstream: plan.Next,
				Changes: plan.Changes, Conflicts: plan.Conflicts, NewFormats: plan.NewFormats}
			res.Staged = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Preset = updated
	if ferr != nil {
		return res, ferr
	}
	return res, nil
}

// ApplyPending accepts a staged update.
func (s *Service) ApplyPending(ctx context.Context, id string) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		if p.Pending == nil {
			return fmt.Errorf("no pending update")
		}
		plan := PlanUpdate(p, p.Pending.Upstream, s.now())
		ApplyPlan(p, plan, "before approved update to "+short(plan.ToCommit), s.now())
		return nil
	})
}

// DiscardPending drops a staged update.
func (s *Service) DiscardPending(ctx context.Context, id string) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		p.Pending = nil
		return nil
	})
}

// ResolveConflict applies a conflict choice.
func (s *Service) ResolveConflict(ctx context.Context, id, path string, choice ConflictChoice, value json.RawMessage) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		return ResolveConflict(p, path, choice, value, s.now())
	})
}

// Rollback restores a history entry; pin=true also pins the preset to the
// restored commit so the next sync does not undo the rollback.
func (s *Service) Rollback(ctx context.Context, id, historyID string, pin bool) (*Preset, error) {
	return s.Mutate(ctx, id, func(p *Preset) error {
		if err := Rollback(p, historyID, s.now()); err != nil {
			return err
		}
		if pin && p.Origin.Kind != OriginManual {
			p.Sync.Mode = SyncPinned
			p.Sync.PinnedCommit = p.Upstream.Commit
		}
		return nil
	})
}

// due reports whether a preset's schedule asks for a check at now.
func due(p *Preset, now time.Time) bool {
	if p.Origin.Kind == OriginManual || p.Sync.Mode == SyncManual {
		return false
	}
	var every time.Duration
	switch p.Sync.Schedule {
	case ScheduleDaily:
		every = 24 * time.Hour
	case ScheduleWeekly:
		every = 7 * 24 * time.Hour
	default:
		return false
	}
	if p.Sync.Mode == SyncPinned && p.Upstream.Commit != "" && p.Upstream.Commit == p.Sync.PinnedCommit {
		return false
	}
	return p.LastCheckedAt == nil || now.Sub(*p.LastCheckedAt) >= every-time.Minute
}

// SyncDueResult summarizes a scheduled run.
type SyncDueResult struct {
	Checked int      `json:"checked"`
	Applied int      `json:"applied"`
	Staged  int      `json:"staged"`
	Errors  []string `json:"errors,omitempty"`
}

// SyncDue checks every preset whose schedule is due (scheduler entry
// point). force checks every syncing preset regardless of schedule.
func (s *Service) SyncDue(ctx context.Context, force bool) (*SyncDueResult, error) {
	presets, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := &SyncDueResult{}
	now := s.now()
	for _, p := range presets {
		if p.Origin.Kind == OriginManual {
			continue
		}
		if !force && !due(p, now) {
			continue
		}
		out.Checked++
		res, err := s.Check(ctx, p.ID, false)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", p.DisplayName(), err))
			continue
		}
		if res.Applied {
			out.Applied++
		}
		if res.Staged {
			out.Staged++
		}
	}
	if len(out.Errors) > 0 {
		log.Printf("[quality-presets] sync: %d checked, %d errors: %s", out.Checked, len(out.Errors), strings.Join(out.Errors, "; "))
	}
	return out, nil
}

// Active is a compiled preset ready for search.
type Active struct {
	PresetID string
	Name     string
	App      arr.App
	Audience Audience
	// Fallback is the preset ID, "simple" or "" to rank with when this
	// preset passes nothing.
	Fallback  string
	UpdatedAt time.Time
	Resolved  *Resolved
	Evaluator *arr.Evaluator
	Errors    []string
}

// Active returns the compiled preset, cached until it changes.
func (s *Service) Active(ctx context.Context, id string) (*Active, error) {
	if v, ok := s.cache.Load(id); ok {
		return v.(*Active), nil
	}
	// A change saved while this compiles must not be overwritten by the
	// stale result: store it only if no change happened in between.
	gen := s.generation.Load()
	if v, ok := s.failed.Load(id); ok && v.(failedCompile).gen == gen {
		// Failed to compile and unchanged since: not compiled again for
		// every search.
		return nil, v.(failedCompile).err
	}
	fail := func(err error) (*Active, error) {
		if s.generation.Load() == gen {
			s.failed.Store(id, failedCompile{gen: gen, err: err})
		}
		return nil, err
	}
	// Unknown IDs are not cached: requests can name any string.
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	res, err := Resolve(p)
	if err != nil {
		return fail(err)
	}
	ev, errs := res.Evaluator()
	if ev == nil {
		return fail(fmt.Errorf("preset %q: %v", p.DisplayName(), errs))
	}
	a := &Active{PresetID: p.ID, Name: p.DisplayName(), App: p.App, Audience: p.Audience, Fallback: p.Fallback, UpdatedAt: p.UpdatedAt, Resolved: res, Evaluator: ev}
	for _, e := range errs {
		a.Errors = append(a.Errors, e.Error())
	}
	if len(a.Errors) > 0 {
		// Logged once per preset version: a left-out format (an LQ format
		// with a negative score, say) changes what passes.
		log.Printf("[quality-presets] %q ranks without %d custom format(s) that failed to compile: %s", a.Name, len(a.Errors), strings.Join(a.Errors, "; "))
	}
	if s.generation.Load() == gen {
		s.cache.Store(id, a)
		if s.generation.Load() != gen {
			// A change landed between the check and the store (changed
			// bumps the generation before it drops the cache entry).
			s.cache.CompareAndDelete(id, a)
		}
	}
	return a, nil
}

// TitleResult is the evaluation of one sample title.
type TitleResult struct {
	Title      string             `json:"title"`
	Rank       int                `json:"rank"`
	Evaluation arr.Evaluation     `json:"evaluation"`
	Formats    []arr.FormatResult `json:"conditions,omitempty"`
}

// Explain evaluates sample titles with a preset, best first.
func (s *Service) Explain(ctx context.Context, id string, titles []string, rc arr.ReleaseContext, detail bool) ([]TitleResult, error) {
	a, err := s.Active(ctx, id)
	if err != nil {
		return nil, err
	}
	return explainWith(ctx, a.Evaluator, a.App, titles, rc, detail), nil
}

// explainWith ranks sample titles; it stops early when ctx ends.
func explainWith(ctx context.Context, ev *arr.Evaluator, app arr.App, titles []string, rc arr.ReleaseContext, detail bool) []TitleResult {
	out := make([]TitleResult, 0, len(titles))
	for _, t := range titles {
		if ctx.Err() != nil {
			break
		}
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		rel := ev.Prepare(arr.Parse(app, t), rc)
		r := TitleResult{Title: t, Evaluation: ev.Evaluate(rel)}
		if detail {
			r.Formats = ev.Explain(rel)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Evaluation, out[j].Evaluation
		if a.Allowed != b.Allowed {
			return a.Allowed
		}
		return ev.Compare(a, b) < 0
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// DryRunResult compares rankings before and after an update.
type DryRunResult struct {
	Plan   *Plan         `json:"plan"`
	Before []TitleResult `json:"before"`
	After  []TitleResult `json:"after"`
}

// DryRun shows what the pending (or a freshly fetched) update would change
// for sample titles, without saving anything.
func (s *Service) DryRun(ctx context.Context, id string, titles []string, rc arr.ReleaseContext) (*DryRunResult, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var fetched Upstream
	if p.Pending != nil {
		fetched = p.Pending.Upstream
	} else {
		fetched, _, err = s.fetchUpstream(ctx, p)
		if err != nil {
			return nil, err
		}
	}
	plan := PlanUpdate(p, fetched, s.now())
	before, err := Resolve(p)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(p)
	var next Preset
	_ = json.Unmarshal(data, &next)
	ApplyPlan(&next, plan, "dry run", s.now())
	after, err := Resolve(&next)
	if err != nil {
		return nil, err
	}
	evBefore, _ := before.Evaluator()
	evAfter, _ := after.Evaluator()
	return &DryRunResult{Plan: plan, Before: explainWith(ctx, evBefore, p.App, titles, rc, false), After: explainWith(ctx, evAfter, p.App, titles, rc, false)}, nil
}

func firstNonEmptyKey(keys ...string) string {
	for _, k := range keys {
		if k != "" {
			return k
		}
	}
	return ""
}

// sameArrOrigin reports whether two Sonarr/Radarr base URLs name the same
// server (scheme, host and port).
func sameArrOrigin(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Hostname(), ub.Hostname()) && port(ua) == port(ub)
}

// failedCompile remembers a preset that did not compile, until the next
// change (generation).
type failedCompile struct {
	gen uint64
	err error
}
