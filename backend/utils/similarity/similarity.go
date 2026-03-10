package similarity

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Similarity calculates the similarity percentage between two strings
// using Levenshtein distance. Returns a value between 0.0 (completely different)
// and 1.0 (identical).
//
// Also handles suffix containment for titles with possessive prefixes like
// "Will Vinton's Claymation Christmas" vs "Claymation Christmas" - if one
// title is a suffix of the other and represents a substantial portion (>60%),
// returns a high similarity score.
func Similarity(s1, s2 string) float64 {
	// Normalize strings for comparison
	s1 = normalize(s1)
	s2 = normalize(s2)

	if s1 == s2 {
		return 1.0
	}

	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}

	// Check for suffix containment (handles "Disney's X" vs "X", "Will Vinton's X" vs "X")
	// If the shorter string is a suffix of the longer and is substantial, consider it a match
	if score := suffixContainmentScore(s1, s2); score > 0 {
		return score
	}

	distance := levenshteinDistance(s1, s2)
	maxLen := max(len(s1), len(s2))

	if maxLen == 0 {
		return 1.0
	}

	return 1.0 - float64(distance)/float64(maxLen)
}

// suffixContainmentScore returns a high similarity score if one string is a suffix
// of the other and represents a substantial portion of the longer string.
// Returns 0 if no suffix containment is found.
func suffixContainmentScore(s1, s2 string) float64 {
	longer, shorter := s1, s2
	if len(s1) < len(s2) {
		longer, shorter = s2, s1
	}

	// Check if shorter is a suffix of longer (with space boundary)
	if strings.HasSuffix(longer, shorter) {
		// Ensure the prefix ends at a word boundary (space before the suffix)
		prefixLen := len(longer) - len(shorter)
		if prefixLen == 0 || longer[prefixLen-1] == ' ' {
			// The shorter string must be substantial (>60% of the longer)
			ratio := float64(len(shorter)) / float64(len(longer))
			if ratio >= 0.6 {
				// Return high score proportional to how much of the title matches
				// 60% containment -> 0.92, 80% containment -> 0.96, 100% -> 1.0
				return 0.90 + (ratio * 0.10)
			}
		}
	}

	return 0
}

// normalize converts a string to lowercase and removes non-alphanumeric characters
// (except spaces) to make title comparison more forgiving.
// Also converts "&" to "and" for equivalence (e.g., "Me & You" matches "Me and You").
func normalize(s string) string {
	// Replace "&" with " and " before other processing
	s = strings.ReplaceAll(s, "&", " and ")

	// NFD decomposition splits accented characters into base + combining marks
	// e.g., "é" → "e" + combining acute accent
	s = norm.NFD.String(strings.ToLower(s))

	var result strings.Builder
	result.Grow(len(s))

	for _, r := range s {
		// Skip combining marks (diacritical marks like accents, umlauts, tildes)
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result.WriteRune(r)
		} else if unicode.IsSpace(r) || r == '.' || r == '-' || r == '_' {
			// Replace dots, dashes, and underscores with spaces
			result.WriteRune(' ')
		}
	}

	// Collapse multiple spaces into one
	normalized := strings.Join(strings.Fields(result.String()), " ")
	return strings.TrimSpace(normalized)
}

// levenshteinDistance calculates the edit distance between two strings
func levenshteinDistance(s1, s2 string) int {
	r1 := []rune(s1)
	r2 := []rune(s2)
	len1 := len(r1)
	len2 := len(r2)

	// Create a 2D slice for dynamic programming
	matrix := make([][]int, len1+1)
	for i := range matrix {
		matrix[i] = make([]int, len2+1)
	}

	// Initialize first column and row
	for i := 0; i <= len1; i++ {
		matrix[i][0] = i
	}
	for j := 0; j <= len2; j++ {
		matrix[0][j] = j
	}

	// Fill the matrix
	for i := 1; i <= len1; i++ {
		for j := 1; j <= len2; j++ {
			cost := 1
			if r1[i-1] == r2[j-1] {
				cost = 0
			}

			matrix[i][j] = min(
				matrix[i-1][j]+1,      // deletion
				matrix[i][j-1]+1,      // insertion
				matrix[i-1][j-1]+cost, // substitution
			)
		}
	}

	return matrix[len1][len2]
}

func min(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	minVal := values[0]
	for _, v := range values[1:] {
		if v < minVal {
			minVal = v
		}
	}
	return minVal
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
