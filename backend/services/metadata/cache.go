package metadata

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type fileCache struct {
	dir string
	ttl time.Duration
}

func newFileCache(dir string, ttlHours int) *fileCache {
	return &fileCache{dir: dir, ttl: time.Duration(ttlHours) * time.Hour}
}

// jitteredTTL returns a TTL for the given key that is deterministically staggered
// between the base TTL and base TTL + 6 hours. The jitter is derived from the key
// hash so the same key always gets the same TTL, preventing cache churn.
func (c *fileCache) jitteredTTL(key string) time.Duration {
	h := sha256.Sum256([]byte(key))
	n := binary.BigEndian.Uint64(h[:8])
	jitter := time.Duration(n % uint64(6*time.Hour)) // 0 to 6 hours
	return c.ttl + jitter
}

func (c *fileCache) get(key string, v any) (bool, error) {
	return c.getWithMaxAge(key, v, 0)
}

// getWithMaxAge is like get but uses a custom maxAge instead of the cache's default TTL.
// If maxAge is 0, the default jittered TTL is used.
func (c *fileCache) getWithMaxAge(key string, v any, maxAge time.Duration) (bool, error) {
	if key == "" {
		return false, errors.New("empty key")
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return false, err
	}
	path := filepath.Join(c.dir, key+".json")
	fi, err := os.Stat(path)
	if err != nil {
		return false, nil
	}
	ttl := c.jitteredTTL(key)
	if maxAge > 0 {
		ttl = maxAge
	}
	if time.Since(fi.ModTime()) > ttl {
		_ = os.Remove(path)
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if err := dec.Decode(v); err != nil {
		return false, nil
	}
	return true, nil
}

func (c *fileCache) set(key string, v any) error {
	if key == "" {
		return errors.New("empty key")
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(c.dir, key+".json")
	f, err := os.CreateTemp(c.dir, key+".json.tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return replaceFile(tmp, path)
}

// replaceFile renames tmp over path. On Windows a rename onto a file that
// another writer is replacing or a reader has open fails with access denied
// for a moment, so it is retried briefly there.
func replaceFile(tmp, path string) error {
	err := os.Rename(tmp, path)
	if runtime.GOOS != "windows" {
		return err
	}
	for delay := time.Millisecond; err != nil && delay <= 64*time.Millisecond; delay *= 2 {
		time.Sleep(delay)
		err = os.Rename(tmp, path)
	}
	return err
}

// clear removes all cached metadata files from the cache directory.
// This is used when API keys change to force fresh data to be fetched.
func (c *fileCache) clear() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Nothing to clear
		}
		return err
	}
	var removed int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) == ".json" {
			path := filepath.Join(c.dir, entry.Name())
			if err := os.Remove(path); err != nil {
				continue // Best effort, skip files we can't remove
			}
			removed++
		}
	}
	return nil
}

const (
	cacheSweepInterval = 24 * time.Hour
	// jitteredTTL adds up to 6h to the base TTL; never sweep younger files.
	cacheSweepGrace = 6 * time.Hour
	// Orphaned temp files from interrupted writes.
	cacheSweepTmpAge = time.Hour
)

// sweepExpired removes cache files older than the maximum possible TTL (base
// TTL plus jitter) and stale temp files. Entries are otherwise only removed
// when re-read. Non-recursive and best effort; returns the number removed.
func (c *fileCache) sweepExpired() int {
	if c == nil || c.dir == "" {
		return 0
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		maxAge := c.ttl + cacheSweepGrace
		if !strings.HasSuffix(name, ".json") {
			if !strings.Contains(name, ".json.tmp-") {
				continue
			}
			maxAge = cacheSweepTmpAge
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) <= maxAge {
			continue
		}
		if os.Remove(filepath.Join(c.dir, name)) == nil {
			removed++
		}
	}
	return removed
}

// runCacheSweeper sweeps the given caches once now and then every
// cacheSweepInterval until stop is closed.
func runCacheSweeper(stop <-chan struct{}, caches ...*fileCache) {
	sweep := func() {
		for _, c := range caches {
			c.sweepExpired()
		}
	}
	sweep()
	ticker := time.NewTicker(cacheSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			sweep()
		case <-stop:
			return
		}
	}
}
