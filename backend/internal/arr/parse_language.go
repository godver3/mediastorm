package arr

import (
	"regexp"
	"strings"
)

type languageRule struct {
	lang  Language
	re    *regexp.Regexp
	match func(string) bool // used instead of re when set
}

func (r languageRule) matches(s string) bool {
	if r.match != nil {
		return r.match(s)
	}
	return r.re.MatchString(s)
}

// lcs builds a rule for an upper-case two-letter code that *arr matches
// case-sensitively and not next to "SUB" (CaseSensitiveLanguageRegex), so
// "Show.S01E04.PL.SUB" or "Cidade.de.Deus" carry no language.
func lcs(lang Language, code string) languageRule {
	return languageRule{lang: lang, match: func(s string) bool { return hasCaseSensitiveCode(s, code) }}
}

var reCaseSensitiveCode = regexp.MustCompile(`\b(EN|LT|CZ|PL|BG|SK|DE|ES)\b`)

func hasCaseSensitiveCode(s, code string) bool {
	isSep := func(c byte) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
	}
	for _, m := range reCaseSensitiveCode.FindAllStringSubmatchIndex(s, -1) {
		start, end := m[0], m[1]
		if s[m[2]:m[3]] != code {
			continue
		}
		// (?<!SUB[\W_]) and (?![\W_]SUB)
		if start >= 4 && isSep(s[start-1]) && strings.EqualFold(s[start-4:start-1], "sub") {
			continue
		}
		if end+4 <= len(s) && isSep(s[end]) && strings.EqualFold(s[end+1:end+4], "sub") {
			continue
		}
		// Radarr: (?<!DTS[._ -])ES
		if code == "ES" && start >= 4 && strings.ContainsRune("._ -", rune(s[start-1])) && s[start-4:start-1] == "DTS" {
			continue
		}
		return true
	}
	return false
}

// lw builds a case-insensitive whole-word rule.
func lw(lang Language, pattern string) languageRule {
	return languageRule{lang: lang, re: regexp.MustCompile(`(?i)\b(?:` + pattern + `)\b`)}
}

// lraw builds a rule from a raw pattern (case-sensitive unless it opts in).
func lraw(lang Language, pattern string) languageRule {
	return languageRule{lang: lang, re: regexp.MustCompile(pattern)}
}

// languageRules detect audio languages in Sonarr release tokens (everything
// from SxxEyy on), following Sonarr's LanguageParser: short codes are only
// those Sonarr knows ("Ger", "Fre", "Por", "NL" are not languages there), and
// LT/CZ/PL/BG/SK are case-sensitive and ignored next to "SUB". Like Sonarr,
// "ENG", "ING", "ESP", "SPA", "LAT" and "TUR" also match episode-title words.
var languageRules = []languageRule{
	lw(LanguageEnglish, `english|eng|ing|ingles|inglés`),
	lw("French", `french|truefrench|fran[cç]ais|vf|vf2|vff|vfi|vfq|fr`),
	lw("Spanish", `spanish|espa[nñ]ol|castellano|esp|spa`),
	lw(LanguageGerman, `german|deutsch|swissgerman|videomann|ger[-. ]?dub`),
	lw("Italian", `italian|italiano|ita`),
	lw("Danish", `danish`),
	lw("Dutch", `dutch`),
	lw("Flemish", `flemish`),
	lw("Japanese", `japanese`),
	lw("Icelandic", `icelandic`),
	lw("Chinese", `chinese|mandarin|cantonese`),
	lraw("Chinese", `(?i)\[(?:chs|cht|gb|big5)\]|[简繁]|中日`),
	lw("Russian", `russian|rus|ru`),
	lw("Polish", `polish|pl\W?dub|dub\W?pl|lek\W?pl|pl\W?lek`),
	lcs("Polish", "PL"),
	lw("Vietnamese", `vietnamese`),
	lw("Swedish", `swedish`),
	lw("Norwegian", `norwegian|norsk`),
	lw("Finnish", `finnish`),
	lw("Turkish", `turkish|tur`),
	lw("Portuguese", `portuguese`),
	lw("Greek", `greek`),
	lw("Korean", `korean`),
	lw("Hungarian", `hungarian|hundub|hun`),
	lw("Hebrew", `hebrew|hebdub`),
	lw("Lithuanian", `lithuanian`),
	lcs("Lithuanian", "LT"),
	lw("Czech", `czech`),
	lcs("Czech", "CZ"),
	lw("Arabic", `arabic`),
	lw("Hindi", `hindi`),
	lw("Bulgarian", `bulgarian|bg[-. ]?audio`),
	lcs("Bulgarian", "BG"),
	lw("Malayalam", `malayalam`),
	lraw("Ukrainian", `(?i)(?:\b|\d|\dx)(?:ukrainian|ukr)\b`),
	lw("Slovak", `slovak`),
	lcs("Slovak", "SK"),
	lw("Thai", `thai`),
	lw("Portuguese (Brazil)", `brazilian|dublado|pt[-. ]?br`),
	lw("Spanish (Latino)", `latino|latam`),
	lw("Romanian", `romanian|rodubbed`),
	lw("Latvian", `latvian|lav|lv|lat`),
	lw("Persian", `persian|farsi`),
	// Sonarr: a bare "cat" only before DD or subs ("Cat.DD5.1", "cat.subs").
	lraw("Catalan", `(?i)[-,. ]cat[. ](?:dd|subs)|\bcatal(?:an|án)\b`),
	lw("Croatian", `croatian`),
	lw("Serbian", `serbian`),
	lw("Bosnian", `bosnian`),
	lw("Estonian", `estonian`),
	lw("Tamil", `tamil`),
	lw("Indonesian", `indonesian`),
	lw("Macedonian", `macedonian`),
	lw("Slovenian", `slovenian`),
	lw("Telugu", `telugu`),
	lw("Bengali", `bengali`),
}

