package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Version and BuildID are read from version.txt (line 1 = version, line 2 = build ID)
var (
	version     string
	buildID     string
	versionOnce sync.Once
)

type VersionHandler struct{}

type VersionResponse struct {
	Version string `json:"version"`
	BuildID string `json:"buildId"`
}

func NewVersionHandler() *VersionHandler {
	return &VersionHandler{}
}

func readVersionFile() {
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
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) >= 1 {
					version = strings.TrimSpace(lines[0])
				}
				if len(lines) >= 2 {
					buildID = strings.TrimSpace(lines[1])
				}
				return
			}
		}

		// Fallback if version.txt not found
		version = "unknown"
		buildID = "unknown"
	})
}

// GetBackendVersion reads the version from version.txt (cached after first read)
func GetBackendVersion() string {
	readVersionFile()
	return version
}

// GetBackendBuildID reads the build ID from version.txt (cached after first read)
func GetBackendBuildID() string {
	readVersionFile()
	return buildID
}

func (h *VersionHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(VersionResponse{
		Version: GetBackendVersion(),
		BuildID: GetBackendBuildID(),
	})
}
