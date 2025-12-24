package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Version is set at build time or read from version.txt
var (
	version     string
	versionOnce sync.Once
)

type VersionHandler struct{}

type VersionResponse struct {
	Version string `json:"version"`
}

func NewVersionHandler() *VersionHandler {
	return &VersionHandler{}
}

// GetBackendVersion reads the version from version.txt (cached after first read)
func GetBackendVersion() string {
	versionOnce.Do(func() {
		// Try multiple locations for version.txt
		paths := []string{
			"version.txt",         // Current directory (backend/)
			"backend/version.txt", // From repo root
			"/app/version.txt",    // Docker container path
		}

		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err == nil {
				version = strings.TrimSpace(string(data))
				return
			}
		}

		// Fallback if version.txt not found
		version = "unknown"
	})
	return version
}

func (h *VersionHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(VersionResponse{
		Version: GetBackendVersion(),
	})
}
