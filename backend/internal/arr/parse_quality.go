package arr

import (
	"regexp"
	"strconv"
	"strings"
)

// sourceClass is the kind of source tag found in a title.
type sourceClass int

const (
	srcNone sourceClass = iota
	srcBluray
	srcBDRip
	srcWebDL
	srcWebRip
	srcHDTV
	srcDVD
	srcTV // pdtv / sdtv / dsr / tvrip style captures
	srcCam
	srcTelesync
	srcTelecine
	srcWorkprint
	srcScreener
	srcRegional
)

type sourceToken struct {
	class sourceClass
	re    *regexp.Regexp
}

func tok(class sourceClass, pattern string) sourceToken {
	return sourceToken{class: class, re: regexp.MustCompile(`(?i)\b(?:` + pattern + `)\b`)}
}

// tokRaw is tok without implicit word boundaries.
func tokRaw(class sourceClass, pattern string) sourceToken {
	return sourceToken{class: class, re: regexp.MustCompile(`(?i)` + pattern)}
}

// Source tags in the order they are tried at the same position. The tag that
// appears last in the title decides the source, which matches how *arr
// treats titles that mention several sources (e.g. "WEB-DL.BluRay").
var sourceTokens = []sourceToken{
	tok(srcBDRip, `bd[-. ]?rip|br[-. ]?rip|bdlight|bd[-. ]?mux|br[-. ]?mux`),
	tokRaw(srcBluray, `(?:^|[ (\[])bd(?:[ )\]]|$)`),
	tok(srcBluray, `blu[-. ]?ray(?:[-. ]?remux)?|hd[-. ]?dvd|bd(?:25|50|66|100)|uhd[-. ]?bd|bd[-. ]?iso|bdmv|bd[-. ]?remux|uhd[-. ]?remux`),
	tok(srcWebRip, `web[-. ]?rip|webrip|webmux|web[-. ]?hd[-. ]?rip|webm?rip`),
	tok(srcWebDL, `web[-. ]?dl(?:mux|[-. ]?rip)?|webdl|web[-. ]?hd|web[-. ](?:ddp?5[. ]1|ddp\d|hevc|avc|h[-. ]?26[456]|x26[456])|(?:amazon|netflix|maxdome|itunes|hbomax|disney|disneyplus|appletv|paramount|sky|joyn|rtlplus|magenta|wow)(?:u?hd|sd)|itunes|vod`),
	tok(srcHDTV, `hdtv(?:rip)?|uhd[-. ]?tv`),
	tok(srcDVD, `dvd(?:rip|mux|r|5|9)?|dvd[-. ]r(?:ip)?|ntsc|pal|xvidvd`),
	tok(srcTV, `pdtv|sdtv|dsr(?:ip)?|dthrip|dvbrip|tvrip|satrip|ws[-. ]?pdtv`),
}

// Sonarr: a bare "WEB" only means WEB-DL in a few positions: right after a
// resolution or before one, after a streaming service tag, at the end, or
// "/ WEB /".
var sonarrSourceTokens = append(append([]sourceToken(nil), sourceTokens...),
	tokRaw(srcWebDL, `(?i)(?:(?:720|1080|2160)p|\bamzn|\bnf|\bdp)[-. ]web(?:[-. ]|$)|[ .]web$| / web / |[-. ]web[-. ](?:720|1080|2160)p\b`),
)

// Radarr: "[Web]", "WEB-HDRip", "MBluRay" and glued "BD1080p" style tags. A
// bare "WEB" is handled in parseRadarrQuality because it depends on the
// resolution.
var radarrMainTokens = append(append([]sourceToken(nil), sourceTokens...),
	tokRaw(srcWebDL, `(?i)\[web\]|\bweb[-. ]hdrip\b|(?:(?:1080|2160)p|\bamzn|\bnf|\bdp|\bhybrid)[-. ]web\b|\bweb[-. ](?:720|1080|2160)p\b|[ .]web$`),
	tok(srcBluray, `m[-. ]?blu[-. ]?ray|bd(?:480|576|720|1080|2160)p?|bluray(?:480|576|720|1080|2160)p|uhd2bd|uhdbd`),
	tokRaw(srcBluray, `(?i)(?:^|[ (\[.])bd(?:[ .)\]]|$)`),
	tok(srcBDRip, `uhd[-. ]?bd[-. ]?rip|hd[-. ]?dvd[-. ]?rip`),
)

