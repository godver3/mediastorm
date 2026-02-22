package filter

import (
	"testing"
)

func TestGetAnimeLanguageTerms_English(t *testing.T) {
	pref, nonPref, _ := GetAnimeLanguageTerms("eng")
	if len(pref) == 0 {
		t.Fatal("expected preferred terms for eng, got none")
	}
	if len(nonPref) == 0 {
		t.Fatal("expected non-preferred terms for eng, got none")
	}

	// Verify key terms are present
	prefSet := toSet(pref)
	if _, ok := prefSet["dual audio"]; !ok {
		t.Error("expected 'dual audio' in preferred terms for eng")
	}
	if _, ok := prefSet["english dub"]; !ok {
		t.Error("expected 'english dub' in preferred terms for eng")
	}
	if _, ok := prefSet["dual-audio"]; !ok {
		t.Error("expected 'dual-audio' in preferred terms for eng")
	}

	// Verify preferred language terms are NOT in non-preferred
	nonPrefSet := toSet(nonPref)
	if _, ok := nonPrefSet["english dub"]; ok {
		t.Error("'english dub' should not appear in non-preferred terms for eng")
	}
}

func TestGetAnimeLanguageTerms_Japanese(t *testing.T) {
	pref, nonPref, _ := GetAnimeLanguageTerms("jpn")
	if len(pref) == 0 {
		t.Fatal("expected preferred terms for jpn, got none")
	}

	prefSet := toSet(pref)
	if _, ok := prefSet["japanese"]; !ok {
		t.Error("expected 'japanese' in preferred terms for jpn")
	}

	// Verify "dual audio" is NOT preferred for jpn (raw Japanese preference)
	if _, ok := prefSet["dual audio"]; ok {
		t.Error("'dual audio' should not be in preferred terms for jpn")
	}

	if len(nonPref) == 0 {
		t.Fatal("expected non-preferred terms for jpn, got none")
	}
}

func TestGetAnimeLanguageTerms_AllLanguages(t *testing.T) {
	languages := []string{"eng", "spa", "fra", "deu", "ita", "por", "jpn"}
	for _, lang := range languages {
		pref, nonPref, _ := GetAnimeLanguageTerms(lang)
		if len(pref) == 0 {
			t.Errorf("expected preferred terms for %s, got none", lang)
		}
		if len(nonPref) == 0 {
			t.Errorf("expected non-preferred terms for %s, got none", lang)
		}
	}
}

func TestGetAnimeLanguageTerms_FilterOutRaw(t *testing.T) {
	// Non-Japanese languages should filter out raw releases
	for _, lang := range []string{"eng", "spa", "fra", "deu", "ita", "por"} {
		_, _, filterOut := GetAnimeLanguageTerms(lang)
		if len(filterOut) == 0 {
			t.Errorf("expected filter-out terms for %s, got none", lang)
		}
	}
	// Japanese should NOT filter out raw releases (raw is preferred for jpn)
	_, _, filterOut := GetAnimeLanguageTerms("jpn")
	if len(filterOut) != 0 {
		t.Errorf("expected no filter-out terms for jpn, got %v", filterOut)
	}
}

func TestGetAnimeLanguageTerms_UnknownLanguage(t *testing.T) {
	pref, nonPref, _ := GetAnimeLanguageTerms("xyz")
	if pref != nil {
		t.Errorf("expected nil preferred for unknown language, got %v", pref)
	}
	if nonPref != nil {
		t.Errorf("expected nil non-preferred for unknown language, got %v", nonPref)
	}
}

func TestGetAnimeLanguageTerms_NoSelfReference(t *testing.T) {
	// For each language, its own language-specific dub terms should not
	// appear in its non-preferred list.
	langDubTerms := map[string]string{
		"eng": "english dub",
		"spa": "spanish dub",
		"fra": "french dub",
		"deu": "german dub",
		"ita": "italian dub",
		"por": "portuguese dub",
	}

	for lang, ownDub := range langDubTerms {
		_, nonPref, _ := GetAnimeLanguageTerms(lang)
		nonPrefSet := toSet(nonPref)
		if _, ok := nonPrefSet[ownDub]; ok {
			t.Errorf("language %s: own dub term %q should not be in non-preferred list", lang, ownDub)
		}
	}
}

func toSet(terms []string) map[string]struct{} {
	s := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		s[t] = struct{}{}
	}
	return s
}