var (
	// "DL" (dual language) marks German releases that also carry the
	// original audio. The "DL" of "WEB-DL" does not count.
	reDualLanguageDL = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])dl(?:$|[^a-z0-9])`)
	reWebDLSuffix    = regexp.MustCompile(`(?i)web[-. ]?dl(?:$|[^a-z0-9])`)
	reMultiLangML    = regexp.MustCompile(`(?i)\bml\b`)
	reOriginalTag    = regexp.MustCompile(`(?i)\b(?:orig|original)\b`)
)

func hasDualLanguageTag(tokens string) bool {
	stripped := reWebDLSuffix.ReplaceAllString(tokens, " ")
	return reDualLanguageDL.MatchString(stripped)
}

// radarrLanguageRules follow Radarr's LanguageParser: full names anywhere,
// the short codes Radarr's LanguageRegex knows (ENG, ITA, GER, FRE, FRA, FR,
// VO, VF*, RUS, RU, HUN, LAT, LAV, LV, TEL, VIE, JAP, KOR, GEO, KA, KAT, ...)
// case-insensitively, and EN/LT/CZ/PL/BG/SK/DE/ES only case-sensitively and
// not next to "SUB". Radarr knows no "ICE", "FIN", "NOR", "POR", "GRE",
// "DAN", "ROM", "SWE", "CHI", "CZE", "ARA", "HEB" or "SPA" code.
var radarrLanguageRules = []languageRule{
	lw(LanguageEnglish, `english|eng`),
	lcs(LanguageEnglish, "EN"),
	lw("French", `french|truefrench|fran[cç]ais|vo|vf|vff|vfq|vfi|vf2|fre|fra|fr`),
	lw("Spanish", `spanish|espa[nñ]ol|castellano`),
	lcs("Spanish", "ES"),
	lw(LanguageGerman, `german|deutsch|swissgerman|videomann|ger|ger[. ]dub`),
	lcs(LanguageGerman, "DE"),
	lw("Italian", `italian|italiano|ita`),
	lw("Danish", `danish`),
	lw("Dutch", `dutch`),
	lw("Flemish", `flemish`),
	lw("Japanese", `japanese|jap`),
	lw("Icelandic", `icelandic`),
	lw("Chinese", `chinese|mandarin|cantonese`),
	lraw("Chinese", `(?i)\[(?:chs|cht|gb|big5)\]|[简繁]|中日|字幕`),
	lw("Russian", `russian|rus|ru`),
	lw("Polish", `polish|pl\W?dub|dub\W?pl|pl\W?lek|lek\W?pl`),
	lcs("Polish", "PL"),
	lw("Vietnamese", `vietnamese|vie`),
	lw("Swedish", `swedish`),
	lw("Norwegian", `norwegian|norsk`),
	lw("Finnish", `finnish`),
	lw("Turkish", `turkish`),
	lw("Portuguese", `portuguese`),
	lw("Greek", `greek`),
	lw("Korean", `korean|kor`),
	lw("Hungarian", `hungarian|hundub|hun`),
	lw("Hebrew", `hebrew|hebdub|hebdubbed`),
	lw("Lithuanian", `lithuanian`),
	lcs("Lithuanian", "LT"),
	lw("Czech", `czech`),
	lcs("Czech", "CZ"),
	lw("Arabic", `arabic`),
	lw("Hindi", `hindi`),
	lw("Bulgarian", `bulgarian|bg[-. ]?audio`),
	lcs("Bulgarian", "BG"),
	lw("Malayalam", `malayalam`),
	lraw("Ukrainian", `(?i)(?:\b|\d|\dx)(?:ukrainian|ukr)\b`),
	lw("Slovak", `slovak`),
	lcs("Slovak", "SK"),
	lw("Thai", `thai`),
	lw("Portuguese (Brazil)", `brazilian|dublado|pt[-. ]?br`),
	lw("Spanish (Latino)", `latino|latam`),
	lw("Romanian", `romanian|rodubbed`),
	lw("Latvian", `latvian|lav|lv|lat`),
	lw("Persian", `persian|farsi`),
	lraw("Catalan", `(?i)\bcatal(?:an?|án)\b|\bcatalà`),
	lw("Croatian", `croatian`),
	lw("Serbian", `serbian`),
	lw("Bosnian", `bosnian`),
	lw("Estonian", `estonian`),
	lw("Tamil", `tamil`),
	lw("Indonesian", `indonesian`),
	lw("Macedonian", `macedonian`),
	lw("Slovenian", `slovenian`),
	lw("Telugu", `telugu|tel`),
	lw("Bengali", `bengali`),
	lw("Kannada", `kannada`),
	lw("Albanian", `albanian`),
	lw("Afrikaans", `afrikaans`),
	lw("Marathi", `marathi`),
	lw("Tagalog", `tagalog`),
	lw("Urdu", `urdu`),
	lw("Romansh", `romansh|romansch|rumantsch`),
	lw("Mongolian", `mongolian|khalkha`),
	lw("Georgian", `georgian|geo|ka|kat`),
}

var reYTSDomain = regexp.MustCompile(`(?i)yts\.(?:lt|mx|am|ag)`)

// parseLanguagesFor detects languages with the app's rule set. Like Radarr,
// which parses its SimpleReleaseTitle with the movie name replaced by
// "A Movie" and the release group by "RlsGrp", Radarr titles are scanned
// without the movie name (or, if its end is not found, the first word) and
// without the trailing release group.
func parseLanguagesFor(app App, title, tokens, group string) []Language {
	if app != AppRadarr {
		return parseLanguages(tokens)
	}
	scan := reYTSDomain.ReplaceAllString(title, " ")
	if group != "" {
		if i := strings.LastIndex(scan, group); i > 0 {
			scan = scan[:i] + "RlsGrp" + scan[i+len(group):]
		}
	}
	if simple := simpleMovieTitle(scan); simple != scan {
		scan = simple
	} else if i := strings.IndexAny(scan, ". _"); i > 0 {
		scan = scan[i:]
	}
	return detectLanguages(radarrLanguageRules, scan)
}

func parseLanguages(tokens string) []Language {
	return detectLanguages(languageRules, tokens)
}

func detectLanguages(rules []languageRule, tokens string) []Language {
	// Underscores stay word characters, so "_ENG_" is not a language tag.
	normalized := tokens
	var out []Language
	for _, rule := range rules {
		if rule.matches(normalized) && !containsLanguage(out, rule.lang) {
			out = append(out, rule.lang)
		}
	}
	// Like *arr's LanguageParser: only a release parsed as German alone gets
	// DL (German + original) or, failing that, ML (German + English +
	// original).
	if len(out) == 1 && out[0] == LanguageGerman {
		if hasDualLanguageTag(normalized) {
			out = append(out, LanguageOriginal)
		} else if reMultiLangML.MatchString(normalized) {
			out = append(out, LanguageEnglish, LanguageOriginal)
		}
	}
	if reOriginalTag.MatchString(normalized) && !containsLanguage(out, LanguageOriginal) {
		out = append(out, LanguageOriginal)
	}
	if len(out) == 0 {
		out = []Language{LanguageUnknown}
	}
	return out
}

// reMultiTag marks releases that carry several audio languages ("MULTi"),
// like *arr's MultiRegex: separated by "_", "." or a space on both sides,
// so subtitle tags such as "[Multi-Subs]" do not count.
var reMultiTag = regexp.MustCompile(`(?i)[_. ]multi[_. ]`)