// Radarr-only pre-release sources. They take part in the same "last tag
// wins" contest as radarrMainTokens (Radarr has a single SourceRegex), but
// see radarrSkipPreRelease for where they are ignored.
var radarrPreReleaseTokens = []sourceToken{
	tok(srcCam, `cam(?:rip)?|hd[-. ]?cam(?:rip)?|hqcam|newcam`),
	tok(srcTelesync, `ts|telesync|telesynch|hd[-. ]?ts|pdvd|ts[-. ]?rip|predvd(?:rip)?`),
	tok(srcTelecine, `tc|telecine|hd[-. ]?tc`),
	tok(srcWorkprint, `workprint|wp`),
	tok(srcScreener, `dvd[-. ]?scr|scr|screener|bd[-. ]?scr|web[-. ]?scr`),
	tok(srcRegional, `r[5-6]|regional`),
}

// lastSource returns the class of the tag that starts last in the title.
// A tag that lies inside a longer tag (e.g. "DVD" in "HD-DVD") is ignored.
func lastSource(name string, tokens []sourceToken) sourceClass {
	return lastSourceExcept(name, tokens, nil)
}

// lastSourceExcept is lastSource ignoring every tag for which skip reports
// true (before the containment check).
func lastSourceExcept(name string, tokens []sourceToken, skip func(class sourceClass, start, end int) bool) sourceClass {
	type hit struct {
		class      sourceClass
		start, end int
	}
	var hits []hit
	for _, t := range tokens {
		for _, loc := range t.re.FindAllStringIndex(name, -1) {
			if skip != nil && skip(t.class, loc[0], loc[1]) {
				continue
			}
			hits = append(hits, hit{t.class, loc[0], loc[1]})
		}
	}
	best, bestPos := srcNone, -1
	for i, h := range hits {
		inside := false
		for j, o := range hits {
			if i != j && o.start <= h.start && o.end >= h.end && (o.end-o.start) > (h.end-h.start) {
				inside = true
				break
			}
		}
		if !inside && h.start > bestPos {
			best, bestPos = h.class, h.start
		}
	}
	return best
}

var (
	reRawHD     = regexp.MustCompile(`(?i)\braw[-. ]?hd\b`)
	reMPEG2     = regexp.MustCompile(`\bMPEG[-. ]?2\b`)
	reRemux     = regexp.MustCompile(`(?i)(?:[ .]|\d{3,4}p-|hybrid-)(?:(?:bd|uhd)[-. ]?)?remux\b|\b(?:(?:bd|uhd)[-. ]?)?remux[ .]\d{3,4}p`)
	reXvidDivx  = regexp.MustCompile(`(?i)\b(?:xvid|divx)\b`)
	reX264      = regexp.MustCompile(`(?i)\bx264\b`)
	reAnimeBD   = regexp.MustCompile(`(?i)bd(?:720|1080|2160)|(?:^|[-. (\[])bd(?:[-. )\]]|$)`)
	reAnimeWeb  = regexp.MustCompile(`(?i)\[web\]|[\[(]web[ .]`)
	reHRWS      = regexp.MustCompile(`(?i)hr[-. ]ws`)
	reHDTVSpace = regexp.MustCompile(`(?i)hd[-. ]tv`)
	reSDTVSpace = regexp.MustCompile(`(?i)sd[-. ]tv`)

	re360  = regexp.MustCompile(`(?i)\b360p\b`)
	re480  = regexp.MustCompile(`(?i)\b(?:480[pi]|640x480|848x480)\b`)
	re540  = regexp.MustCompile(`(?i)\b540p\b`)
	re576  = regexp.MustCompile(`(?i)\b576p\b`)
	re720  = regexp.MustCompile(`(?i)\b(?:720p|1280x720|960p)\b`)
	re1080 = regexp.MustCompile(`(?i)\b(?:1080[pi]|1920x1080|1440p|fhd|4kto1080p)\b`)
	re2160 = regexp.MustCompile(`(?i)\b(?:2160p|3840x2160|4k[-. ](?:uhd|hevc|bd|h26[45])|(?:uhd|hevc|bd|h26[45])[-. ]4k)\b`)
	reUHD  = regexp.MustCompile(`(?i)\buhd\b|\[4k\]`)

	reProper  = regexp.MustCompile(`(?i)\bproper\b`)
	reRepack  = regexp.MustCompile(`(?i)\b(?:repack|rerip)\d?\b`)
	reVersion = regexp.MustCompile(`(?i)\d[-. ]?v(\d)[-. ]|\[v(\d)\]|repack(\d)|rerip(\d)|(?:480|576|720|1080|2160)p[. ]v(\d)`)
	reReal    = regexp.MustCompile(`\bREAL\b`)
)

// parseResolution returns the first resolution tag in the title.
func parseResolution(name string) int {
	best, bestPos := 0, -1
	for _, r := range []struct {
		re  *regexp.Regexp
		res int
	}{{re360, 360}, {re480, 480}, {re540, 540}, {re576, 576}, {re720, 720}, {re1080, 1080}, {re2160, 2160}} {
		if loc := r.re.FindStringIndex(name); loc != nil && (bestPos < 0 || loc[0] < bestPos) {
			best, bestPos = r.res, loc[0]
		}
	}
	if best == 0 && reUHD.MatchString(name) {
		return 2160
	}
	return best
}

