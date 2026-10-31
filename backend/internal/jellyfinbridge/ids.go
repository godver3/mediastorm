package jellyfinbridge

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Jellyfin item IDs must be GUIDs, but mediastorm identifies things by provider
// strings ("tmdb:movie:603", "tmdb:tv:1399:s01e02", channel IDs, ...). Every
// item the bridge emits gets a deterministic UUIDv5 of (kind, source), so the
// same title always has the same GUID, and a reverse index (persisted to disk)
// maps GUIDs back to the source plus enough context to rebuild the item
// without searching again.

// Item kinds.
const (
	refUser     = "user"
	refView     = "view"
	refMovie    = "movie"
	refSeries   = "series"
	refSeason   = "season"
	refEpisode  = "episode"
	refList     = "list"
	refLocalLib = "locallib"
	refLocal    = "local"
	refChannel  = "channel"
	refProgram  = "program"
)

var idNamespace = uuid.MustParse("8f0c8b8e-6a55-4e0e-9c0a-6d5c1f6a7b21")

// deriveID returns the stable 32-hex Jellyfin ID for (kind, source).
func deriveID(kind, source string) string {
	u := uuid.NewSHA1(idNamespace, []byte(kind+"\x00"+source))
	return formatN(u)
}

func formatN(u uuid.UUID) string {
	return strings.ReplaceAll(u.String(), "-", "")
}

// normalizeID accepts dashed or N-format GUIDs and returns the N form, or "".
func normalizeID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := uuid.Parse(raw)
	if err != nil {
		return ""
	}
	return formatN(u)
}

// refContext carries what the bridge needs to rebuild an item from its GUID.
type refContext struct {
	Name           string            `json:"n,omitempty"`
	Year           int               `json:"y,omitempty"`
	TitleID        string            `json:"t,omitempty"` // mediastorm title ID (movie or series)
	ExternalIDs    map[string]string `json:"x,omitempty"`
	SeriesName     string            `json:"sn,omitempty"`
	Season         int               `json:"s,omitempty"`
	Episode        int               `json:"e,omitempty"`
	Poster         string            `json:"p,omitempty"`
	SeriesPoster   string            `json:"sp,omitempty"` // parent artwork, separate from a season poster or episode still
	SeasonOrdering string            `json:"so,omitempty"`
	Backdrop       string            `json:"b,omitempty"`
	Logo           string            `json:"l,omitempty"`
	Thumb          string            `json:"th,omitempty"`
	ProfileID      string            `json:"pr,omitempty"`
	ViewKey        string            `json:"v,omitempty"`
	Extra          string            `json:"ex,omitempty"` // kind-specific (list ID, channel URL, ...)
}

type itemRef struct {
	Kind   string     `json:"k"`
	Source string     `json:"s"`
	Ctx    refContext `json:"c"`
	Seen   int64      `json:"t,omitempty"`
}

// idStore is the GUID reverse index. It is safe for concurrent use and flushes
// to disk in the background when dirty.
type idStore struct {
	mu    sync.RWMutex
	refs  map[string]itemRef
	path  string
	dirty bool
	stop  chan struct{}
	done  chan struct{}
}

func newIDStore(path string) *idStore {
	s := &idStore{refs: map[string]itemRef{}, path: path, stop: make(chan struct{}), done: make(chan struct{})}
	if path != "" {
		if err := s.load(); err != nil {
			log.Printf("[jellyfin-bridge] id index load failed (%v); starting empty", err)
		}
	}
	go s.flushLoop()
	return s
}

// put registers a ref and returns its GUID. Context from later calls replaces
// earlier context field-by-field only where the new value is non-empty, so a
// sparse ref (e.g. from a progress list) never erases richer details.
func (s *idStore) put(kind, source string, ctx refContext) string {
	id := deriveID(kind, source)
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	persistent := kind != refProgram // programme refs are memory-only
	existing, ok := s.refs[id]
	if !ok {
		s.refs[id] = itemRef{Kind: kind, Source: source, Ctx: ctx, Seen: now}
		s.dirty = s.dirty || persistent
		return id
	}
	merged := fillContext(existing.Ctx, ctx)
	if !contextEqual(merged, existing.Ctx) || now-existing.Seen > 86400 {
		existing.Ctx = merged
		existing.Seen = now
		s.refs[id] = existing
		s.dirty = s.dirty || persistent
	}
	return id
}

