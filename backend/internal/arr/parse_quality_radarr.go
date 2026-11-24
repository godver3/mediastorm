package arr

import (
	"regexp"
	"strings"
)

var (
	// Disc-only video codecs: a BluRay with one of these and no encoder tag is
	// an untouched disc (BR-DISK) or, for German releases, a remux.
	reDiscCodec    = regexp.MustCompile(`(?i)\b(?:avc|vc[-. ]?1|mpeg[-. ]?2)\b`)
	reHEVC         = regexp.MustCompile(`(?i)\bhevc\b`)
	reEncoder      = regexp.MustCompile(`(?i)\b(?:x26[456]|h[-. ]?26[456]|xvid|divx)\b`)
	reBracketRemux = regexp.MustCompile(`(?i)\[(?:bd|uhd)?[-. ]?remux`)
	reAV1          = regexp.MustCompile(`(?i)\bav1\b`)
	reCompleteBD   = regexp.MustCompile(`(?i)\bcomplete[-. ]?(?:uhd[-. ]?)?blu[-. ]?ray\b`)
	reDiscTags     = regexp.MustCompile(`(?i)\b(?:bd[-. ]?25|bd[-. ]?50|bd[-. ]?66|bd[-. ]?100|bdmv|br[-. ]?disk|bluray[-. ]iso|bd[-. ]?iso|untouched)\b`)
	reUHDTag       = regexp.MustCompile(`(?i)\buhd\b`)
	reDVDR         = regexp.MustCompile(`(?i)\b(?:m?dvd[-. ]?r|\d?x?dvd[59]|dvdiso)\b`)
	reRadarrRemux  = regexp.MustCompile(`(?i)remux[-. ]\d{3,4}p`)
)

// radarrSourceTokens are all Radarr source tags, main and pre-release.
var radarrSourceTokens = append(append([]sourceToken(nil), radarrMainTokens...), radarrPreReleaseTokens...)

func isPreRelease(c sourceClass) bool {
	return c >= srcCam && c <= srcRegional
}

// radarrSkipPreRelease ignores the short, ambiguous pre-release tags (CAM,
// TS, TC, WP, SCR, R5) in the movie name and as the trailing release group.
// Radarr itself would read "Movie.2020.1080p.WEB-DL.x264-SCR" as DVDSCR and
// "Cam.2018.1080p" as CAM; both are clearly wrong, so we deviate here.
func radarrSkipPreRelease(name string) func(sourceClass, int, int) bool {
	nameEnd := movieNameEnd(name)
	return func(c sourceClass, start, end int) bool {
		if !isPreRelease(c) {
			return false
		}
		if start < nameEnd {
			return true
		}
		return start > 0 && name[start-1] == '-' && end == len(name)
	}
}

// movieNameEnd estimates where the movie name ends for source detection:
// at the release year, or, without a year, after the first word.
func movieNameEnd(name string) int {
	if loc := reMovieYear.FindStringIndex(name); loc != nil && loc[0] > 0 {
		return loc[0]
	}
	if i := strings.IndexAny(name, ". _-"); i > 0 {
		return i
	}
	return 0
}

