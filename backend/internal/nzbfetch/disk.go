package nzbfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DiskTTL keeps a grabbed NZB long enough to replay a title, re-run health
	// checks and survive scheduled pre-warm cycles without another indexer grab.
	DiskTTL     = 30 * 24 * time.Hour
	maxDiskSize = 2 << 30
)

// credentialParams are newznab download URL parameters that identify the
// account (API key, RSS key, user ID) rather than the release. Some indexers
// (Ninja) put them after an "&" in the path instead of a query string.
var credentialParams = map[string]bool{"apikey": true, "api_key": true, "r": true, "i": true, "key": true, "token": true}

// ReleaseURL returns the download URL without account credentials, so the same
// release keeps one identity after an API key is rotated or reissued.
func ReleaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.User = nil
	u.Fragment = ""
	if i := strings.Index(u.Path, "&"); i >= 0 {
		kept := []string{u.Path[:i]}
		for _, part := range strings.Split(u.Path[i+1:], "&") {
			name, _, _ := strings.Cut(part, "=")
			if part != "" && !credentialParams[strings.ToLower(name)] {
				kept = append(kept, part)
			}
		}
		u.Path = strings.Join(kept, "&")
		u.RawPath = ""
	}
	q := u.Query()
	for name := range q {
		if credentialParams[strings.ToLower(name)] {
			q.Del(name)
		}
	}
	u.RawQuery = q.Encode()
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

// ReleaseURLHash is the hex SHA-256 of ReleaseURL.
func ReleaseURLHash(raw string) string {
	normalized := ReleaseURL(raw)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// diskCache persists grabbed NZBs so every acquisition path (playback,
// availability checks, track probes, pre-warm) reuses one grab per release
// across restarts. Indexers count grabs, and many watermark each copy, so the
// bytes themselves are the only stable thing to keep.
type diskCache struct {
	mu  sync.Mutex
	dir string
}

type diskMeta struct {
	ContentDisposition string `json:"contentDisposition,omitempty"`
	Indexer            string `json:"indexer,omitempty"`
	Title              string `json:"title,omitempty"`
	SavedAt            int64  `json:"savedAt"`
}

// SetDiskCache enables the persistent NZB store under dir.
func (f *Fetcher) SetDiskCache(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		f.disk = nil
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[nzbfetch] disk cache disabled: %v", err)
		f.disk = nil
		return
	}
	f.disk = &diskCache{dir: dir}
}

func (d *diskCache) paths(key string) (string, string) {
	return filepath.Join(d.dir, key+".nzb"), filepath.Join(d.dir, key+".json")
}

func (d *diskCache) get(key string) (payload, bool) {
	if d == nil || key == "" {
		return payload{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	nzbPath, metaPath := d.paths(key)
	info, err := os.Stat(nzbPath)
	if err != nil {
		return payload{}, false
	}
	if time.Since(info.ModTime()) >= DiskTTL {
		_ = os.Remove(nzbPath)
		_ = os.Remove(metaPath)
		return payload{}, false
	}
	data, err := os.ReadFile(nzbPath)
	if err != nil || len(data) == 0 {
		return payload{}, false
	}
	header := http.Header{}
	if raw, err := os.ReadFile(metaPath); err == nil {
		var meta diskMeta
		if json.Unmarshal(raw, &meta) == nil && meta.ContentDisposition != "" {
			header.Set("Content-Disposition", meta.ContentDisposition)
		}
	}
	return payload{data: data, header: header, at: info.ModTime()}, true
}

func (d *diskCache) put(key, indexer, title string, p payload) {
	if d == nil || key == "" || len(p.data) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	nzbPath, metaPath := d.paths(key)
	meta, _ := json.Marshal(diskMeta{
		ContentDisposition: p.header.Get("Content-Disposition"),
		Indexer:            indexer,
		Title:              title,
		SavedAt:            time.Now().Unix(),
	})
	if err := writeAtomic(nzbPath, p.data); err != nil {
		log.Printf("[nzbfetch] disk cache write failed: %v", err)
		return
	}
	_ = writeAtomic(metaPath, meta)
	d.pruneLocked()
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// pruneLocked drops expired entries, then the oldest until under maxDiskSize.
func (d *diskCache) pruneLocked() {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	type item struct {
		key  string
		size int64
		mod  time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".nzb") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		key := strings.TrimSuffix(name, ".nzb")
		if time.Since(info.ModTime()) >= DiskTTL {
			d.removeLocked(key)
			continue
		}
		items = append(items, item{key, info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= maxDiskSize {
			break
		}
		d.removeLocked(it.key)
		total -= it.size
	}
}

func (d *diskCache) removeLocked(key string) {
	nzbPath, metaPath := d.paths(key)
	_ = os.Remove(nzbPath)
	_ = os.Remove(metaPath)
}
