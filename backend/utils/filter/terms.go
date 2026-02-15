package filter

import (
	"regexp"
	"strings"
)

// CompiledTerm holds either a plain substring or a compiled regex for matching.
type CompiledTerm struct {
	plain string         // lowercased substring (used when regex is nil)
	regex *regexp.Regexp // compiled regex (nil for plain terms)
}

// CompileTerms pre-compiles a list of term strings into CompiledTerms.
// Terms wrapped in /slashes/ are treated as case-insensitive regex.
// Invalid regex falls back to a plain substring match on the entire string (including slashes).
// Empty/whitespace-only terms are skipped.
func CompileTerms(terms []string) []CompiledTerm {
	compiled := make([]CompiledTerm, 0, len(terms))
	for _, raw := range terms {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		// Check for /regex/ syntax
		if len(trimmed) >= 3 && trimmed[0] == '/' && trimmed[len(trimmed)-1] == '/' {
			pattern := trimmed[1 : len(trimmed)-1]
			re, err := regexp.Compile("(?i)" + pattern)
			if err == nil {
				compiled = append(compiled, CompiledTerm{regex: re})
				continue
			}
			// Invalid regex: fall back to plain substring of the whole string
		}

		compiled = append(compiled, CompiledTerm{plain: strings.ToLower(trimmed)})
	}
	return compiled
}

// MatchesAnyTerm checks if the title matches any of the compiled terms.
// Returns false if terms is empty.
func MatchesAnyTerm(title string, terms []CompiledTerm) bool {
	if len(terms) == 0 {
		return false
	}
	titleLower := strings.ToLower(title)
	for _, t := range terms {
		if t.regex != nil {
			if t.regex.MatchString(title) {
				return true
			}
		} else {
			if strings.Contains(titleLower, t.plain) {
				return true
			}
		}
	}
	return false
}
