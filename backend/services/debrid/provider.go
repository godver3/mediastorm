package debrid

import (
	"context"
	"fmt"
	"sync"
)

// Provider defines the interface that all debrid providers must implement.
// This abstraction allows the debrid services to work with any provider
// without provider-specific switch statements.
type Provider interface {
	// Name returns the provider identifier (e.g., "realdebrid", "torbox").
	Name() string

	// AddMagnet adds a magnet link and returns the torrent/download ID.
	AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error)

	// AddTorrentFile uploads a .torrent file and returns the torrent/download ID.
	// This is used when only a torrent file URL is available (no magnet/infohash).
	AddTorrentFile(ctx context.Context, torrentData []byte, filename string) (*AddMagnetResult, error)

	// GetTorrentInfo retrieves information about a torrent by ID.
	GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error)

	// SelectFiles selects which files to download from a torrent.
	// Pass "all" to select all files, or a comma-separated list of file IDs.
	SelectFiles(ctx context.Context, torrentID string, fileIDs string) error

	// DeleteTorrent removes a torrent from the provider.
	DeleteTorrent(ctx context.Context, torrentID string) error

	// UnrestrictLink converts a restricted link to an actual download URL.
	UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error)

	// CheckInstantAvailability checks if a torrent hash is cached.
	// Note: Some providers may not support this (Real-Debrid deprecated it).
	CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error)
}

// Configurable is an optional interface for providers that support runtime configuration.
type Configurable interface {
	// Configure sets provider-specific options from a config map.
	Configure(config map[string]string)
}

// AddMagnetResult contains the result of adding a magnet link.
type AddMagnetResult struct {
	ID  string // Provider-specific torrent/download ID
	URI string // Optional: URI for the added item
}

// UnrestrictResult contains the result of unrestricting a link.
type UnrestrictResult struct {
	ID          string // Provider-specific ID
	Filename    string // Resolved filename
	MimeType    string // MIME type of the file
	Filesize    int64  // Size in bytes
	DownloadURL string // Direct download URL
}

// ProviderFactory is a function that creates a new Provider instance with the given API key.
type ProviderFactory func(apiKey string) Provider

// Registry manages registered debrid provider factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]ProviderFactory
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]ProviderFactory),
	}
}

// Register adds a provider factory to the registry.
func (r *Registry) Register(name string, factory ProviderFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Get retrieves a provider factory by name and creates a new instance.
func (r *Registry) Get(name, apiKey string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[name]
	if !ok {
		return nil, false
	}
	return factory(apiKey), true
}

// MustGet retrieves a provider by name or returns an error.
func (r *Registry) MustGet(name, apiKey string) (Provider, error) {
	p, ok := r.Get(name, apiKey)
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", name)
	}
	return p, nil
}

// IsRegistered checks if a provider is registered.
func (r *Registry) IsRegistered(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.factories[name]
	return ok
}

// List returns all registered provider names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry is the global provider registry.
var DefaultRegistry = NewRegistry()

// RegisterProvider registers a provider factory with the default registry.
func RegisterProvider(name string, factory ProviderFactory) {
	DefaultRegistry.Register(name, factory)
}

// GetProvider retrieves a provider from the default registry.
func GetProvider(name, apiKey string) (Provider, bool) {
	return DefaultRegistry.Get(name, apiKey)
}