func parseRevision(name string) Revision {
	rev := Revision{Version: 1}
	if reProper.MatchString(name) {
		rev.Version = 2
	}
	if reRepack.MatchString(name) {
		rev.Version = 2
		rev.IsRepack = true
	}
	if m := reVersion.FindStringSubmatch(name); m != nil {
		for i, g := range m[1:] {
			if g == "" {
				continue
			}
			if n, err := strconv.Atoi(g); err == nil {
				rev.Version = n
				// "REPACK2"/"RERIP2" is the second repack: revision 3.
				if i == 2 || i == 3 {
					rev.Version = n + 1
				}
			}
			break
		}
	}
	rev.Real = len(reReal.FindAllStringIndex(name, -1))
	return rev
}

func sonarrQuality(name string) Quality {
	q, _ := QualityByName(AppSonarr, name)
	return q
}

func radarrQuality(name string) Quality {
	q, _ := QualityByName(AppRadarr, name)
	return q
}

func parseQuality(app App, title string) (Quality, Revision) {
	name := strings.TrimSpace(strings.ReplaceAll(title, "_", " "))
	rev := parseRevision(name)
	if app == AppRadarr {
		return parseRadarrQuality(title, name), rev
	}
	return parseSonarrQuality(title, name), rev
}

func pickRes(res int, byRes map[int]string, fallback string) string {
	if n, ok := byRes[res]; ok {
		return n
	}
	return fallback
}

func parseSonarrQuality(raw, name string) Quality {
	if reRawHD.MatchString(name) {
		return sonarrQuality("Raw-HD")
	}
	source := lastSource(name, sonarrSourceTokens)
	res := parseResolution(name)
	remux := reRemux.MatchString(name)

	switch source {
	case srcBluray:
		if reXvidDivx.MatchString(name) {
			return sonarrQuality("Bluray-480p")
		}
		switch res {
		case 2160:
			if remux {
				return sonarrQuality("Bluray-2160p Remux")
			}
			return sonarrQuality("Bluray-2160p")
		case 1080:
			if remux {
				return sonarrQuality("Bluray-1080p Remux")
			}
			return sonarrQuality("Bluray-1080p")
		case 576:
			return sonarrQuality("Bluray-576p")
		case 360, 480, 540:
			return sonarrQuality("Bluray-480p")
		}
		if remux && res != 720 {
			return sonarrQuality("Bluray-1080p Remux")
		}
		return sonarrQuality("Bluray-720p")
	case srcWebDL:
		if res == 0 && strings.Contains(raw, "[WEBDL]") {
			return sonarrQuality("WEBDL-720p")
		}
		return sonarrQuality(pickRes(res, map[int]string{2160: "WEBDL-2160p", 1080: "WEBDL-1080p", 720: "WEBDL-720p"}, "WEBDL-480p"))
	case srcWebRip:
		return sonarrQuality(pickRes(res, map[int]string{2160: "WEBRip-2160p", 1080: "WEBRip-1080p", 720: "WEBRip-720p"}, "WEBRip-480p"))
	case srcHDTV:
		if reMPEG2.MatchString(name) {
			return sonarrQuality("Raw-HD")
		}
		if res == 0 && strings.Contains(raw, "[HDTV]") {
			return sonarrQuality("HDTV-720p")
		}
		return sonarrQuality(pickRes(res, map[int]string{2160: "HDTV-2160p", 1080: "HDTV-1080p", 720: "HDTV-720p"}, "SDTV"))
	case srcBDRip:
		return sonarrQuality(pickRes(res, map[int]string{720: "Bluray-720p", 1080: "Bluray-1080p", 2160: "Bluray-2160p"}, "Bluray-480p"))
	case srcDVD:
		return sonarrQuality("DVD")
	case srcTV:
		lower := strings.ToLower(name)
		switch {
		case res == 1080 || strings.Contains(lower, "1080p"):
			return sonarrQuality("HDTV-1080p")
		case res == 720 || strings.Contains(lower, "720p"):
			return sonarrQuality("HDTV-720p")
		case reHRWS.MatchString(name):
			return sonarrQuality("HDTV-720p")
		}
		return sonarrQuality("SDTV")
	}

	if remux && res != 0 {
		switch res {
		case 480:
			return sonarrQuality("Bluray-480p")
		case 720:
			return sonarrQuality("Bluray-720p")
		case 2160:
			return sonarrQuality("Bluray-2160p Remux")
		case 1080:
			return sonarrQuality("Bluray-1080p Remux")
		}
	}
	lower := strings.ToLower(name)
	if reAnimeBD.MatchString(name) {
		switch {
		case res == 360 || res == 480 || res == 540 || res == 576 || strings.Contains(lower, "480p"):
			return sonarrQuality("DVD")
		case res == 1080 || strings.Contains(lower, "1080p"):
			if remux {
				return sonarrQuality("Bluray-1080p Remux")
			}
			return sonarrQuality("Bluray-1080p")
		case res == 2160 || strings.Contains(lower, "2160p"):
			if remux {
				return sonarrQuality("Bluray-2160p Remux")
			}
			return sonarrQuality("Bluray-2160p")
		case remux && res != 720:
			return sonarrQuality("Bluray-1080p Remux")
		}
		return sonarrQuality("Bluray-720p")
	}
	if reAnimeWeb.MatchString(name) {
		switch {
		case res == 360 || res == 480 || res == 540 || res == 576 || strings.Contains(lower, "480p"):
			return sonarrQuality("WEBDL-480p")
		case res == 1080 || strings.Contains(lower, "1080p"):
			return sonarrQuality("WEBDL-1080p")
		case res == 2160 || strings.Contains(lower, "2160p"):
			return sonarrQuality("WEBDL-2160p")
		}
		return sonarrQuality("WEBDL-720p")
	}
	if res != 0 {
		extSource := Source(-1)
		if remux {
			extSource = SonarrSourceBlurayRaw
		} else if q, ok := qualityForExtension(AppSonarr, raw); ok {
			extSource = q.Source
		}
		target := res
		if res != 2160 && res != 1080 && res != 720 {
			target = 480
		}
		if extSource >= 0 && extSource != SonarrSourceUnknown {
			if q, ok := findBySourceAndResolution(AppSonarr, extSource, target); ok {
				return q
			}
			return UnknownQuality(AppSonarr)
		}
		return sonarrQuality(map[int]string{2160: "HDTV-2160p", 1080: "HDTV-1080p", 720: "HDTV-720p", 480: "SDTV"}[target])
	}
	if reX264.MatchString(name) {
		return sonarrQuality("SDTV")
	}
	switch {
	case strings.Contains(name, "848x480"):
		if strings.Contains(name, "dvd") {
			return sonarrQuality("DVD")
		}
		if strings.Contains(lower, "bluray") {
			return sonarrQuality("Bluray-480p")
		}
		return sonarrQuality("SDTV")
	case strings.Contains(lower, "1280x720"):
		if strings.Contains(lower, "bluray") {
			return sonarrQuality("Bluray-720p")
		}
		return sonarrQuality("HDTV-720p")
	case strings.Contains(lower, "1920x1080"):
		if strings.Contains(lower, "bluray") {
			return sonarrQuality("Bluray-1080p")
		}
		return sonarrQuality("HDTV-1080p")
	case strings.Contains(lower, "bluray720p"):
		return sonarrQuality("Bluray-720p")
	case strings.Contains(lower, "bluray1080p"):
		return sonarrQuality("Bluray-1080p")
	case strings.Contains(lower, "bluray2160p"):
		return sonarrQuality("Bluray-2160p")
	case reHDTVSpace.MatchString(name):
		return sonarrQuality("HDTV-720p")
	case reSDTVSpace.MatchString(name):
		return sonarrQuality("SDTV")
	}
	if q, ok := qualityForExtension(AppSonarr, raw); ok {
		return q
	}
	return UnknownQuality(AppSonarr)
}