// putTrusted registers a ref whose context comes from the metadata service;
// unlike put, it replaces existing values. Refs are shared by every profile,
// so context from user-supplied records (watchlists, lists) may only fill
// gaps (put) and never override what other accounts see.
func (s *idStore) putTrusted(kind, source string, ctx refContext) string {
	id := deriveID(kind, source)
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.refs[id]
	if !ok {
		s.refs[id] = itemRef{Kind: kind, Source: source, Ctx: ctx, Seen: now}
		s.dirty = true
		return id
	}
	merged := mergeContext(existing.Ctx, ctx)
	if !contextEqual(merged, existing.Ctx) || now-existing.Seen > 86400 {
		existing.Ctx = merged
		existing.Seen = now
		s.refs[id] = existing
		s.dirty = true
	}
	return id
}

// refreshEpisode updates a stored episode's title and still from the
// metadata service. Unlike putTrusted it leaves the series details alone:
// an episode can be listed from a profile's watch history, and refs are
// shared by every profile, so that part may only fill gaps.
func (s *idStore) refreshEpisode(id, name, thumb string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.refs[id]
	if !ok {
		return
	}
	changed := false
	if name != "" && !placeholderName(name) && ref.Ctx.Name != name {
		ref.Ctx.Name, changed = name, true
	}
	if thumb != "" && ref.Ctx.Thumb != thumb {
		ref.Ctx.Thumb, changed = thumb, true
	}
	if changed {
		s.refs[id] = ref
		s.dirty = true
	}
}

// placeholderName reports titles metadata providers use for episodes that
// have no name yet.
func placeholderName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "TBA", "TBD", "TBC":
		return true
	}
	return false
}

// fillContext copies only fields that are empty in base (a placeholder
// title counts as empty).
func fillContext(base, in refContext) refContext {
	out := base
	fill := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	// A placeholder title from before an episode aired is no real value.
	if placeholderName(out.Name) && in.Name != "" {
		out.Name = in.Name
	}
	fill(&out.Name, in.Name)
	fill(&out.TitleID, in.TitleID)
	fill(&out.SeriesName, in.SeriesName)
	fill(&out.Poster, in.Poster)
	fill(&out.SeriesPoster, in.SeriesPoster)
	fill(&out.SeasonOrdering, in.SeasonOrdering)
	fill(&out.Backdrop, in.Backdrop)
	fill(&out.Logo, in.Logo)
	fill(&out.Thumb, in.Thumb)
	fill(&out.ProfileID, in.ProfileID)
	fill(&out.ViewKey, in.ViewKey)
	fill(&out.Extra, in.Extra)
	if out.Year == 0 {
		out.Year = in.Year
	}
	if out.Season == 0 {
		out.Season = in.Season
	}
	if out.Episode == 0 {
		out.Episode = in.Episode
	}
	copied := false // base's map is shared with the stored ref; copy before writing
	for k, v := range in.ExternalIDs {
		if v == "" || out.ExternalIDs[k] != "" {
			continue
		}
		if !copied {
			ext := make(map[string]string, len(out.ExternalIDs)+len(in.ExternalIDs))
			for bk, bv := range out.ExternalIDs {
				ext[bk] = bv
			}
			out.ExternalIDs = ext
			copied = true
		}
		out.ExternalIDs[k] = v
	}
	return out
}

