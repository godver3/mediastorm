package language

import (
	"testing"
)

func TestNormalizeToCode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// ISO codes
		{"ISO code eng", "eng", "eng"},
		{"ISO code spa", "spa", "spa"},
		{"ISO code deu", "deu", "deu"},
		{"ISO code uppercase", "ENG", "eng"},
		{"ISO code mixed case", "Eng", "eng"},
		{"Alternative code chi", "chi", "chi"},
		{"Alternative code dut", "dut", "dut"},
		{"Alternative code cze", "cze", "cze"},

		// Emoji flags
		{"UK flag", "🇬🇧", "eng"},
		{"US flag", "🇺🇸", "eng"},
		{"Spain flag", "🇪🇸", "spa"},
		{"Mexico flag", "🇲🇽", "spa"},
		{"France flag", "🇫🇷", "fra"},
		{"Germany flag", "🇩🇪", "deu"},
		{"Japan flag", "🇯🇵", "jpn"},
		{"China flag", "🇨🇳", "zho"},
		{"Korea flag", "🇰🇷", "kor"},

		// Language names
		{"Name English", "English", "eng"},
		{"Name english lowercase", "english", "eng"},
		{"Name ENGLISH uppercase", "ENGLISH", "eng"},
		{"Name Spanish", "Spanish", "spa"},
		{"Name German", "German", "deu"},
		{"Name Japanese", "Japanese", "jpn"},
		{"Name Chinese", "Chinese", "zho"},
		{"Name Multi", "Multi", "mul"},

		// Edge cases
		{"Empty string", "", ""},
		{"Whitespace", "   ", ""},
		{"Whitespace around value", "  eng  ", "eng"},
		{"Unknown language", "klingon", ""},
		{"Unknown code", "xyz", ""},
		{"Two-letter code", "en", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeToCode(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeToCode(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestHasPreferredLanguage(t *testing.T) {
	tests := []struct {
		name            string
		resultLanguages string
		preferredCode   string
		expected        bool
	}{
		// Emoji flag matching
		{"UK flag matches eng", "🇬🇧", "eng", true},
		{"US flag matches eng", "🇺🇸", "eng", true},
		{"Spain flag matches spa", "🇪🇸", "spa", true},
		{"Germany flag matches deu", "🇩🇪", "deu", true},
		{"Japan flag matches jpn", "🇯🇵", "jpn", true},
		{"UK flag does not match spa", "🇬🇧", "spa", false},

		// Language name matching
		{"English name matches eng", "English", "eng", true},
		{"Spanish name matches spa", "Spanish", "spa", true},
		{"German name matches deu", "German", "deu", true},
		{"English name does not match spa", "English", "spa", false},

		// Multiple languages (comma-separated)
		{"Multi emoji first match", "🇬🇧,🇩🇪", "eng", true},
		{"Multi emoji second match", "🇬🇧,🇩🇪", "deu", true},
		{"Multi emoji no match", "🇬🇧,🇩🇪", "spa", false},
		{"Multi names first match", "English,German", "eng", true},
		{"Multi names second match", "English,German", "deu", true},
		{"Multi names no match", "English,German", "spa", false},
		{"Mixed format match", "🇬🇧,German,spa", "deu", true},

		// Whitespace handling
		{"Whitespace in list", " 🇬🇧 , 🇩🇪 ", "eng", true},
		{"Whitespace in pref code", "🇬🇧", "  eng  ", true},

		// Equivalent codes
		{"chi matches zho result", "Chinese", "chi", true},
		{"zho matches chi pref", "🇨🇳", "zho", true},
		{"dut matches nld result", "Dutch", "dut", true},
		{"cze matches ces result", "Czech", "cze", true},

		// Edge cases
		{"Empty result languages", "", "eng", false},
		{"Empty preferred code", "🇬🇧", "", false},
		{"Both empty", "", "", false},
		{"Unknown language in result", "Klingon", "eng", false},
		{"Only unknown languages", "Klingon,Elvish", "eng", false},
		{"Mixed known/unknown", "Klingon,English", "eng", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasPreferredLanguage(tt.resultLanguages, tt.preferredCode)
			if result != tt.expected {
				t.Errorf("HasPreferredLanguage(%q, %q) = %v, want %v",
					tt.resultLanguages, tt.preferredCode, result, tt.expected)
			}
		})
	}
}

func TestGetEquivalentCodes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"zho has equivalents", "zho", []string{"zho", "chi"}},
		{"chi has equivalents", "chi", []string{"zho", "chi"}},
		{"nld has equivalents", "nld", []string{"nld", "dut"}},
		{"dut has equivalents", "dut", []string{"nld", "dut"}},
		{"ces has equivalents", "ces", []string{"ces", "cze"}},
		{"cze has equivalents", "cze", []string{"ces", "cze"}},
		{"gre has equivalents", "gre", []string{"gre", "ell"}},
		{"ell has equivalents", "ell", []string{"gre", "ell"}},
		{"ron has equivalents", "ron", []string{"ron", "rum"}},
		{"rum has equivalents", "rum", []string{"ron", "rum"}},
		{"eng has no equivalents", "eng", []string{"eng"}},
		{"spa has no equivalents", "spa", []string{"spa"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getEquivalentCodes(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("getEquivalentCodes(%q) = %v, want %v", tt.input, result, tt.expected)
				return
			}
			// Check all expected codes are present (order may vary)
			for _, exp := range tt.expected {
				found := false
				for _, res := range result {
					if res == exp {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("getEquivalentCodes(%q) missing %q, got %v", tt.input, exp, result)
				}
			}
		})
	}
}
