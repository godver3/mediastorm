package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
)

// Version and BuildID are read from version.txt (line 1 = version, line 2 = build ID)
var (
	version     string
	buildID     string
	versionOnce sync.Once
)

// buildCommit is the backend's git commit, set at build time with
// -ldflags "-X novastream/handlers.buildCommit=<sha>".
//
// version.txt's build ID is frozen at 20261115: the legacy Expo apps compare it
// against their own build ID for update prompts, so it must not move. The commit
// digest is what identifies a backend build now.
var buildCommit string

// GetBackendCommit returns the short (8-char) git commit of this build, falling
// back to the Go toolchain's embedded VCS revision, then "dev".
func GetBackendCommit() string {
	commit := strings.TrimSpace(buildCommit)
	if commit == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					commit = setting.Value
				}
			}
		}
	}
	if commit == "" {
		return "dev"
	}
	if len(commit) > 8 {
		commit = commit[:8]
	}
	return commit
}

type VersionHandler struct{}

type VersionResponse struct {
	Version string `json:"version"`
	BuildID string `json:"buildId"`
	Commit  string `json:"commit"`
}

func NewVersionHandler() *VersionHandler {
	return &VersionHandler{}
}

func readVersionFile() {
	versionOnce.Do(func() {
		paths := []string{}
		if configured := strings.TrimSpace(os.Getenv("STRMR_VERSION_FILE")); configured != "" {
			paths = append(paths, configured)
		}
		if executable, err := os.Executable(); err == nil {
			paths = append(paths,
				filepath.Join(filepath.Dir(executable), "version.txt"),
				filepath.Join(filepath.Dir(executable), "app", "version.txt"),
			)
		}
		paths = append(paths,
			"version.txt",         // Current directory (backend/)
			"backend/version.txt", // From repo root
			"/app/version.txt",    // Docker container path
		)

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
		Commit:  GetBackendCommit(),
	})
}
