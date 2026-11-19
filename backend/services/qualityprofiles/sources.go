package qualityprofiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"novastream/internal/httpheaders"
	"novastream/internal/trash"
)

// SourceType is how a TRaSH-format snapshot is obtained.
type SourceType string

const (
	SourceGitHub  SourceType = "github"  // owner/repo at a branch, tag or commit
	SourceURL     SourceType = "url"     // a .tar.gz of the repository (or of docs/json)
	SourceBundled SourceType = "bundled" // the snapshot compiled into mediastorm
)

// Source is one configured snapshot source.
type Source struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Type     SourceType `json:"type"`
	Repo     string     `json:"repo,omitempty"` // GitHub owner/name
	Ref      string     `json:"ref,omitempty"`  // branch, tag or commit (default: master)
	URL      string     `json:"url,omitempty"`
	Priority int        `json:"priority"` // lower = higher precedence
	Enabled  bool       `json:"enabled"`
}

// DefaultSources returns the TRaSH-Guides repository with the bundled
// snapshot as offline fallback.
func DefaultSources() []Source {
	return []Source{{ID: "trash-guides", Name: "TRaSH-Guides", Type: SourceGitHub, Repo: "TRaSH-Guides/Guides", Ref: "master", Priority: 0, Enabled: true}}
}

