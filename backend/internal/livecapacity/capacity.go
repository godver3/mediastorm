// Package livecapacity arbitrates provider connections across Live TV, probes,
// and recordings in one server process. A lease is retained until its input has
// closed, including while a live session is reconnecting.
package livecapacity

import (
	"errors"
	"sync"
)

var ErrLimit = errors.New("live provider connection limit reached")

var Default = NewManager()

type Manager struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	next    uint64
}

type bucket struct {
	limit       int
	connections map[uint64]int
	shared      map[string]uint64
}

type Lease struct {
	manager    *Manager
	bucket     string
	connection uint64
	released   bool
}

func NewManager() *Manager { return &Manager{buckets: make(map[string]*bucket)} }

// Acquire atomically reserves a connection. Only consumers actually using the
// shared upstream hub may supply a share key; private inputs use an empty key.
// Zero preserves the existing unconfigured-limit policy. A stricter limit from
// an alias applies to the whole bucket until it becomes idle.
func (m *Manager) Acquire(key, shareKey string, limit int) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[key]
	if b == nil {
		b = &bucket{connections: make(map[uint64]int), shared: make(map[string]uint64)}
		m.buckets[key] = b
	}
	if limit > 0 && (b.limit == 0 || limit < b.limit) {
		b.limit = limit
	}
	if id, ok := b.shared[shareKey]; shareKey != "" && ok {
		b.connections[id]++
		return &Lease{manager: m, bucket: key, connection: id}, nil
	}
	if b.limit > 0 && len(b.connections) >= b.limit {
		return nil, ErrLimit
	}
	m.next++
	id := m.next
	b.connections[id] = 1
	if shareKey != "" {
		b.shared[shareKey] = id
	}
	return &Lease{manager: m, bucket: key, connection: id}, nil
}

// MakePrivate corrects an optimistic TS share when the provider unexpectedly
// returns HLS. Each FFmpeg then opens its own input and needs a separate slot.
// No provider input may open if this conversion fails.
func (l *Lease) MakePrivate() error {
	if l == nil {
		return nil
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.released {
		return ErrLimit
	}
	b := m.buckets[l.bucket]
	if b.connections[l.connection] > 1 {
		if b.limit > 0 && len(b.connections) >= b.limit {
			return ErrLimit
		}
		b.connections[l.connection]--
		m.next++
		l.connection = m.next
		b.connections[l.connection] = 1
	}
	for key, id := range b.shared {
		if id == l.connection {
			delete(b.shared, key)
		}
	}
	return nil
}

func (l *Lease) Release() {
	if l == nil {
		return
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	b := m.buckets[l.bucket]
	b.connections[l.connection]--
	if b.connections[l.connection] == 0 {
		delete(b.connections, l.connection)
		for key, id := range b.shared {
			if id == l.connection {
				delete(b.shared, key)
			}
		}
	}
	if len(b.connections) == 0 {
		delete(m.buckets, l.bucket)
	}
}

func (m *Manager) Current(key string) int {
	current, _ := m.Usage(key, 0)
	return current
}

// Usage includes reservations still opening or shutting down. Report the same
// effective limit that admission enforces, including a stricter active alias.
func (m *Manager) Usage(key string, limit int) (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.buckets[key]; b != nil {
		if b.limit > 0 && (limit <= 0 || b.limit < limit) {
			limit = b.limit
		}
		return len(b.connections), limit
	}
	return 0, limit
}
