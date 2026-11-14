package interests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

const MaxPerProfile = 50
const MaxTotal = 500
const RetentionDays = 90

type snapshot struct {
	ProfileID string                          `json:"profileId"`
	Items     []models.RecommendationInterest `json:"items"`
}
type observation struct {
	profile    string
	item       models.RecommendationInterest
	generation uint64
}
type Service struct {
	mu           sync.Mutex
	generationMu sync.Mutex
	directory    string
	data         map[string][]models.RecommendationInterest
	generation   map[string]uint64
	queue        chan observation
	now          func() time.Time
}

func digest(profile string) string {
	sum := sha256.Sum256([]byte(profile))
	return hex.EncodeToString(sum[:]) + ".json"
}
func NewService(directory string) (*Service, error) {
	if directory == "" {
		return nil, errors.New("interest directory required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	s := &Service{directory: directory, data: map[string][]models.RecommendationInterest{}, generation: map[string]uint64{}, queue: make(chan observation, 64), now: time.Now}
	files, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || len(name) != 69 || !strings.HasSuffix(name, ".json") {
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
		var saved snapshot
		if json.Unmarshal(data, &saved) != nil || saved.ProfileID == "" || digest(saved.ProfileID) != name {
			continue
		}
		s.data[saved.ProfileID] = saved.Items
	}
	s.trimLocked()
	return s, nil
}

func (s *Service) clean(items []models.RecommendationInterest) []models.RecommendationInterest {
	cutoff := s.now().AddDate(0, 0, -RetentionDays)
	out := []models.RecommendationInterest{}
	for _, item := range items {
		if item.TMDBID <= 0 || (item.MediaType != "movie" && item.MediaType != "series") || item.SearchedAt.Before(cutoff) || item.SearchedAt.After(s.now().Add(time.Minute)) {
			continue
		}
		item.Title = strings.TrimSpace(item.Title)
		if len([]rune(item.Title)) > 200 {
			item.Title = string([]rune(item.Title)[:200])
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SearchedAt.After(out[j].SearchedAt) })
	if len(out) > MaxPerProfile {
		out = out[:MaxPerProfile]
	}
	return out
}
func (s *Service) List(profile string) []models.RecommendationInterest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]models.RecommendationInterest(nil), s.clean(s.data[profile])...)
}

// Observe never waits for disk or a metadata provider on the search response.
func (s *Service) Observe(profile string, title models.Title) {
	if profile == "" || title.TMDBID <= 0 || (title.MediaType != "movie" && title.MediaType != "series") {
		return
	}
	s.generationMu.Lock()
	generation := s.generation[profile]
	s.generationMu.Unlock()
	item := observation{profile: profile, generation: generation, item: models.RecommendationInterest{TMDBID: title.TMDBID, MediaType: title.MediaType, Title: title.Name, SearchedAt: s.now().UTC()}}
	select {
	case s.queue <- item:
	default:
	}
}
func (s *Service) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-s.queue:
				_ = s.record(item)
			case <-ticker.C:
				s.mu.Lock()
				s.trimLocked()
				s.mu.Unlock()
			}
		}
	}()
}
func (s *Service) record(item observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generationMu.Lock()
	generation := s.generation[item.profile]
	s.generationMu.Unlock()
	if item.generation != generation {
		return nil
	}
	items := s.clean(s.data[item.profile])
	for _, existing := range items {
		if existing.TMDBID == item.item.TMDBID && existing.MediaType == item.item.MediaType && item.item.SearchedAt.Sub(existing.SearchedAt) < 24*time.Hour {
			return nil
		}
	}
	next := []models.RecommendationInterest{item.item}
	for _, existing := range items {
		if existing.TMDBID != item.item.TMDBID || existing.MediaType != item.item.MediaType {
			next = append(next, existing)
		}
	}
	next = s.clean(next)
	if err := s.persistLocked(item.profile, next); err != nil {
		return err
	}
	s.data[item.profile] = next
	s.trimLocked()
	return nil
}
func (s *Service) Clear(profile string) error {
	s.generationMu.Lock()
	s.generation[profile]++
	s.generationMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(filepath.Join(s.directory, digest(profile))); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.data, profile)
	return nil
}
func (s *Service) persistLocked(profile string, items []models.RecommendationInterest) error {
	data, err := json.Marshal(snapshot{ProfileID: profile, Items: items})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.directory, "interest-*.tmp")
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
	return os.Rename(name, filepath.Join(s.directory, digest(profile)))
}
func (s *Service) trimLocked() {
	total := 0
	for profile, items := range s.data {
		clean := s.clean(items)
		if len(clean) == 0 {
			delete(s.data, profile)
			_ = os.Remove(filepath.Join(s.directory, digest(profile)))
			continue
		}
		s.data[profile] = clean
		if len(clean) != len(items) {
			_ = s.persistLocked(profile, clean)
		}
		total += len(clean)
	}
	for total > MaxTotal {
		oldestProfile := ""
		var oldest time.Time
		for profile, items := range s.data {
			if len(items) == 0 {
				continue
			}
			at := items[len(items)-1].SearchedAt
			if oldestProfile == "" || at.Before(oldest) {
				oldestProfile = profile
				oldest = at
			}
		}
		if oldestProfile == "" {
			return
		}
		items := s.data[oldestProfile]
		s.data[oldestProfile] = items[:len(items)-1]
		_ = s.persistLocked(oldestProfile, s.data[oldestProfile])
		total--
	}
}
