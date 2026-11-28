package qualityprofiles

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store persists presets and downloaded snapshot archives.
type Store interface {
	ListPresets(ctx context.Context) ([]*Preset, error)
	// GetPreset returns nil, nil when the preset does not exist.
	GetPreset(ctx context.Context, id string) (*Preset, error)
	SavePreset(ctx context.Context, p *Preset) error
	DeletePreset(ctx context.Context, id string) error
	// GetSnapshot returns nil, nil when the archive is not cached.
	GetSnapshot(ctx context.Context, sourceID, commit string) ([]byte, error)
	PutSnapshot(ctx context.Context, sourceID, commit string, archive []byte) error
	// GetTitlePreset returns the preset a profile last picked for a title
	// on a device, "" when none.
	GetTitlePreset(ctx context.Context, userID, clientID, contentID string) (string, error)
	// SetTitlePreset remembers it; "" forgets it.
	SetTitlePreset(ctx context.Context, userID, clientID, contentID, presetID string) error
	// ListTitlePresets returns every remembered title preset (for backups).
	ListTitlePresets(ctx context.Context) ([]TitlePresetRecord, error)
}

// TitlePresetRecord is one remembered per-title, per-device preset pick.
type TitlePresetRecord struct {
	UserID    string    `json:"userId"`
	ClientID  string    `json:"clientId"`
	ContentID string    `json:"contentId"`
	PresetID  string    `json:"presetId"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MemoryStore is an in-process Store for tests; mediastorm itself needs
// Postgres.
type MemoryStore struct {
	mu        sync.RWMutex
	presets   map[string][]byte
	snapshots map[string][]byte
	titles    map[string]string // user|client|content -> preset
}

// NewMemoryStore creates an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{presets: map[string][]byte{}, snapshots: map[string][]byte{}, titles: map[string]string{}}
}

func decodePreset(data []byte) (*Preset, error) {
	var p Preset
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPresets implements Store.
func (m *MemoryStore) ListPresets(context.Context) ([]*Preset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Preset, 0, len(m.presets))
	for _, data := range m.presets {
		p, err := decodePreset(data)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// GetPreset implements Store.
func (m *MemoryStore) GetPreset(_ context.Context, id string) (*Preset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.presets[id]
	if !ok {
		return nil, nil
	}
	return decodePreset(data)
}

// SavePreset implements Store.
func (m *MemoryStore) SavePreset(_ context.Context, p *Preset) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.presets[p.ID] = data
	return nil
}

// DeletePreset implements Store.
func (m *MemoryStore) DeletePreset(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.presets, id)
	return nil
}

// GetSnapshot implements Store.
func (m *MemoryStore) GetSnapshot(_ context.Context, sourceID, commit string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshots[sourceID+"@"+commit], nil
}

// PutSnapshot implements Store.
func (m *MemoryStore) PutSnapshot(_ context.Context, sourceID, commit string, archive []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots[sourceID+"@"+commit] = append([]byte(nil), archive...)
	return nil
}

func (m *MemoryStore) GetTitlePreset(_ context.Context, userID, clientID, contentID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.titles[userID+"|"+clientID+"|"+contentID], nil
}

func (m *MemoryStore) SetTitlePreset(_ context.Context, userID, clientID, contentID, presetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := userID + "|" + clientID + "|" + contentID
	if presetID == "" {
		delete(m.titles, key)
	} else {
		m.titles[key] = presetID
	}
	return nil
}

// ListTitlePresets implements Store.
func (m *MemoryStore) ListTitlePresets(_ context.Context) ([]TitlePresetRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]TitlePresetRecord, 0, len(m.titles))
	for key, presetID := range m.titles {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 {
			continue
		}
		out = append(out, TitlePresetRecord{UserID: parts[0], ClientID: parts[1], ContentID: parts[2], PresetID: presetID})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.ClientID != b.ClientID {
			return a.ClientID < b.ClientID
		}
		return a.ContentID < b.ContentID
	})
	return out, nil
}
