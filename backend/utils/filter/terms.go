package filter

import (
	"log"
	"regexp"
	"strconv"
	"strings"
)

// CompiledTerm holds either a plain substring or a compiled regex for matching.
type CompiledTerm struct {
	plain  string         // lowercased substring (used when regex is nil)
	regex  *regexp.Regexp // compiled regex (nil for plain terms)
	Weight int            // per-term weight (default 1, parsed from "term=N" suffix)
}

// ParseTermWeight splits a raw term string on the last "=" to extract
// the term text and an optional weight. For example:
//
//	"DV=3"      → ("DV", 3)
//	"REMUX"     → ("REMUX", 1)
//	"/foo=bar/" → ("/foo=bar/", 1)   (no trailing int)
//	"/x26[45]/=2" → ("/x26[45]/", 2)
//
// Weight must be a positive integer; invalid or missing weights default to 1.
func ParseTermWeight(raw string) (string, int) {
	idx := strings.LastIndex(raw, "=")
	if idx < 0 || idx == len(raw)-1 {
		return raw, 1
	}
	weightStr := raw[idx+1:]
	w, err := strconv.Atoi(weightStr)
	if err != nil || w < 1 {
		return raw, 1
	}
	term := raw[:idx]
	if term == "" {
		return raw, 1
	}
	return term, w
}

// CompileTerms pre-compiles a list of term strings into CompiledTerms.
// Terms wrapped in /slashes/ are treated as case-insensitive regex.
// Invalid regex falls back to a plain substring match on the entire string (including slashes).
// Empty/whitespace-only terms are skipped.
// Terms may include a "=N" weight suffix (e.g. "DV=3"); see ParseTermWeight.
func CompileTerms(terms []string) []CompiledTerm {
	compiled := make([]CompiledTerm, 0, len(terms))
	for _, raw := range terms {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		term, weight := ParseTermWeight(trimmed)

		// Check for /regex/ syntax
		if len(term) >= 3 && term[0] == '/' && term[len(term)-1] == '/' {
			pattern := term[1 : len(term)-1]
			re, err := regexp.Compile("(?i)" + pattern)
			if err == nil {
				compiled = append(compiled, CompiledTerm{regex: re, Weight: weight})
				continue
			}
			log.Printf("[filter] Invalid regex in term %q: %v — falling back to plain substring", term, err)
			// Invalid regex: fall back to plain substring of the whole string
		}

		// Auto-detect anchored regex patterns (^ or $) without /slashes/
		if strings.HasPrefix(term, "^") || strings.HasSuffix(term, "$") {
			re, err := regexp.Compile("(?i)" + term)
			if err == nil {
				compiled = append(compiled, CompiledTerm{regex: re, Weight: weight})
				continue
			}
			log.Printf("[filter] Term %q looks like regex (has anchor) but failed to compile: %v — falling back to plain substring", term, err)
		}

		compiled = append(compiled, CompiledTerm{plain: strings.ToLower(term), Weight: weight})
	}
	return compiled
}

// MatchedTerm returns the first matching term string, or "" if none match.
// Used to provide rejection reasons in filter details.
func MatchedTerm(title string, terms []CompiledTerm) string {
	if len(terms) == 0 {
		return ""
	}
	titleLower := strings.ToLower(title)
	for _, t := range terms {
		if t.regex != nil {
			if t.regex.MatchString(title) {
				return t.regex.String()
			}
		} else {
			if strings.Contains(titleLower, t.plain) {
				return t.plain
			}
		}
	}
	return ""
}

// SumMatchedWeights returns the sum of weights of all matching terms and
// a slice of matched term display names. If no terms match, totalWeight is 0.
func SumMatchedWeights(title string, terms []CompiledTerm) (totalWeight int, matchedNames []string) {
	if len(terms) == 0 {
		return 0, nil
	}
	titleLower := strings.ToLower(title)
	for _, t := range terms {
		matched := false
		name := ""
		if t.regex != nil {
			if t.regex.MatchString(title) {
				matched = true
				name = t.regex.String()
			}
		} else {
			if strings.Contains(titleLower, t.plain) {
				matched = true
				name = t.plain
			}
		}
		if matched {
			totalWeight += t.Weight
			matchedNames = append(matchedNames, name)
		}
	}
	return totalWeight, matchedNames
}

// MatchedTermWithWeight returns the first matching term string and its weight,
// or ("", 0) if none match.
func MatchedTermWithWeight(title string, terms []CompiledTerm) (string, int) {
	if len(terms) == 0 {
		return "", 0
	}
	titleLower := strings.ToLower(title)
	for _, t := range terms {
		if t.regex != nil {
			if t.regex.MatchString(title) {
				return t.regex.String(), t.Weight
			}
		} else {
			if strings.Contains(titleLower, t.plain) {
				return t.plain, t.Weight
			}
		}
	}
	return "", 0
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
