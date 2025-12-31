package parsett

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// getScriptPaths returns the paths to the Python script and venv python executable.
// It first checks for Docker paths (scripts at /), then falls back to local development paths.
func getScriptPaths(scriptName string) (scriptPath, pythonPath string, err error) {
	// Docker paths (scripts copied to / in container)
	dockerScript := "/" + scriptName
	dockerPython := "/.venv/bin/python3"

	if _, err := os.Stat(dockerScript); err == nil {
		if _, err := os.Stat(dockerPython); err == nil {
			return dockerScript, dockerPython, nil
		}
	}

	// Local development paths (scripts in backend/, venv in project root)
	_, currentFile, _, ok := runtime.Caller(1)
	if !ok {
		return "", "", fmt.Errorf("failed to get current file path")
	}

	// From backend/utils/parsett/, go up 2 levels to backend/
	scriptPath = filepath.Join(filepath.Dir(currentFile), "..", "..", scriptName)
	// From backend/utils/parsett/, go up 3 levels to project root for .venv
	pythonPath = filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".venv", "bin", "python3")

	return scriptPath, pythonPath, nil
}

// ParsedTitle represents the result from PTT's parse_title function
type ParsedTitle struct {
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	Quality    string   `json:"quality,omitempty"`
	Codec      string   `json:"codec,omitempty"`
	Audio      []string `json:"audio,omitempty"`      // Can be array
	Channels   []string `json:"channels,omitempty"`   // Audio channels like 5.1
	Group      string   `json:"group,omitempty"`
	Container  string   `json:"container,omitempty"`
	Episodes   []int    `json:"episodes,omitempty"`
	Seasons    []int    `json:"seasons,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Extended   bool     `json:"extended,omitempty"`
	Hardcoded  bool     `json:"hardcoded,omitempty"`
	Proper     bool     `json:"proper,omitempty"`
	Repack     bool     `json:"repack,omitempty"`
	Complete   bool     `json:"complete,omitempty"`   // Complete series/season pack
	Site       string   `json:"site,omitempty"`
	BitDepth   string   `json:"bit_depth,omitempty"`  // e.g., "10bit"
	HDR        []string `json:"hdr,omitempty"`        // HDR formats like DV, HDR, HDR10+
}

// ParseTitle calls the Python PTT library to parse a media title
// Returns a ParsedTitle struct with the parsed information
func ParseTitle(title string) (*ParsedTitle, error) {
	scriptPath, venvPython, err := getScriptPaths("parse_title.py")
	if err != nil {
		return nil, err
	}

	// Execute the Python script with the title as an argument
	cmd := exec.Command(venvPython, scriptPath, title)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("python script error: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("failed to execute python script: %w", err)
	}

	// Parse the JSON output
	var result ParsedTitle
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON output: %w (output: %s)", err, string(output))
	}

	return &result, nil
}

// BatchResult represents a single result from batch parsing
type BatchResult struct {
	Title  string       `json:"title"`
	Parsed *ParsedTitle `json:"parsed"`
	Error  string       `json:"error"`
}

// ParseTitleBatch parses multiple titles in a single Python subprocess call
// This is much faster than calling ParseTitle repeatedly (100x speedup for 50 items)
// Returns a map of title -> parsed result
func ParseTitleBatch(titles []string) (map[string]*ParsedTitle, error) {
	if len(titles) == 0 {
		return make(map[string]*ParsedTitle), nil
	}

	scriptPath, venvPython, err := getScriptPaths("parse_title_batch.py")
	if err != nil {
		return nil, err
	}

	// Build command with all titles as arguments
	args := append([]string{scriptPath}, titles...)
	cmd := exec.Command(venvPython, args...)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("python batch script error: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("failed to execute python batch script: %w", err)
	}

	// Parse the JSON array output
	var results []BatchResult
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to parse batch JSON output: %w (output: %s)", err, string(output))
	}

	// Build result map
	resultMap := make(map[string]*ParsedTitle, len(results))
	for _, r := range results {
		if r.Error != "" {
			// Store nil for failed parses (will be treated as parse errors in filter)
			resultMap[r.Title] = nil
		} else {
			resultMap[r.Title] = r.Parsed
		}
	}

	return resultMap, nil
}
