package importer

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// Single 7z archive: filename.7z
	sz7zPattern = regexp.MustCompile(`^(.+)\.7z$`)
	// Multipart 7z archive: filename.7z.001, filename.7z.002, etc.
	szMultiPattern = regexp.MustCompile(`^(.+)\.7z\.(\d+)$`)
)

// get7zPartNumber extracts the part number from a 7z filename
// Returns 0 for .7z (first/only part), or the part number for .7z.001 style
func get7zPartNumber(filename string) int {
	lowerFilename := strings.ToLower(filename)

	// Pattern: filename.7z.001, filename.7z.002, etc.
	if matches := szMultiPattern.FindStringSubmatch(filename); len(matches) > 2 {
		if num := parseInt(matches[2]); num >= 0 {
			return num
		}
	}

	// Pattern: filename.7z (single file or first part)
	if strings.HasSuffix(lowerFilename, ".7z") {
		return 0
	}

	return 999999 // Unknown format goes last
}

// extractBase7zFilename extracts the base filename without the 7z part suffix
func extractBase7zFilename(filename string) string {
	// Try multipart pattern first: filename.7z.001
	if matches := szMultiPattern.FindStringSubmatch(filename); len(matches) > 1 {
		return matches[1]
	}

	// Try single 7z pattern: filename.7z
	if matches := sz7zPattern.FindStringSubmatch(filename); len(matches) > 1 {
		return matches[1]
	}

	// Fallback: return filename without extension
	return strings.TrimSuffix(filename, filepath.Ext(filename))
}

// get7zPartSuffix extracts the suffix portion (.7z or .7z.001)
func get7zPartSuffix(originalFileName string) string {
	if matches := szMultiPattern.FindStringSubmatch(originalFileName); len(matches) > 2 {
		return ".7z." + matches[2]
	}

	if sz7zPattern.MatchString(originalFileName) {
		return ".7z"
	}

	return filepath.Ext(originalFileName)
}

// rename7zFilesAndSort normalizes 7z filenames and sorts them by part number
func rename7zFilesAndSort(szFiles []ParsedFile) []ParsedFile {
	if len(szFiles) == 0 {
		return nil
	}

	// Get the base name of the first 7z file
	firstFileBase := extractBase7zFilename(szFiles[0].Filename)

	type szFileWithPart struct {
		file ParsedFile
		part int
	}

	withParts := make([]szFileWithPart, len(szFiles))

	for i, sf := range szFiles {
		partSuffix := get7zPartSuffix(sf.Filename)
		sf.Filename = firstFileBase + partSuffix

		withParts[i] = szFileWithPart{
			file: sf,
			part: get7zPartNumber(sf.Filename),
		}
	}

	sort.SliceStable(withParts, func(i, j int) bool {
		return withParts[i].part < withParts[j].part
	})

	renamed := make([]ParsedFile, len(withParts))
	for i := range withParts {
		renamed[i] = withParts[i].file
	}

	return renamed
}

// getFirst7zPart finds and returns the filename of the first part of a 7z archive
func getFirst7zPart(szFileNames []string) (string, error) {
	if len(szFileNames) == 0 {
		return "", NewNonRetryableError("no 7z files provided", nil)
	}

	// If only one file, return it
	if len(szFileNames) == 1 {
		return szFileNames[0], nil
	}

	// Find files that are first parts (part 0)
	type candidateFile struct {
		filename string
		partNum  int
		priority int // Lower = higher priority
	}

	var candidates []candidateFile

	for _, filename := range szFileNames {
		partNum := get7zPartNumber(filename)

		// Only consider files that are first parts
		if partNum != 0 {
			continue
		}

		// Determine priority based on file pattern
		priority := 1 // .7z files have highest priority
		if szMultiPattern.MatchString(filename) {
			priority = 2 // .7z.001 files have lower priority
		}

		candidates = append(candidates, candidateFile{
			filename: filename,
			partNum:  partNum,
			priority: priority,
		})
	}

	if len(candidates) == 0 {
		// No first part found, try to find .7z.001
		for _, filename := range szFileNames {
			if matches := szMultiPattern.FindStringSubmatch(filename); len(matches) > 2 {
				if num := parseInt(matches[2]); num == 1 {
					return filename, nil
				}
			}
		}
		return "", NewNonRetryableError("no valid first 7z part found in archive", nil)
	}

	// Sort by priority, then filename for consistency
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.priority < best.priority ||
			(candidate.priority == best.priority && candidate.filename < best.filename) {
			best = candidate
		}
	}

	return best.filename, nil
}