// qualityForExtension infers a quality from a media file extension, as *arr
// does for file names without quality tags.
func qualityForExtension(app App, raw string) (Quality, bool) {
	m := extensionRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return Quality{}, false
	}
	name := ""
	ext := strings.ToLower(m[1])
	if app == AppRadarr {
		switch ext {
		case "mkv":
			name = "WEBDL-720p"
		case "m2ts":
			name = "Bluray-720p"
		case "iso", "img":
			name = "DVD"
		case "webm", "avc", "bin", "nzb", "par2":
			return Quality{}, false
		default:
			name = "SDTV"
		}
		return mustQuality(app, name), true
	}
	switch ext {
	case "avi", "divx", "wmv", "mp4", "m4v", "flv", "xvid", "mov", "mpg", "mpeg", "3gp", "asf", "rm", "rmvb", "ogm", "vob", "dvr-ms", "mk3d":
		name = "SDTV"
	case "mkv", "ts", "wtv", "m2v", "strm":
		name = "HDTV-720p"
	case "m2ts", "iso":
		name = "Bluray-720p"
	default:
		return Quality{}, false
	}
	return mustQuality(app, name), true
}

func mustQuality(app App, name string) Quality {
	q, _ := QualityByName(app, name)
	return q
}

func findBySourceAndResolution(app App, source Source, res int) (Quality, bool) {
	for _, def := range Qualities(app) {
		if def.Quality.Source == source && def.Quality.Resolution == res && def.Quality.Modifier == ModifierNone {
			return def.Quality, true
		}
	}
	return Quality{}, false
}
