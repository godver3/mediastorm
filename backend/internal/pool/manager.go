package pool

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/javi11/nntppool"
)

// Manager provides centralized NNTP connection pool management
type Manager interface {
	// GetPool returns the current connection pool, recreating it from the last
	// configured providers if it was cleared (e.g. by a cold-test flush) so a
	// cleared pool is not permanently broken.
	GetPool() (nntppool.UsenetConnectionPool, error)

	// SetProviders creates/recreates the pool with new providers
	SetProviders(providers []nntppool.UsenetProviderConfig) error

	// ClearPool shuts down and drops the live pool. The provider configuration
	// is retained, so the next GetPool reconstitutes it with fresh connections
	// (a "cold pool" rather than a disabled one).
	ClearPool() error

	// HasPool returns true if a pool is currently available
	HasPool() bool
}

// manager implements the Manager interface
type manager struct {
	mu        sync.RWMutex
	pool      nntppool.UsenetConnectionPool
	providers []nntppool.UsenetProviderConfig // last configured set, rebuilt lazily after ClearPool
}

// NewManager creates a new pool manager
func NewManager() Manager {
	return &manager{}
}

// GetPool returns the current connection pool, recreating it from the last
// configured providers if it was cleared. Rebuilding under the write lock is
// safe: it only happens after a clear, and callers block briefly only then.
func (m *manager) GetPool() (nntppool.UsenetConnectionPool, error) {
	m.mu.RLock()
	pool := m.pool
	hasProviders := len(m.providers) > 0
	m.mu.RUnlock()

	if pool != nil {
		return pool, nil
	}
	if !hasProviders {
		return nil, fmt.Errorf("NNTP connection pool not available - no providers configured")
	}

	// No live pool but providers are configured: rebuild it (double-checked).
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pool != nil {
		return m.pool, nil
	}
	if err := m.buildPoolLocked(); err != nil {
		return nil, err
	}
	return m.pool, nil
}

// SetProviders creates/recreates the pool with new providers
func (m *manager) SetProviders(providers []nntppool.UsenetProviderConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.providers = append([]nntppool.UsenetProviderConfig(nil), providers...)

	// Shut down existing pool if present
	if m.pool != nil {
		slog.Info("Shutting down existing NNTP connection pool")
		m.pool.Quit()
		m.pool = nil
	}

	// Return early if no providers (clear pool scenario)
	if len(providers) == 0 {
		slog.Info("No NNTP providers configured - pool cleared")
		return nil
	}

	return m.buildPoolLocked()
}

// ClearPool shuts down and drops the live pool. Provider configuration is kept
// so the next GetPool recreates fresh connections — a cold pool, not a dead one.
func (m *manager) ClearPool() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pool != nil {
		slog.Info("Clearing NNTP connection pool (providers retained for lazy rebuild)")
		m.pool.Quit()
		m.pool = nil
	}

	return nil
}

// HasPool returns true if a pool is currently available
func (m *manager) HasPool() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.pool != nil
}

// buildPoolLocked creates the pool from the retained provider config. Caller
// must hold m.mu (write).
func (m *manager) buildPoolLocked() error {
	// Create new pool with providers
	// Keep MinConnections > 0 to maintain warm connections for faster health checks
	// MaxConnections is set per-provider from user config (UsenetSettings.Connections)
	slog.Info("Creating NNTP connection pool", "provider_count", len(m.providers))
	pool, err := nntppool.NewConnectionPool(nntppool.Config{
		Providers:      m.providers,
		Logger:         slog.Default(),
		DelayType:      nntppool.DelayTypeFixed,
		RetryDelay:     10 * time.Millisecond,
		MinConnections: 2, // Keep 2 warm connections per provider for faster STAT commands
	})
	if err != nil {
		return fmt.Errorf("failed to create NNTP connection pool: %w", err)
	}

	m.pool = pool
	slog.Info("NNTP connection pool created successfully")
	return nil
}
