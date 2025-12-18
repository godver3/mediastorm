package config

import (
	"sync"

	"github.com/javi11/nntppool"
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

	return &AltMountConfig{
		RClone: RCloneConfig{
			Password: "", // Not used in NovaStream
			Salt:     "", // Not used in NovaStream
		},
		Streaming: StreamingConfig{
			MaxDownloadWorkers: settings.Streaming.MaxDownloadWorkers,
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
	// Close idle sockets aggressively so providers with short idle limits don't reset them out from under us.
	defaultMaxConnectionIdleTimeSeconds = 120
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
