package config

import (
	"sync"

	"github.com/javi11/nntppool"
	"novastream/internal/usenet"
)

// AltMountConfig represents the subset of configuration needed by altmount packages
type AltMountConfig struct {
	RClone    RCloneConfig
	Streaming StreamingConfig
	Import    ImportConfig
	SABnzbd   SABnzbdConfig
	WebDAV    WebDAVConfig
}

// RCloneConfig represents rclone configuration (minimal for our use)
type RCloneConfig struct {
	Password string
	Salt     string
}

// StreamingConfig represents streaming configuration
type StreamingConfig struct {
	MaxDownloadWorkers int
	MaxCacheSizeMB     int
}

// ImportConfig represents import/queue processing configuration
type ImportConfig struct {
	QueueProcessingIntervalSeconds int
	RarMaxWorkers                  int
	RarMaxCacheSizeMB              int
	RarEnableMemoryPreload         bool
	RarMaxMemoryGB                 int
}

// SABnzbdConfig represents SABnzbd fallback configuration
type SABnzbdConfig struct {
	Enabled        *bool
	FallbackHost   string
	FallbackAPIKey string
}

// WebDAVConfig represents WebDAV configuration
type WebDAVConfig struct {
	Prefix   string
	User     string
	Password string
}

// ConfigGetter is a function that returns the current configuration
type ConfigGetter func() *AltMountConfig

// ConfigAdapter adapts NovaStream's Settings to altmount's ConfigGetter interface
type ConfigAdapter struct {
	manager *Manager
	mu      sync.RWMutex
}

// NewConfigAdapter creates a new config adapter
func NewConfigAdapter(manager *Manager) *ConfigAdapter {
	return &ConfigAdapter{
		manager: manager,
	}
}

// GetConfig returns the current configuration in altmount format
func (ca *ConfigAdapter) GetConfig() *AltMountConfig {
	ca.mu.RLock()
	defer ca.mu.RUnlock()

	settings, err := ca.manager.Load()
	if err != nil {
		// Return defaults on error
		return &AltMountConfig{
			Streaming: StreamingConfig{
				MaxDownloadWorkers: 15,
				MaxCacheSizeMB:     100,
			},
			Import: ImportConfig{
				QueueProcessingIntervalSeconds: 1,
				RarMaxWorkers:                  40,
				RarMaxCacheSizeMB:              128,
				RarEnableMemoryPreload:         true,
				RarMaxMemoryGB:                 8,
			},
		}
	}

	// Dynamically cap download workers based on provider connection limits
	// and the number of active usenet readers (concurrent streams).
	// Each stream gets an equal share of available connections minus headroom.
	maxWorkers := settings.Streaming.MaxDownloadWorkers
	totalConns := 0
	for _, u := range settings.Usenet {
		if u.Enabled && u.Host != "" {
			totalConns += u.Connections
		}
	}
	if totalConns > 0 {
		const headroom = 2 // reserve for STAT commands / health checks
		readers := int(usenet.ActiveReaders())
		if readers < 1 {
			readers = 1
		}
		perStreamCap := (totalConns - headroom) / readers
		if perStreamCap < 4 {
			perStreamCap = 4 // floor: at least 4 workers per stream
		}
		if maxWorkers > perStreamCap {
			maxWorkers = perStreamCap
		}
	}

	return &AltMountConfig{
		RClone: RCloneConfig{
			Password: "", // Not used in NovaStream
			Salt:     "", // Not used in NovaStream
		},
		Streaming: StreamingConfig{
			MaxDownloadWorkers: maxWorkers,
			MaxCacheSizeMB:     settings.Streaming.MaxCacheSizeMB,
		},
		Import: ImportConfig{
			QueueProcessingIntervalSeconds: settings.Import.QueueProcessingIntervalSeconds,
			RarMaxWorkers:                  settings.Import.RarMaxWorkers,
			RarMaxCacheSizeMB:              settings.Import.RarMaxCacheSizeMB,
			RarEnableMemoryPreload:         settings.Import.RarEnableMemoryPreload,
			RarMaxMemoryGB:                 settings.Import.RarMaxMemoryGB,
		},
		SABnzbd: SABnzbdConfig{
			Enabled:        settings.SABnzbd.Enabled,
			FallbackHost:   settings.SABnzbd.FallbackHost,
			FallbackAPIKey: settings.SABnzbd.FallbackAPIKey,
		},
		WebDAV: WebDAVConfig{
			Prefix:   settings.WebDAV.Prefix,
			User:     settings.WebDAV.Username,
			Password: settings.WebDAV.Password,
		},
	}
}

// GetConfigGetter returns a ConfigGetter function
func (ca *ConfigAdapter) GetConfigGetter() ConfigGetter {
	return ca.GetConfig
}

// ToNNTPProviders converts NovaStream usenet settings to NNTP provider configs
const (
	// Close idle sockets quickly so connections are freed promptly after playback stops,
	// leaving headroom for new streams without hitting provider connection limits.
	defaultMaxConnectionIdleTimeSeconds = 10
	// Give each connection a reasonable upper bound before we recycle it even if it stays active.
	defaultMaxConnectionTTLSeconds = 900
)

func ToNNTPProviders(settings []UsenetSettings) []nntppool.UsenetProviderConfig {
	providers := make([]nntppool.UsenetProviderConfig, 0, len(settings))

	for _, s := range settings {
		// Skip disabled providers or those without a host
		if !s.Enabled || s.Host == "" {
			continue
		}

		providers = append(providers, nntppool.UsenetProviderConfig{
			Host:                           s.Host,
			Port:                           s.Port,
			Username:                       s.Username,
			Password:                       s.Password,
			MaxConnections:                 s.Connections,
			TLS:                            s.SSL,
			MaxConnectionIdleTimeInSeconds: defaultMaxConnectionIdleTimeSeconds,
			MaxConnectionTTLInSeconds:      defaultMaxConnectionTTLSeconds,
		})
	}

	return providers
}
