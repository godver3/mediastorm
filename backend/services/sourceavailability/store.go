package sourceavailability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"novastream/models"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxRecords = 500

type record struct {
	ProfileID string                    `json:"profileId"`
	TitleID   string                    `json:"titleId"`
	Scope     string                    `json:"scope"`
	Value     models.SourceAvailability `json:"value"`
}
type Store struct {
	mu        sync.Mutex
	directory string
	records   map[string]record
	now       func() time.Time
}

func NewStore(directory string) (*Store, error) {
	if directory == "" {
		return nil, errors.New("availability cache directory required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	s := &Store{directory: directory, records: map[string]record{}, now: time.Now}
	files, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || len(name) != 69 || !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".json")); err != nil {
			continue
		}
		info, err := file.Info()
		if err != nil || info.Size() > 65536 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			continue
		}
		var entry record
		if json.Unmarshal(data, &entry) != nil {
			continue
		}
		if entry.ProfileID == "" || entry.TitleID == "" || entry.Scope == "" ||
			entry.Value.CheckedAt == nil || !validState(entry.Value) ||
			entry.Value.ExpiresAt == nil || !entry.Value.ExpiresAt.After(s.now()) {
			_ = os.Remove(filepath.Join(directory, name))
			continue
		}
		if key(entry.ProfileID, entry.TitleID, entry.Scope)+".json" != name {
			continue
		}
		if len(s.records) < MaxRecords {
			s.records[strings.TrimSuffix(name, ".json")] = entry
		} else {
			_ = os.Remove(filepath.Join(directory, name))
		}
	}
	return s, nil
}

func key(profile, title, scope string) string {
	sum := sha256.Sum256([]byte(profile + "\x00" + title + "\x00" + scope))
	return hex.EncodeToString(sum[:])
}
func unknown(reason string) models.SourceAvailability {
	return models.SourceAvailability{State: "unknown", Reason: reason}
}

func validState(value models.SourceAvailability) bool {
	return value.State == "found" || value.State == "unknown" || (value.State == "not_found" && value.Complete)
}
func copyValue(value models.SourceAvailability) models.SourceAvailability {
	value.Providers = append([]string(nil), value.Providers...)
	if value.CheckedAt != nil {
		t := *value.CheckedAt
		value.CheckedAt = &t
	}
	if value.ExpiresAt != nil {
		t := *value.ExpiresAt
		value.ExpiresAt = &t
	}
	return value
}

func (s *Store) Get(profile, title, scope string) models.SourceAvailability {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.records[key(profile, title, scope)]
	if !ok {
		return unknown("not_checked")
	}
	if entry.Value.ExpiresAt == nil || !entry.Value.ExpiresAt.After(s.now()) {
		return unknown("expired")
	}
	return copyValue(entry.Value)
}

func (s *Store) Put(profile, title, scope string, value models.SourceAvailability) error {
	if profile == "" || title == "" || scope == "" || value.CheckedAt == nil || value.ExpiresAt == nil {
		return errors.New("availability identity and dates required")
	}
	if value.State != "found" && value.State != "not_found" && value.State != "unknown" {
		return errors.New("invalid source state")
	}
	if value.State == "not_found" && !value.Complete {
		return errors.New("incomplete search cannot prove a missing candidate")
	}
	value = copyValue(value)
	entry := record{ProfileID: profile, TitleID: title, Scope: scope, Value: value}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.CreateTemp(s.directory, "availability-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	id := key(profile, title, scope)
	if err = os.Rename(name, filepath.Join(s.directory, id+".json")); err != nil {
		return err
	}
	s.records[id] = entry
	if len(s.records) > MaxRecords {
		oldest := ""
		var oldestAt time.Time
		for candidate, item := range s.records {
			if candidate == id {
				continue
			}
			if oldest == "" || item.Value.CheckedAt.Before(oldestAt) {
				oldest = candidate
				oldestAt = *item.Value.CheckedAt
			}
		}
		if oldest != "" {
			delete(s.records, oldest)
			_ = os.Remove(filepath.Join(s.directory, oldest+".json"))
		}
	}
	return nil
}

// Revision changes when this profile's visible cached states change or expire.
func (s *Store) Revision(profile string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := []string{}
	for id, entry := range s.records {
		if entry.ProfileID != profile {
			continue
		}
		state := entry.Value.State
		if entry.Value.ExpiresAt == nil || !entry.Value.ExpiresAt.After(s.now()) {
			state = "unknown"
		}
		checked := ""
		if entry.Value.CheckedAt != nil {
			checked = entry.Value.CheckedAt.UTC().Format(time.RFC3339Nano)
		}
		parts = append(parts, id+":"+state+":"+checked)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:16]
}
