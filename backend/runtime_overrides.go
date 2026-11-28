package main

import (
	"os"
	"strings"

	"novastream/config"
)

// applyRuntimeOverrides lets a platform launcher keep mutable data outside the
// application directory without rewriting the user's settings file. This is
// especially important for ZIP-based Windows updates, which replace app files
// but preserve %LOCALAPPDATA% data.
//
// The returned function maps a settings value back to what should be written
// to disk: the overridden fields keep the values that were loaded from
// settings.json, so env-derived paths are never persisted.
func applyRuntimeOverrides(settings *config.Settings) func(config.Settings) config.Settings {
	diskCacheDir := settings.Cache.Directory
	diskHLSTempDir := settings.Transmux.HLSTempDirectory
	overrodeCache, overrodeHLS := false, false
	if value := strings.TrimSpace(os.Getenv("STRMR_CACHE_DIR")); value != "" {
		settings.Cache.Directory = value
		overrodeCache = true
	}
	if value := strings.TrimSpace(os.Getenv("STRMR_HLS_TEMP_DIR")); value != "" {
		settings.Transmux.HLSTempDirectory = value
		overrodeHLS = true
	}
	return func(s config.Settings) config.Settings {
		if overrodeCache {
			s.Cache.Directory = diskCacheDir
		}
		if overrodeHLS {
			s.Transmux.HLSTempDirectory = diskHLSTempDir
		}
		return s
	}
}