func (s *idStore) get(id string) (itemRef, bool) {
	id = normalizeID(id)
	if id == "" {
		return itemRef{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.refs[id]
	return ref, ok
}

func mergeContext(base, in refContext) refContext {
	out := base
	// A placeholder never replaces a real title (stale continue-watching
	// data can still say "TBA").
	if in.Name != "" && (!placeholderName(in.Name) || base.Name == "") {
		out.Name = in.Name
	}
	if in.Year != 0 {
		out.Year = in.Year
	}
	if in.TitleID != "" {
		out.TitleID = in.TitleID
	}
	if len(in.ExternalIDs) > 0 {
		merged := make(map[string]string, len(base.ExternalIDs)+len(in.ExternalIDs))
		for k, v := range base.ExternalIDs {
			merged[k] = v
		}
		for k, v := range in.ExternalIDs {
			if v != "" {
				merged[k] = v
			}
		}
		out.ExternalIDs = merged
	}
	if in.SeriesName != "" {
		out.SeriesName = in.SeriesName
	}
	if in.Season != 0 {
		out.Season = in.Season
	}
	if in.Episode != 0 {
		out.Episode = in.Episode
	}
	if in.Poster != "" {
		out.Poster = in.Poster
	}
	if in.SeriesPoster != "" {
		out.SeriesPoster = in.SeriesPoster
	}
	if in.SeasonOrdering != "" {
		out.SeasonOrdering = in.SeasonOrdering
	}
	if in.Backdrop != "" {
		out.Backdrop = in.Backdrop
	}
	if in.Logo != "" {
		out.Logo = in.Logo
	}
	if in.Thumb != "" {
		out.Thumb = in.Thumb
	}
	if in.ProfileID != "" {
		out.ProfileID = in.ProfileID
	}
	if in.ViewKey != "" {
		out.ViewKey = in.ViewKey
	}
	if in.Extra != "" {
		out.Extra = in.Extra
	}
	return out
}

func contextEqual(a, b refContext) bool {
	if a.Name != b.Name || a.Year != b.Year || a.TitleID != b.TitleID || a.SeriesName != b.SeriesName ||
		a.Season != b.Season || a.Episode != b.Episode || a.Poster != b.Poster || a.SeriesPoster != b.SeriesPoster || a.SeasonOrdering != b.SeasonOrdering || a.Backdrop != b.Backdrop ||
		a.Logo != b.Logo || a.Thumb != b.Thumb || a.ProfileID != b.ProfileID || a.ViewKey != b.ViewKey ||
		a.Extra != b.Extra || len(a.ExternalIDs) != len(b.ExternalIDs) {
		return false
	}
	for k, v := range a.ExternalIDs {
		if b.ExternalIDs[k] != v {
			return false
		}
	}
	return true
}

func (s *idStore) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var refs map[string]itemRef
	if err := json.Unmarshal(data, &refs); err != nil {
		return err
	}
	s.refs = refs
	if s.refs == nil {
		s.refs = map[string]itemRef{}
	}
	return nil
}

// maxRefAge bounds the index: refs not seen for this long are dropped on flush.
// Dropped GUIDs only matter to clients that cached them, and are re-created the
// next time the item is listed.
const maxRefAge = 120 * 24 * time.Hour

// programRefAge bounds guide programme refs, which are memory-only.
const programRefAge = 2 * 24 * time.Hour

func (s *idStore) flush() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	now := time.Now()
	cutoff := now.Add(-maxRefAge).Unix()
	programCutoff := now.Add(-programRefAge).Unix()
	snapshot := make(map[string]itemRef, len(s.refs))
	for id, ref := range s.refs {
		switch {
		case ref.Kind == refProgram && ref.Seen < programCutoff:
			delete(s.refs, id)
		case ref.Kind == refProgram:
			// Guide programmes churn constantly and their IDs are only useful
			// while the programme is in the guide; keep them in memory only.
		case ref.Seen > 0 && ref.Seen < cutoff && ref.Kind != refUser:
			delete(s.refs, id)
		default:
			snapshot[id] = ref
		}
	}
	s.dirty = false
	s.mu.Unlock()
	data, err := json.Marshal(snapshot) // outside the lock: requests keep flowing
	if err == nil {
		err = os.MkdirAll(filepath.Dir(s.path), 0o755)
	}
	if err == nil {
		tmp := s.path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, s.path)
		}
	}
	if err != nil {
		s.mu.Lock()
		s.dirty = true // retry on the next tick
		s.mu.Unlock()
	}
	return err
}

func (s *idStore) flushLoop() {
	defer close(s.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.flush(); err != nil {
				log.Printf("[jellyfin-bridge] id index flush failed: %v", err)
			}
		case <-s.stop:
			if err := s.flush(); err != nil {
				log.Printf("[jellyfin-bridge] id index flush failed: %v", err)
			}
			return
		}
	}
}

func (s *idStore) close() {
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	<-s.done
}