func parseRadarrQuality(raw, name string) Quality {
	if reRawHD.MatchString(name) {
		return radarrQuality("Raw-HD")
	}
	// Quality tags are read from the raw title (a trailing ".XviD" counts);
	// only a ".ts" container is dropped so it is not taken for TELESYNC.
	clean := name
	res := parseResolution(clean)
	remux := reRemux.MatchString(clean) || reRadarrRemux.MatchString(clean)
	// Main and pre-release tags compete and the last one wins, as with
	// Radarr's single SourceRegex: "Cam.2018.1080p.WEB-DL" is a WEB-DL.
	scan := stripExtension(clean)
	source := lastSourceExcept(scan, radarrSourceTokens, radarrSkipPreRelease(scan))
	switch source {
	case srcCam:
		return radarrQuality("CAM")
	case srcTelesync:
		return radarrQuality("TELESYNC")
	case srcTelecine:
		return radarrQuality("TELECINE")
	case srcWorkprint:
		return radarrQuality("WORKPRINT")
	case srcScreener:
		return radarrQuality("DVDSCR")
	case srcRegional:
		return radarrQuality("REGIONAL")
	}

	german := containsLanguage(parseLanguages(releaseTokens(clean, nil)), LanguageGerman)
	if reBracketRemux.MatchString(clean) {
		remux = true
	}
	lower := strings.ToLower(clean)
	for _, glued := range []struct {
		tag string
		res int
	}{{"bluray2160p", 2160}, {"bluray1080p", 1080}, {"bluray720p", 720}, {"bd2160p", 2160}, {"bd1080p", 1080}, {"bd720p", 720}} {
		if strings.Contains(lower, glued.tag) && res == 0 {
			res = glued.res
		}
	}

	switch source {
	case srcBluray:
		if reXvidDivx.MatchString(clean) {
			return radarrQuality("Bluray-480p")
		}
		encoded := reEncoder.MatchString(clean) || (german && reAV1.MatchString(clean))
		uhd := res == 2160 || (res == 0 && reUHDTag.MatchString(clean))
		discCodec := reDiscCodec.MatchString(clean)
		hevc := reHEVC.MatchString(clean)
		complete := reCompleteBD.MatchString(clean)
		if !remux && !encoded {
			switch {
			case german && (discCodec || hevc):
				// German scene remuxes are named "German.1080p.BluRay.AVC".
				remux = true
			case reDiscTags.MatchString(clean),
				discCodec && res != 720,
				hevc && res != 720 && res != 1080,
				complete && !hevc && res != 720 && res != 576:
				return radarrQuality("BR-DISK")
			}
		}
		switch {
		case res == 2160 || (res == 0 && uhd):
			if remux {
				return radarrQuality("Remux-2160p")
			}
			return radarrQuality("Bluray-2160p")
		case res == 1080:
			if remux {
				return radarrQuality("Remux-1080p")
			}
			return radarrQuality("Bluray-1080p")
		case res == 576:
			return radarrQuality("Bluray-576p")
		case res == 360 || res == 480 || res == 540:
			return radarrQuality("Bluray-480p")
		}
		if remux && res != 720 {
			return radarrQuality("Remux-1080p")
		}
		return radarrQuality("Bluray-720p")
	case srcWebDL:
		return radarrQuality(pickRes(res, map[int]string{2160: "WEBDL-2160p", 1080: "WEBDL-1080p", 720: "WEBDL-720p"}, "WEBDL-480p"))
	case srcWebRip:
		return radarrQuality(pickRes(res, map[int]string{2160: "WEBRip-2160p", 1080: "WEBRip-1080p", 720: "WEBRip-720p"}, "WEBRip-480p"))
	case srcHDTV:
		if reMPEG2.MatchString(clean) {
			return radarrQuality("Raw-HD")
		}
		return radarrQuality(pickRes(res, map[int]string{2160: "HDTV-2160p", 1080: "HDTV-1080p", 720: "HDTV-720p"}, "SDTV"))
	case srcBDRip:
		return radarrQuality(pickRes(res, map[int]string{720: "Bluray-720p", 1080: "Bluray-1080p", 2160: "Bluray-2160p", 576: "Bluray-576p"}, "Bluray-480p"))
	case srcDVD:
		if reDVDR.MatchString(clean) {
			return radarrQuality("DVD-R")
		}
		return radarrQuality("DVD")
	case srcTV:
		switch res {
		case 1080:
			return radarrQuality("HDTV-1080p")
		case 720:
			return radarrQuality("HDTV-720p")
		}
		return radarrQuality("SDTV")
	}

	if reDVDR.MatchString(clean) {
		return radarrQuality("DVD-R")
	}
	if remux {
		switch res {
		case 480:
			return radarrQuality("Bluray-480p")
		case 720:
			return radarrQuality("Bluray-720p")
		case 1080:
			return radarrQuality("Remux-1080p")
		case 2160:
			return radarrQuality("Remux-2160p")
		case 0:
		default:
			// A remux at a resolution without a remux quality (576p).
			return UnknownQuality(AppRadarr)
		}
	}
	if res != 0 {
		if q, ok := qualityForExtension(AppRadarr, raw); ok && q.Source != RadarrSourceUnknown {
			target := res
			if res != 2160 && res != 1080 && res != 720 {
				target = 480
			}
			src := q.Source
			if src == RadarrSourceTV && target == 480 {
				return radarrQuality("SDTV")
			}
			if found, ok := findBySourceAndResolution(AppRadarr, src, target); ok {
				return found
			}
		}
		switch res {
		case 2160:
			return radarrQuality("HDTV-2160p")
		case 1080:
			return radarrQuality("HDTV-1080p")
		case 720:
			return radarrQuality("HDTV-720p")
		}
		return radarrQuality("SDTV")
	}
	if reX264.MatchString(clean) {
		return radarrQuality("SDTV")
	}
	if q, ok := qualityForExtension(AppRadarr, raw); ok {
		return q
	}
	return UnknownQuality(AppRadarr)
}