var (
	reSourceID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	reRepo     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	reRef      = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,100}$`)
	reSHA      = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ValidateSources checks a source list.
func ValidateSources(sources []Source) error {
	seen := map[string]bool{}
	for _, s := range sources {
		if !reSourceID.MatchString(s.ID) {
			return fmt.Errorf("source id %q must be lower-case letters, digits and dashes", s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate source id %q", s.ID)
		}
		seen[s.ID] = true
		switch s.Type {
		case SourceGitHub:
			if !reRepo.MatchString(s.Repo) {
				return fmt.Errorf("source %s: repo must be owner/name", s.ID)
			}
			if s.Ref != "" && !reRef.MatchString(s.Ref) {
				return fmt.Errorf("source %s: invalid ref %q", s.ID, s.Ref)
			}
		case SourceURL:
			u, err := url.Parse(s.URL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return fmt.Errorf("source %s: url must be http(s)", s.ID)
			}
		case SourceBundled:
		default:
			return fmt.Errorf("source %s: unknown type %q", s.ID, s.Type)
		}
	}
	return nil
}

// SortSources orders enabled sources by precedence.
func SortSources(sources []Source) []Source {
	out := make([]Source, 0, len(sources))
	for _, s := range sources {
		if s.Enabled {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// MaxDownloadBytes bounds one compressed archive download.
const MaxDownloadBytes = 64 << 20

// Fetcher downloads and caches snapshots.
type Fetcher struct {
	HTTP      *http.Client
	Store     Store
	GitHubAPI string // default https://api.github.com
	Codeload  string // default https://codeload.github.com
	Now       func() time.Time

	mu    sync.Mutex
	cache map[string]*trash.Snapshot // sourceID@commit
	order []string                   // cache keys, oldest first
	// urls remembers what a URL source served, so checks within urlTTL do
	// not download it again (a URL has no commit to ask for first).
	urls  map[string]urlSnapshot
	group singleflight.Group // one download per source and commit at a time
}

type urlSnapshot struct {
	snap    *trash.Snapshot
	fetched time.Time
}

const (
	// maxCachedSnapshots bounds the parsed snapshots kept in memory.
	maxCachedSnapshots = 4
	urlTTL             = 10 * time.Minute
)

// NewFetcher creates a fetcher using the given store as archive cache.
func NewFetcher(store Store, client *http.Client) *Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Fetcher{HTTP: client, Store: store, GitHubAPI: "https://api.github.com", Codeload: "https://codeload.github.com", Now: time.Now,
		cache: map[string]*trash.Snapshot{}, urls: map[string]urlSnapshot{}}
}

func (f *Fetcher) get(ctx context.Context, rawURL, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpheaders.UserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", rawURL, limit)
	}
	if resp.StatusCode != http.StatusOK {
		body := strings.TrimSpace(string(data))
		if len(body) > 200 {
			body = body[:200]
		}
		return nil, fmt.Errorf("%s: HTTP %d %s", rawURL, resp.StatusCode, body)
	}
	return data, nil
}

// ResolveCommit returns the commit a source currently points to.
func (f *Fetcher) ResolveCommit(ctx context.Context, src Source) (string, error) {
	switch src.Type {
	case SourceGitHub:
		ref := src.Ref
		if ref == "" {
			ref = "master"
		}
		if reSHA.MatchString(ref) {
			return ref, nil
		}
		data, err := f.get(ctx, fmt.Sprintf("%s/repos/%s/commits/%s", strings.TrimRight(f.GitHubAPI, "/"), src.Repo, url.PathEscape(ref)), "application/vnd.github.sha", 1024)
		if err != nil {
			return "", err
		}
		sha := strings.TrimSpace(string(data))
		if !reSHA.MatchString(sha) {
			return "", fmt.Errorf("github returned an invalid commit for %s@%s", src.Repo, ref)
		}
		return sha, nil
	case SourceBundled:
		snap, err := trash.Bundled()
		if err != nil {
			return "", err
		}
		return snap.Commit, nil
	case SourceURL:
		// A URL has no commit until it is downloaded.
		return "", nil
	}
	return "", fmt.Errorf("unknown source type %q", src.Type)
}

// Snapshot loads a source at a commit ("" = latest), using the cache.
func (f *Fetcher) Snapshot(ctx context.Context, src Source, commit string) (*trash.Snapshot, error) {
	if src.Type == SourceBundled {
		snap, err := trash.Bundled()
		if err != nil {
			return nil, err
		}
		if commit != "" && commit != snap.Commit {
			return nil, fmt.Errorf("bundled snapshot is %s, not %s", snap.Commit, commit)
		}
		cp := *snap
		cp.Source = src.ID
		return &cp, nil
	}
	if commit == "" {
		c, err := f.ResolveCommit(ctx, src)
		if err != nil {
			return nil, err
		}
		commit = c
	}
	if src.Type == SourceURL && commit == "" {
		f.mu.Lock()
		u, ok := f.urls[src.URL]
		f.mu.Unlock()
		if ok && f.Now().Sub(u.fetched) < urlTTL {
			return u.snap, nil
		}
	}
	v, err, _ := f.group.Do(src.ID+"@"+commit, func() (any, error) { return f.load(ctx, src, commit) })
	if err != nil {
		return nil, err
	}
	return v.(*trash.Snapshot), nil
}

// load reads a snapshot from memory, the store or the network.
func (f *Fetcher) load(ctx context.Context, src Source, commit string) (*trash.Snapshot, error) {
	if commit != "" {
		if snap := f.cached(src.ID, commit); snap != nil {
			return snap, nil
		}
		if f.Store != nil {
			if archive, err := f.Store.GetSnapshot(ctx, src.ID, commit); err == nil && len(archive) > 0 {
				snap, err := trash.LoadArchive(bytes.NewReader(archive), commit)
				if err == nil {
					snap.Source = src.ID
					f.remember(src.ID, commit, snap)
					return snap, nil
				}
			}
		}
	}
	var archiveURL string
	switch src.Type {
	case SourceGitHub:
		archiveURL = fmt.Sprintf("%s/%s/tar.gz/%s", strings.TrimRight(f.Codeload, "/"), src.Repo, commit)
	case SourceURL:
		archiveURL = src.URL
	default:
		return nil, fmt.Errorf("unknown source type %q", src.Type)
	}
	archive, err := f.get(ctx, archiveURL, "", MaxDownloadBytes)
	if err != nil {
		return nil, err
	}
	if src.Type == SourceURL {
		sum := sha256.Sum256(archive)
		got := "sha256:" + hex.EncodeToString(sum[:])[:16]
		if commit != "" && commit != got {
			return nil, fmt.Errorf("%s now serves %s, not the pinned %s", src.URL, got, commit)
		}
		commit = got
	}
	snap, err := trash.LoadArchive(bytes.NewReader(archive), commit)
	if err != nil {
		return nil, err
	}
	snap.Commit = commit
	snap.Source = src.ID
	if f.Store != nil {
		// Cache only the part that is used, not the whole repository.
		stored := archive
		if compact, err := trash.CompactArchive(archive, commit); err == nil {
			stored = compact
		}
		_ = f.Store.PutSnapshot(ctx, src.ID, commit, stored)
	}
	f.remember(src.ID, commit, snap)
	if src.Type == SourceURL {
		f.mu.Lock()
		f.urls[src.URL] = urlSnapshot{snap: snap, fetched: f.Now()}
		f.mu.Unlock()
	}
	return snap, nil
}

func (f *Fetcher) cached(id, commit string) *trash.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cache[id+"@"+commit]
}

// remember keeps a parsed snapshot; the oldest goes when there are more
// than maxCachedSnapshots.
func (f *Fetcher) remember(id, commit string, snap *trash.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := id + "@" + commit
	if _, ok := f.cache[key]; !ok {
		f.order = append(f.order, key)
	}
	f.cache[key] = snap
	for len(f.order) > maxCachedSnapshots {
		delete(f.cache, f.order[0])
		f.order = f.order[1:]
	}
}

// ErrNoSources is returned when no source is enabled.
var ErrNoSources = errors.New("no TRaSH source is enabled")

// MergedSnapshot loads every enabled source (at the commits given per
// source ID, latest otherwise) and merges them by precedence. When a
// network source fails and no commit was requested for it, the bundled
// snapshot is used in its place so imports keep working offline.
func (f *Fetcher) MergedSnapshot(ctx context.Context, sources []Source, commits map[string]string) (*trash.Snapshot, []string, error) {
	return f.mergedSnapshot(ctx, sources, commits, true)
}

// MergedSnapshotStrict is MergedSnapshot without the bundled fallback: an
// unreachable source is an error. Update checks use it, so an outage never
// looks like an upstream change back to the bundled data.
func (f *Fetcher) MergedSnapshotStrict(ctx context.Context, sources []Source, commits map[string]string) (*trash.Snapshot, []string, error) {
	return f.mergedSnapshot(ctx, sources, commits, false)
}

func (f *Fetcher) mergedSnapshot(ctx context.Context, sources []Source, commits map[string]string, fallback bool) (*trash.Snapshot, []string, error) {
	ordered := SortSources(sources)
	if len(ordered) == 0 {
		return nil, nil, ErrNoSources
	}
	var snaps []*trash.Snapshot
	var warnings []string
	for _, src := range ordered {
		snap, err := f.Snapshot(ctx, src, commits[src.ID])
		if err != nil {
			if fallback && commits[src.ID] == "" && src.Type != SourceBundled {
				if b, berr := f.Snapshot(ctx, Source{ID: "bundled", Type: SourceBundled}, ""); berr == nil {
					warnings = append(warnings, fmt.Sprintf("%s unavailable (%v); using the bundled snapshot %s", src.Name, err, b.Commit))
					snaps = append(snaps, b)
					continue
				}
			}
			return nil, warnings, fmt.Errorf("source %s: %w", src.Name, err)
		}
		snaps = append(snaps, snap)
	}
	return trash.Merge(snaps...), warnings, nil
}

// ParseCommits splits a merged commit ("a@sha,b@sha") into per-source
// commits. A plain commit applies to the first source.
func ParseCommits(commit string, sources []Source) map[string]string {
	out := map[string]string{}
	if commit == "" {
		return out
	}
	if !strings.Contains(commit, "@") {
		if ordered := SortSources(sources); len(ordered) > 0 {
			out[ordered[0].ID] = commit
		}
		return out
	}
	for _, part := range strings.Split(commit, ",") {
		if i := strings.LastIndex(part, "@"); i > 0 {
			out[part[:i]] = part[i+1:]
		}
	}
	return out
}
