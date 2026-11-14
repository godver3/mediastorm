package badstreams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"novastream/models"
)

const defaultStorePath = "cache/bad_streams.json"

type Entry struct {
	ID                    string       `json:"id"`
	ReleaseName           string       `json:"releaseName"`
	NormalizedReleaseName string       `json:"normalizedReleaseName"`
	ServiceType           string       `json:"serviceType,omitempty"`
	Provider              string       `json:"provider,omitempty"`
	SourcePath            string       `json:"sourcePath,omitempty"`
	Reason                string       `json:"reason,omitempty"`
	MarkedAt              time.Time    `json:"markedAt"`
	LastSeenAt            time.Time    `json:"lastSeenAt"`
	HitCount              int          `json:"hitCount"`
	Scope                 *ReportScope `json:"scope,omitempty"`
	ReportedBy            string       `json:"reportedBy,omitempty"`
	ExpiresAt             *time.Time   `json:"expiresAt,omitempty"`
	ContentIdentity       string       `json:"contentIdentity,omitempty"`
}

type MarkRequest struct {
	ReleaseName string `json:"releaseName"`
	ServiceType string `json:"serviceType,omitempty"`
	Provider    string `json:"provider,omitempty"`
	SourcePath  string `json:"sourcePath,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type Service struct {
	mu      sync.RWMutex
	path    string
	entries map[string]Entry
}

func New(path string) *Service {
	if strings.TrimSpace(path) == "" {
		path = defaultStorePath
	}
	s := &Service{
		path:    path,
		entries: make(map[string]Entry),
	}
	if err := s.load(); err != nil {
		log.Printf("[bad-streams] failed to load registry %s: %v", path, err)
	}
	return s
}

func (s *Service) List() []Entry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	out := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		out = append(out, entry)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeenAt.After(out[j].LastSeenAt)
	})
	return out
}

func (s *Service) Mark(req MarkRequest) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("bad stream service unavailable")
	}
	releaseName := strings.TrimSpace(req.ReleaseName)
	normalizedRelease := NormalizeReleaseName(releaseName)
	if normalizedRelease == "" {
		return Entry{}, errors.New("releaseName is required")
	}

	serviceType := normalizeServiceType(req.ServiceType)
	provider := NormalizeProvider(req.Provider)
	if provider == "" {
		provider = providerFromSourcePath(req.SourcePath)
	}
	id := entryID(normalizedRelease, serviceType, provider)
	now := time.Now().UTC()

	s.mu.Lock()
	entry, exists := s.entries[id]
	if !exists {
		entry = Entry{
			ID:                    id,
			ReleaseName:           releaseName,
			NormalizedReleaseName: normalizedRelease,
			ServiceType:           serviceType,
			Provider:              provider,
			MarkedAt:              now,
		}
	}
	if releaseName != "" {
		entry.ReleaseName = releaseName
	}
	entry.SourcePath = strings.TrimSpace(req.SourcePath)
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		entry.Reason = reason
	}
	entry.LastSeenAt = now
	entry.HitCount++
	s.entries[id] = entry
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s *Service) Delete(id string) bool {
	if s == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	s.mu.Lock()
	previous, exists := s.entries[id]
	if exists {
		delete(s.entries, id)
		if err := s.saveLocked(); err != nil {
			log.Printf("[bad-streams] failed to save after delete: %v", err)
			s.entries[id] = previous
			s.mu.Unlock()
			return false
		}
	}
	s.mu.Unlock()
	return exists
}

func (s *Service) Clear() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	count := len(s.entries)
	s.entries = make(map[string]Entry)
	if err := s.saveLocked(); err != nil {
		log.Printf("[bad-streams] failed to save after clear: %v", err)
	}
	s.mu.Unlock()
	return count
}

func (s *Service) IsBad(result models.NZBResult) bool {
	return s.Match(result) != nil
}

func (s *Service) Match(result models.NZBResult) *Entry {
	if s == nil {
		return nil
	}
	normalizedRelease := NormalizeReleaseName(result.Title)
	if normalizedRelease == "" {
		return nil
	}
	serviceType := normalizeServiceType(string(result.ServiceType))
	provider := providerForResult(result)

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.entries {
		// Legacy callers have no language/title context and must never interpret
		// a conditional report as a global blacklist entry.
		if entry.Scope != nil {
			continue
		}
		if entry.NormalizedReleaseName != normalizedRelease {
			continue
		}
		if entry.ServiceType != "" && serviceType != "" && entry.ServiceType != serviceType {
			continue
		}
		if entry.ServiceType != "" && serviceType == "" {
			continue
		}
		if entry.Provider != "" && provider != "" && entry.Provider != provider {
			continue
		}
		if entry.Provider != "" && provider == "" {
			continue
		}
		entryCopy := entry
		return &entryCopy
	}
	return nil
}

func (s *Service) FilterResults(results []models.NZBResult) []models.NZBResult {
	if s == nil || len(results) == 0 {
		return results
	}
	filtered := make([]models.NZBResult, 0, len(results))
	for _, result := range results {
		if s.IsBad(result) {
			log.Printf("[bad-streams] skipping marked bad stream service=%q provider=%q release=%q", result.ServiceType, providerForResult(result), result.Title)
			continue
		}
		filtered = append(filtered, result)
	}
	return filtered
}

func (s *Service) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	s.mu.Lock()
	for _, entry := range entries {
		normalizedRelease := NormalizeReleaseName(entry.ReleaseName)
		if normalizedRelease == "" {
			normalizedRelease = NormalizeReleaseName(entry.NormalizedReleaseName)
		}
		entry.NormalizedReleaseName = normalizedRelease
		entry.ServiceType = normalizeServiceType(entry.ServiceType)
		entry.Provider = NormalizeProvider(entry.Provider)
		if entry.ID == "" {
			entry.ID = entryID(entry.NormalizedReleaseName, entry.ServiceType, entry.Provider)
		}
		if entry.ID == "" {
			continue
		}
		s.entries[entry.ID] = entry
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	entries := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastSeenAt.After(entries[j].LastSeenAt)
	})
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".bad-streams-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func NormalizeReleaseName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func NormalizeProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	return value
}

func normalizeServiceType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "usenet", "debrid", "local":
		return value
	default:
		return ""
	}
}

func providerForResult(result models.NZBResult) string {
	if result.Attributes == nil {
		return ""
	}
	for _, key := range []string{"provider", "debridProvider", "resolver", "resolverProvider"} {
		if provider := NormalizeProvider(result.Attributes[key]); provider != "" {
			return provider
		}
	}
	return ""
}

func providerFromSourcePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
		value = parsed.Path
	}
	parts := strings.Split(strings.Trim(value, "/"), "/")
	for i, part := range parts {
		if strings.EqualFold(part, "debrid") && i+1 < len(parts) {
			return NormalizeProvider(parts[i+1])
		}
	}
	return ""
}

func entryID(normalizedRelease, serviceType, provider string) string {
	if normalizedRelease == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalizedRelease + "|" + normalizeServiceType(serviceType) + "|" + NormalizeProvider(provider)))
	return hex.EncodeToString(sum[:8])
}
