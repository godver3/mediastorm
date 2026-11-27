package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

// settingsCache memoizes the parsed settings file. Load is called from hundreds
// of places, several on hot paths (prewarm, prequeue scope keys), and parsing
// the file each time dominated idle CPU. Entries are keyed on the file's
// modification time and size, so edits made outside Save are still picked up.
type settingsCache struct {
	mu       sync.Mutex
	valid    bool
	modTime  time.Time
	size     int64
	settings Settings
}

func (c *settingsCache) invalidate() {
	c.mu.Lock()
	c.valid = false
	c.settings = Settings{}
	c.mu.Unlock()
}

// Load returns the current settings. Each call returns an independent deep
// copy, so callers may freely mutate the result.
func (m *Manager) Load() (Settings, error) {
	if m.path == "" {
		return Settings{}, errors.New("config path not set")
	}
	info, err := os.Stat(m.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			m.cache.invalidate()
			return m.loadUncached()
		}
		return Settings{}, err
	}

	m.cache.mu.Lock()
	defer m.cache.mu.Unlock()
	if m.cache.valid && info.ModTime().Equal(m.cache.modTime) && info.Size() == m.cache.size {
		return cloneSettings(m.cache.settings), nil
	}
	s, err := m.loadUncached()
	if err != nil {
		m.cache.valid = false
		return Settings{}, err
	}
	m.cache.valid = true
	m.cache.modTime = info.ModTime()
	m.cache.size = info.Size()
	m.cache.settings = cloneSettings(s)
	return s, nil
}

func cloneSettings(s Settings) Settings {
	var out Settings
	deepCopyValue(reflect.ValueOf(&out).Elem(), reflect.ValueOf(s))
	return out
}

// deepCopyValue copies src into dst, duplicating every map, slice and pointer
// reachable through exported fields. Unexported struct fields (e.g. inside
// time.Time) are copied by value, which is safe for immutable types.
func deepCopyValue(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			return
		}
		n := reflect.New(src.Elem().Type())
		deepCopyValue(n.Elem(), src.Elem())
		dst.Set(n)
	case reflect.Interface:
		if src.IsNil() {
			return
		}
		inner := src.Elem()
		n := reflect.New(inner.Type()).Elem()
		deepCopyValue(n, inner)
		dst.Set(n)
	case reflect.Slice:
		if src.IsNil() {
			return
		}
		n := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		for i := 0; i < src.Len(); i++ {
			deepCopyValue(n.Index(i), src.Index(i))
		}
		dst.Set(n)
	case reflect.Map:
		if src.IsNil() {
			return
		}
		n := reflect.MakeMapWithSize(src.Type(), src.Len())
		iter := src.MapRange()
		for iter.Next() {
			v := reflect.New(iter.Value().Type()).Elem()
			deepCopyValue(v, iter.Value())
			n.SetMapIndex(iter.Key(), v)
		}
		dst.Set(n)
	case reflect.Array:
		for i := 0; i < src.Len(); i++ {
			deepCopyValue(dst.Index(i), src.Index(i))
		}
	case reflect.Struct:
		dst.Set(src) // copies unexported fields by value
		t := src.Type()
		for i := 0; i < src.NumField(); i++ {
			if !t.Field(i).IsExported() {
				continue
			}
			deepCopyValue(dst.Field(i), src.Field(i))
		}
	default:
		dst.Set(src)
	}
}

// --- Dry-run details sidecar ---------------------------------------------

func (m *Manager) dryRunDir() string {
	return filepath.Join(filepath.Dir(m.path), "scheduled_task_dry_runs")
}

func (m *Manager) dryRunPath(taskID string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == os.PathSeparator || r == 0 {
			return '_'
		}
		return r
	}, taskID)
	safe = strings.TrimLeft(safe, ".")
	return filepath.Join(m.dryRunDir(), safe+".json")
}

// SaveDryRunDetails stores a task's dry-run results outside settings.json.
// Passing nil removes them.
func (m *Manager) SaveDryRunDetails(taskID string, details *DryRunDetails) error {
	if m.path == "" || strings.TrimSpace(taskID) == "" {
		return errors.New("config path or task id not set")
	}
	path := m.dryRunPath(taskID)
	if details == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(m.dryRunDir(), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(details)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadDryRunDetails returns a task's stored dry-run results, or nil.
func (m *Manager) LoadDryRunDetails(taskID string) (*DryRunDetails, error) {
	if m.path == "" || strings.TrimSpace(taskID) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(m.dryRunPath(taskID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var d DryRunDetails
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// migrateDryRunDetailsRaw moves legacy inline dryRunDetails out of the raw
// settings map into sidecar files and sets hasDryRunDetails instead.
func (m *Manager) migrateDryRunDetailsRaw(raw map[string]interface{}) {
	st, ok := raw["scheduledTasks"].(map[string]interface{})
	if !ok {
		return
	}
	tasks, ok := st["tasks"].([]interface{})
	if !ok {
		return
	}
	for _, t := range tasks {
		task, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		detailsRaw, ok := task["dryRunDetails"]
		if !ok {
			continue
		}
		delete(task, "dryRunDetails")
		id, _ := task["id"].(string)
		if detailsRaw == nil || id == "" {
			continue
		}
		data, err := json.Marshal(detailsRaw)
		if err != nil {
			continue
		}
		var d DryRunDetails
		if err := json.Unmarshal(data, &d); err != nil {
			continue
		}
		if err := m.SaveDryRunDetails(id, &d); err == nil {
			task["hasDryRunDetails"] = true
		}
	}
}
