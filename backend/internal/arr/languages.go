package arr

import (
	"strings"
)

// Language is an app-independent language identity. Sonarr and Radarr assign
// different numeric IDs to the same language (e.g. Arabic is 26 in Sonarr and
// 31 in Radarr), so the engine works with names and converts IDs at the edges.
type Language string

const (
	LanguageUnknown  Language = "Unknown"
	LanguageOriginal Language = "Original"
	LanguageAny      Language = "Any"
	LanguageEnglish  Language = "English"
	LanguageGerman   Language = "German"
)

type languageDef struct {
	name     Language
	iso1     string   // ISO 639-1
	iso2     []string // ISO 639-2 (B and T forms) and common aliases
	sonarrID *int
	radarrID *int
}

func id(v int) *int { return &v }

// languageTable lists every language known to Sonarr v4 and Radarr v5+ with
// its per-app ID. Entries without an ID for an app do not exist in that app.
var languageTable = []languageDef{
	{name: LanguageUnknown, sonarrID: id(0), radarrID: id(0)},
	{name: "English", iso1: "en", iso2: []string{"eng"}, sonarrID: id(1), radarrID: id(1)},
	{name: "French", iso1: "fr", iso2: []string{"fra", "fre"}, sonarrID: id(2), radarrID: id(2)},
	{name: "Spanish", iso1: "es", iso2: []string{"spa"}, sonarrID: id(3), radarrID: id(3)},
	{name: "German", iso1: "de", iso2: []string{"deu", "ger"}, sonarrID: id(4), radarrID: id(4)},
	{name: "Italian", iso1: "it", iso2: []string{"ita"}, sonarrID: id(5), radarrID: id(5)},
	{name: "Danish", iso1: "da", iso2: []string{"dan"}, sonarrID: id(6), radarrID: id(6)},
	{name: "Dutch", iso1: "nl", iso2: []string{"nld", "dut"}, sonarrID: id(7), radarrID: id(7)},
	{name: "Japanese", iso1: "ja", iso2: []string{"jpn"}, sonarrID: id(8), radarrID: id(8)},
	{name: "Icelandic", iso1: "is", iso2: []string{"isl", "ice"}, sonarrID: id(9), radarrID: id(9)},
	{name: "Chinese", iso1: "zh", iso2: []string{"zho", "chi"}, sonarrID: id(10), radarrID: id(10)},
	{name: "Russian", iso1: "ru", iso2: []string{"rus"}, sonarrID: id(11), radarrID: id(11)},
	{name: "Polish", iso1: "pl", iso2: []string{"pol"}, sonarrID: id(12), radarrID: id(12)},
	{name: "Vietnamese", iso1: "vi", iso2: []string{"vie"}, sonarrID: id(13), radarrID: id(13)},
	{name: "Swedish", iso1: "sv", iso2: []string{"swe"}, sonarrID: id(14), radarrID: id(14)},
	{name: "Norwegian", iso1: "no", iso2: []string{"nor", "nob", "nno"}, sonarrID: id(15), radarrID: id(15)},
	{name: "Finnish", iso1: "fi", iso2: []string{"fin"}, sonarrID: id(16), radarrID: id(16)},
	{name: "Turkish", iso1: "tr", iso2: []string{"tur"}, sonarrID: id(17), radarrID: id(17)},
	{name: "Portuguese", iso1: "pt", iso2: []string{"por"}, sonarrID: id(18), radarrID: id(18)},
	{name: "Flemish", iso2: []string{"vls"}, sonarrID: id(19), radarrID: id(19)},
	{name: "Greek", iso1: "el", iso2: []string{"ell", "gre"}, sonarrID: id(20), radarrID: id(20)},
	{name: "Korean", iso1: "ko", iso2: []string{"kor"}, sonarrID: id(21), radarrID: id(21)},
	{name: "Hungarian", iso1: "hu", iso2: []string{"hun"}, sonarrID: id(22), radarrID: id(22)},
	{name: "Hebrew", iso1: "he", iso2: []string{"heb"}, sonarrID: id(23), radarrID: id(23)},
	{name: "Lithuanian", iso1: "lt", iso2: []string{"lit"}, sonarrID: id(24), radarrID: id(24)},
	{name: "Czech", iso1: "cs", iso2: []string{"ces", "cze"}, sonarrID: id(25), radarrID: id(25)},
	{name: "Arabic", iso1: "ar", iso2: []string{"ara"}, sonarrID: id(26), radarrID: id(31)},
	{name: "Hindi", iso1: "hi", iso2: []string{"hin"}, sonarrID: id(27), radarrID: id(26)},
	{name: "Bulgarian", iso1: "bg", iso2: []string{"bul"}, sonarrID: id(28), radarrID: id(29)},
	{name: "Malayalam", iso1: "ml", iso2: []string{"mal"}, sonarrID: id(29), radarrID: id(48)},
	{name: "Ukrainian", iso1: "uk", iso2: []string{"ukr"}, sonarrID: id(30), radarrID: id(32)},
	{name: "Slovak", iso1: "sk", iso2: []string{"slk", "slo"}, sonarrID: id(31), radarrID: id(35)},
	{name: "Thai", iso1: "th", iso2: []string{"tha"}, sonarrID: id(32), radarrID: id(28)},
	{name: "Portuguese (Brazil)", iso1: "pt-br", iso2: []string{"pob"}, sonarrID: id(33), radarrID: id(30)},
	{name: "Spanish (Latino)", iso1: "es-419", iso2: []string{"spl", "la"}, sonarrID: id(34), radarrID: id(37)},
	{name: "Romanian", iso1: "ro", iso2: []string{"ron", "rum"}, sonarrID: id(35), radarrID: id(27)},
	{name: "Latvian", iso1: "lv", iso2: []string{"lav"}, sonarrID: id(36), radarrID: id(36)},
	{name: "Persian", iso1: "fa", iso2: []string{"fas", "per"}, sonarrID: id(37), radarrID: id(33)},
	{name: "Catalan", iso1: "ca", iso2: []string{"cat"}, sonarrID: id(38), radarrID: id(38)},
	{name: "Croatian", iso1: "hr", iso2: []string{"hrv"}, sonarrID: id(39), radarrID: id(39)},
	{name: "Serbian", iso1: "sr", iso2: []string{"srp"}, sonarrID: id(40), radarrID: id(40)},
	{name: "Bosnian", iso1: "bs", iso2: []string{"bos"}, sonarrID: id(41), radarrID: id(41)},
	{name: "Estonian", iso1: "et", iso2: []string{"est"}, sonarrID: id(42), radarrID: id(42)},
	{name: "Tamil", iso1: "ta", iso2: []string{"tam"}, sonarrID: id(43), radarrID: id(43)},
	{name: "Indonesian", iso1: "id", iso2: []string{"ind"}, sonarrID: id(44), radarrID: id(44)},
	{name: "Macedonian", iso1: "mk", iso2: []string{"mkd", "mac"}, sonarrID: id(45), radarrID: id(46)},
	{name: "Slovenian", iso1: "sl", iso2: []string{"slv"}, sonarrID: id(46), radarrID: id(47)},
	{name: "Azerbaijani", iso1: "az", iso2: []string{"aze"}, sonarrID: id(47)},
	{name: "Uzbek", iso1: "uz", iso2: []string{"uzb"}, sonarrID: id(48)},
	{name: "Malay", iso1: "ms", iso2: []string{"msa", "may"}, sonarrID: id(49)},
	{name: "Urdu", iso1: "ur", iso2: []string{"urd"}, sonarrID: id(50), radarrID: id(54)},
	{name: "Romansh", iso1: "rm", iso2: []string{"roh"}, sonarrID: id(51), radarrID: id(55)},
	{name: "Georgian", iso1: "ka", iso2: []string{"kat", "geo"}, sonarrID: id(52), radarrID: id(57)},
	{name: "Irish", iso1: "ga", iso2: []string{"gle"}, sonarrID: id(53)},
	{name: "Bengali", iso1: "bn", iso2: []string{"ben"}, radarrID: id(34)},
	{name: "Telugu", iso1: "te", iso2: []string{"tel"}, radarrID: id(45)},
	{name: "Kannada", iso1: "kn", iso2: []string{"kan"}, radarrID: id(49)},
	{name: "Albanian", iso1: "sq", iso2: []string{"sqi", "alb"}, radarrID: id(50)},
	{name: "Afrikaans", iso1: "af", iso2: []string{"afr"}, radarrID: id(51)},
	{name: "Marathi", iso1: "mr", iso2: []string{"mar"}, radarrID: id(52)},
	{name: "Tagalog", iso1: "tl", iso2: []string{"tgl", "fil"}, radarrID: id(53)},
	{name: "Mongolian", iso1: "mn", iso2: []string{"mon"}, radarrID: id(56)},
	{name: LanguageAny, radarrID: id(-1)},
	{name: LanguageOriginal, sonarrID: id(-2), radarrID: id(-2)},
}

