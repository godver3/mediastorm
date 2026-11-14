package sourceavailability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type budget struct {
	StartedAt time.Time `json:"startedAt"`
	Checks    int       `json:"checks"`
}

// reserveCheck persists before provider work. Restarting the server does not
// reset the title-check budget, and a cache write failure starts no HTTP call.
func (s *Store) reserveCheck(now time.Time, interval time.Duration, limit int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.directory, "budget.json")
	var current budget
	if data, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(data, &current); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if current.StartedAt.IsZero() || now.Sub(current.StartedAt) >= interval || now.Before(current.StartedAt) {
		current = budget{StartedAt: now}
	}
	if current.Checks >= limit {
		return false, nil
	}
	current.Checks++
	data, err := json.Marshal(current)
	if err != nil {
		return false, err
	}
	file, err := os.CreateTemp(s.directory, "budget-*.tmp")
	if err != nil {
		return false, err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return false, err
	}
	if err = file.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(name, path); err != nil {
		return false, err
	}
	return true, nil
}
