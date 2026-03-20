package debrid

import (
	"fmt"
	"strings"
)

// resolveRestrictedLink returns the Real-Debrid link corresponding to the provided file ID.
// When fileID is empty or not found, it falls back to the first available link.
// Also returns the filename from the matched file.
func resolveRestrictedLink(info *TorrentInfo, fileID string) (link string, filename string, index int, matched bool) {
	if info == nil || len(info.Links) == 0 {
		return "", "", 0, false
	}

	if strings.TrimSpace(fileID) == "" || len(info.Files) == 0 {
		// No fileID specified - return first link and first selected file's path as filename
		if len(info.Files) > 0 {
			for _, file := range info.Files {
				if file.Selected == 1 {
					filename = file.Path
					break
				}
			}
		}
		return info.Links[0], filename, 0, false
	}

	target := strings.TrimSpace(fileID)
	linkIndex := 0

	for _, file := range info.Files {
		if file.Selected == 0 {
			continue
		}
		if fmt.Sprintf("%d", file.ID) == target {
			if linkIndex >= len(info.Links) {
				// RAR pack: all files share the single link
				return info.Links[0], file.Path, 0, true
			}
			return info.Links[linkIndex], file.Path, linkIndex, true
		}
		linkIndex++
	}

	// Fallback to first link and first selected file's path
	if len(info.Files) > 0 {
		for _, file := range info.Files {
			if file.Selected == 1 {
				filename = file.Path
				break
			}
		}
	}
	return info.Links[0], filename, 0, false
}