var (
	languageByName = map[string]*languageDef{}
	languageByCode = map[string]*languageDef{}
	languageByID   = map[App]map[int]*languageDef{AppSonarr: {}, AppRadarr: {}}
)

func init() {
	for i := range languageTable {
		def := &languageTable[i]
		languageByName[strings.ToLower(string(def.name))] = def
		if def.iso1 != "" {
			languageByCode[def.iso1] = def
		}
		for _, code := range def.iso2 {
			languageByCode[code] = def
		}
		if def.sonarrID != nil {
			languageByID[AppSonarr][*def.sonarrID] = def
		}
		if def.radarrID != nil {
			languageByID[AppRadarr][*def.radarrID] = def
		}
	}
	// Common name aliases seen in release names and scraper metadata.
	for alias, name := range map[string]string{
		"brazilian": "Portuguese (Brazil)", "latino": "Spanish (Latino)", "castellano": "Spanish",
		"mandarin": "Chinese", "cantonese": "Chinese", "farsi": "Persian", "deutsch": "German",
		"francais": "French", "español": "Spanish", "espanol": "Spanish", "polski": "Polish",
		"nederlands": "Dutch", "italiano": "Italian", "portugues": "Portuguese", "filipino": "Tagalog",
	} {
		languageByName[alias] = languageByName[strings.ToLower(name)]
	}
}

