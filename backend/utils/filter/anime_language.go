package filter

// animeLanguageTerms maps ISO 639-2/B language codes to preferred and non-preferred
// terms for anime release ranking. Preferred terms boost releases matching the
// user's chosen language; non-preferred terms derank releases tagged with other languages.
// Language-specific dub terms (plain substring + hyphenated + ISO bracket tag regex).
// ISO tags like [ITA] use regex with word boundaries to avoid false positives.
var animeLanguageTerms = map[string]struct {
	Preferred    []string
	NonPreferred []string
	FilterOut    []string
}{
	"eng": {
		Preferred:    []string{"dual audio", "dual-audio", "multi audio", "multi-audio", "english dub", "english dubbed", "english-dub", `/\bEN-US\b/`, `/\[ENG\]/`},
		NonPreferred: []string{"german dub", "german dubbed", "german-dub", `/\[GER\]/`, "french dub", "french dubbed", "french-dub", `/\[FRE\]/`, "spanish dub", "spanish dubbed", "spanish-dub", `/\[SPA\]/`, "italian dub", "italian dubbed", "italian-dub", `/\[ITA\]/`, "portuguese dub", "portuguese dubbed", "portuguese-dub", `/\[POR\]/`},
		FilterOut:    []string{`/\braw\b/`},
	},
	"spa": {
		Preferred:    []string{"dual audio", "dual-audio", "multi audio", "multi-audio", "spanish dub", "spanish dubbed", "spanish-dub", "latino", `/\[SPA\]/`, `/\[ES-LA\]/`, `/\[ES-ES\]/`},
		NonPreferred: []string{"german dub", "german dubbed", "german-dub", `/\[GER\]/`, "french dub", "french dubbed", "french-dub", `/\[FRE\]/`, "english dub", "english dubbed", "english-dub", `/\[ENG\]/`, "italian dub", "italian dubbed", "italian-dub", `/\[ITA\]/`, "portuguese dub", "portuguese dubbed", "portuguese-dub", `/\[POR\]/`},
		FilterOut:    []string{`/\braw\b/`},
	},
	"fra": {
		Preferred:    []string{"dual audio", "dual-audio", "multi audio", "multi-audio", "french dub", "french dubbed", "french-dub", "vostfr", `/\[FRE\]/`},
		NonPreferred: []string{"german dub", "german dubbed", "german-dub", `/\[GER\]/`, "english dub", "english dubbed", "english-dub", `/\[ENG\]/`, "spanish dub", "spanish dubbed", "spanish-dub", `/\[SPA\]/`, "italian dub", "italian dubbed", "italian-dub", `/\[ITA\]/`, "portuguese dub", "portuguese dubbed", "portuguese-dub", `/\[POR\]/`},
		FilterOut:    []string{`/\braw\b/`},
	},
	"deu": {
		Preferred:    []string{"dual audio", "dual-audio", "multi audio", "multi-audio", "german dub", "german dubbed", "german-dub", `/\[GER\]/`},
		NonPreferred: []string{"french dub", "french dubbed", "french-dub", `/\[FRE\]/`, "english dub", "english dubbed", "english-dub", `/\[ENG\]/`, "spanish dub", "spanish dubbed", "spanish-dub", `/\[SPA\]/`, "italian dub", "italian dubbed", "italian-dub", `/\[ITA\]/`, "portuguese dub", "portuguese dubbed", "portuguese-dub", `/\[POR\]/`},
		FilterOut:    []string{`/\braw\b/`},
	},
	"ita": {
		Preferred:    []string{"dual audio", "dual-audio", "multi audio", "multi-audio", "italian dub", "italian dubbed", "italian-dub", `/\[ITA\]/`, `/\bITA\b.*\bBDRip\b/`, `/\bITA\b.*\bBD\b/`},
		NonPreferred: []string{"german dub", "german dubbed", "german-dub", `/\[GER\]/`, "french dub", "french dubbed", "french-dub", `/\[FRE\]/`, "english dub", "english dubbed", "english-dub", `/\[ENG\]/`, "spanish dub", "spanish dubbed", "spanish-dub", `/\[SPA\]/`, "portuguese dub", "portuguese dubbed", "portuguese-dub", `/\[POR\]/`},
		FilterOut:    []string{`/\braw\b/`},
	},
	"por": {
		Preferred:    []string{"dual audio", "dual-audio", "multi audio", "multi-audio", "portuguese dub", "portuguese dubbed", "portuguese-dub", `/\[POR\]/`, `/\[PT-BR\]/`},
		NonPreferred: []string{"german dub", "german dubbed", "german-dub", `/\[GER\]/`, "french dub", "french dubbed", "french-dub", `/\[FRE\]/`, "english dub", "english dubbed", "english-dub", `/\[ENG\]/`, "spanish dub", "spanish dubbed", "spanish-dub", `/\[SPA\]/`, "italian dub", "italian dubbed", "italian-dub", `/\[ITA\]/`},
		FilterOut:    []string{`/\braw\b/`},
	},
	"jpn": {
		Preferred:    []string{"japanese", `/\braw\b/`, `/\bjpn\b/`},
		NonPreferred: []string{"english dub", "english dubbed", "english-dub", `/\[ENG\]/`, "german dub", "german dubbed", "german-dub", `/\[GER\]/`, "french dub", "french dubbed", "french-dub", `/\[FRE\]/`, "spanish dub", "spanish dubbed", "spanish-dub", `/\[SPA\]/`, "italian dub", "italian dubbed", "italian-dub", `/\[ITA\]/`, "portuguese dub", "portuguese dubbed", "portuguese-dub", `/\[POR\]/`},
	},
}

// GetAnimeLanguageTerms returns preferred, non-preferred, and filter-out terms
// for the given language code. Unknown codes return empty slices.
func GetAnimeLanguageTerms(langCode string) (preferred []string, nonPreferred []string, filterOut []string) {
	entry, ok := animeLanguageTerms[langCode]
	if !ok {
		return nil, nil, nil
	}
	return entry.Preferred, entry.NonPreferred, entry.FilterOut
}
