package liveusage

import (
	"strings"
	"sync"
	"time"
)

type ActiveRecording struct {
	ID        string
	ProfileID string
	StartedAt time.Time
}

type Tracker struct {
	mu         sync.RWMutex
	recordings map[string]ActiveRecording
}

var globalTracker = &Tracker{
	recordings: make(map[string]ActiveRecording),
}

func GetTracker() *Tracker {
	return globalTracker
}

func (t *Tracker) StartRecording(id, profileID string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordings[id] = ActiveRecording{
		ID:        id,
		ProfileID: strings.TrimSpace(profileID),
		StartedAt: time.Now(),
	}
}

func (t *Tracker) EndRecording(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.recordings, id)
}

func (t *Tracker) ListRecordings() []ActiveRecording {
	t.mu.RLock()
	defer t.mu.RUnlock()

	recordings := make([]ActiveRecording, 0, len(t.recordings))
	for _, recording := range t.recordings {
		recordings = append(recordings, recording)
	}
	return recordings
}