// LanguageFromID resolves an app-specific language ID.
func LanguageFromID(app App, value int) (Language, bool) {
	def, ok := languageByID[app][value]
	if !ok {
		return "", false
	}
	return def.name, true
}

// LanguageID returns the app-specific ID for a language.
func LanguageID(app App, lang Language) (int, bool) {
	def, ok := languageByName[strings.ToLower(string(lang))]
	if !ok {
		return 0, false
	}
	switch app {
	case AppSonarr:
		if def.sonarrID != nil {
			return *def.sonarrID, true
		}
	case AppRadarr:
		if def.radarrID != nil {
			return *def.radarrID, true
		}
	}
	return 0, false
}

// LanguageFromCode resolves an ISO 639-1/639-2 code, a regional code such as
// "pt-BR", or an English language name. It returns LanguageUnknown when the
// value is not recognized.
func LanguageFromCode(value string) Language {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return LanguageUnknown
	}
	switch v {
	case "pt-br", "pt_br", "ptbr":
		return "Portuguese (Brazil)"
	case "es-419", "es-mx", "es_mx", "es-la":
		return "Spanish (Latino)"
	}
	if def, ok := languageByCode[v]; ok {
		return def.name
	}
	if def, ok := languageByName[v]; ok {
		return def.name
	}
	if i := strings.IndexAny(v, "-_"); i > 0 {
		if def, ok := languageByCode[v[:i]]; ok {
			return def.name
		}
	}
	return LanguageUnknown
}

// ISOCode returns the ISO 639-2/T code mediastorm uses elsewhere for a
// language ("" when there is none).
func (l Language) ISOCode() string {
	def, ok := languageByName[strings.ToLower(string(l))]
	if !ok || len(def.iso2) == 0 {
		return ""
	}
	// The first entry is the ISO 639-2/T form (e.g. "deu" rather than "ger").
	return def.iso2[0]
}

// LanguageOptions lists the languages that exist for an app, ordered by ID,
// for building select inputs.
func LanguageOptions(app App) []LanguageOption {
	out := make([]LanguageOption, 0, len(languageTable))
	for _, def := range languageTable {
		if v, ok := LanguageID(app, def.name); ok {
			out = append(out, LanguageOption{ID: v, Name: def.name})
		}
	}
	return out
}

// LanguageOption is an (ID, name) pair for one app.
type LanguageOption struct {
	ID   int      `json:"id"`
	Name Language `json:"name"`
}

func containsLanguage(list []Language, lang Language) bool {
	for _, l := range list {
		if l == lang {
			return true
		}
	}
	return false
}

// FinalizeLanguages applies the *arr aggregation rules that turn parsed
// languages into the release's effective languages, given the title's
// original language (LanguageUnknown when metadata does not provide one):
//
//   - indexer/scraper supplied languages replace parsed ones when present
//   - no parsed language falls back to the original language
//   - "Original" is replaced by the original language
//
// This mirrors what Sonarr/Radarr do for a release that maps to a library item.
func FinalizeLanguages(parsed, provided []Language, original Language) []Language {
	langs := parsed
	if len(provided) > 0 {
		langs = provided
	}
	out := make([]Language, 0, len(langs)+1)
	for _, l := range langs {
		if !containsLanguage(out, l) {
			out = append(out, l)
		}
	}
	if original == "" || original == LanguageOriginal || original == LanguageAny {
		original = LanguageUnknown
	}
	if original == LanguageUnknown {
		if len(out) == 0 {
			out = append(out, LanguageUnknown)
		}
		return out
	}
	if len(out) == 0 || (len(out) == 1 && out[0] == LanguageUnknown) {
		return []Language{original}
	}
	if containsLanguage(out, LanguageOriginal) {
		filtered := out[:0]
		for _, l := range out {
			if l != LanguageOriginal {
				filtered = append(filtered, l)
			}
		}
		out = filtered
		if !containsLanguage(out, original) {
			out = append(out, original)
		} else {
			out = append(out, LanguageUnknown)
		}
	}
	return out
}
