package metadata

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	jitter := time.Duration(n%uint64(6*time.Hour)) // 0 to 6 hours
	return c.ttl + jitter
}

func (c *fileCache) get(key string, v any) (bool, error) {
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
	if time.Since(fi.ModTime()) > c.jitteredTTL(key) {
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
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
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
