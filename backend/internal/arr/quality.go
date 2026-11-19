package arr

import (
	"strings"
)

// Source is an app-specific quality source value. Sonarr and Radarr use
// different enums, so values must always be interpreted with their App.
type Source int

// Sonarr QualitySource values.
const (
	SonarrSourceUnknown       Source = 0
	SonarrSourceTelevision    Source = 1
	SonarrSourceTelevisionRaw Source = 2
	SonarrSourceWeb           Source = 3
	SonarrSourceWebRip        Source = 4
	SonarrSourceDVD           Source = 5
	SonarrSourceBluray        Source = 6
	SonarrSourceBlurayRaw     Source = 7
)

// Radarr QualitySource values.
const (
	RadarrSourceUnknown   Source = 0
	RadarrSourceCAM       Source = 1
	RadarrSourceTelesync  Source = 2
	RadarrSourceTelecine  Source = 3
	RadarrSourceWorkprint Source = 4
	RadarrSourceDVD       Source = 5
	RadarrSourceTV        Source = 6
	RadarrSourceWebDL     Source = 7
	RadarrSourceWebRip    Source = 8
	RadarrSourceBluray    Source = 9
)

// Modifier is Radarr's quality modifier (Sonarr has none).
type Modifier int

const (
	ModifierNone     Modifier = 0
	ModifierRegional Modifier = 1
	ModifierScreener Modifier = 2
	ModifierRawHD    Modifier = 3
	ModifierBRDisk   Modifier = 4
	ModifierRemux    Modifier = 5
)

// Quality is one *arr quality (e.g. "WEBDL-1080p").
type Quality struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Source     Source   `json:"source"`
	Resolution int      `json:"resolution"`
	Modifier   Modifier `json:"modifier,omitempty"`
}

// QualityDefinition holds the default ordering weight, grouping and size
// limits (MB per minute of runtime) for a quality.
type QualityDefinition struct {
	Quality       Quality  `json:"quality"`
	Weight        int      `json:"weight"`
	GroupName     string   `json:"groupName,omitempty"`
	MinSize       float64  `json:"minSize"`
	MaxSize       *float64 `json:"maxSize,omitempty"`
	PreferredSize *float64 `json:"preferredSize,omitempty"`
}

func f(v float64) *float64 { return &v }

var sonarrQualities = []QualityDefinition{
	{Quality: Quality{0, "Unknown", SonarrSourceUnknown, 0, 0}, Weight: 1, MinSize: 1, MaxSize: f(199.9), PreferredSize: f(95)},
	{Quality: Quality{1, "SDTV", SonarrSourceTelevision, 480, 0}, Weight: 2, MinSize: 2, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{12, "WEBRip-480p", SonarrSourceWebRip, 480, 0}, Weight: 3, GroupName: "WEB 480p", MinSize: 2, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{8, "WEBDL-480p", SonarrSourceWeb, 480, 0}, Weight: 3, GroupName: "WEB 480p", MinSize: 2, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{2, "DVD", SonarrSourceDVD, 480, 0}, Weight: 4, MinSize: 2, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{13, "Bluray-480p", SonarrSourceBluray, 480, 0}, Weight: 5, MinSize: 2, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{22, "Bluray-576p", SonarrSourceBluray, 576, 0}, Weight: 6, MinSize: 2, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{4, "HDTV-720p", SonarrSourceTelevision, 720, 0}, Weight: 7, MinSize: 3, MaxSize: f(125), PreferredSize: f(95)},
	{Quality: Quality{9, "HDTV-1080p", SonarrSourceTelevision, 1080, 0}, Weight: 8, MinSize: 4, MaxSize: f(125), PreferredSize: f(95)},
	{Quality: Quality{10, "Raw-HD", SonarrSourceTelevisionRaw, 1080, 0}, Weight: 9, MinSize: 4, PreferredSize: f(95)},
	{Quality: Quality{14, "WEBRip-720p", SonarrSourceWebRip, 720, 0}, Weight: 10, GroupName: "WEB 720p", MinSize: 3, MaxSize: f(130), PreferredSize: f(95)},
	{Quality: Quality{5, "WEBDL-720p", SonarrSourceWeb, 720, 0}, Weight: 10, GroupName: "WEB 720p", MinSize: 3, MaxSize: f(130), PreferredSize: f(95)},
	{Quality: Quality{6, "Bluray-720p", SonarrSourceBluray, 720, 0}, Weight: 11, MinSize: 4, MaxSize: f(130), PreferredSize: f(95)},
	{Quality: Quality{15, "WEBRip-1080p", SonarrSourceWebRip, 1080, 0}, Weight: 12, GroupName: "WEB 1080p", MinSize: 4, MaxSize: f(130), PreferredSize: f(95)},
	{Quality: Quality{3, "WEBDL-1080p", SonarrSourceWeb, 1080, 0}, Weight: 12, GroupName: "WEB 1080p", MinSize: 4, MaxSize: f(130), PreferredSize: f(95)},
	{Quality: Quality{7, "Bluray-1080p", SonarrSourceBluray, 1080, 0}, Weight: 13, MinSize: 4, MaxSize: f(155), PreferredSize: f(95)},
	{Quality: Quality{20, "Bluray-1080p Remux", SonarrSourceBlurayRaw, 1080, 0}, Weight: 14, MinSize: 35, PreferredSize: f(95)},
	{Quality: Quality{16, "HDTV-2160p", SonarrSourceTelevision, 2160, 0}, Weight: 15, MinSize: 35, MaxSize: f(199.9), PreferredSize: f(95)},
	{Quality: Quality{17, "WEBRip-2160p", SonarrSourceWebRip, 2160, 0}, Weight: 16, GroupName: "WEB 2160p", MinSize: 35, PreferredSize: f(95)},
	{Quality: Quality{18, "WEBDL-2160p", SonarrSourceWeb, 2160, 0}, Weight: 16, GroupName: "WEB 2160p", MinSize: 35, PreferredSize: f(95)},
	{Quality: Quality{19, "Bluray-2160p", SonarrSourceBluray, 2160, 0}, Weight: 17, MinSize: 35, PreferredSize: f(95)},
	{Quality: Quality{21, "Bluray-2160p Remux", SonarrSourceBlurayRaw, 2160, 0}, Weight: 18, MinSize: 35, PreferredSize: f(95)},
}

var radarrQualities = []QualityDefinition{
	{Quality: Quality{0, "Unknown", RadarrSourceUnknown, 0, ModifierNone}, Weight: 1, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{24, "WORKPRINT", RadarrSourceWorkprint, 0, ModifierNone}, Weight: 2, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{25, "CAM", RadarrSourceCAM, 0, ModifierNone}, Weight: 3, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{26, "TELESYNC", RadarrSourceTelesync, 0, ModifierNone}, Weight: 4, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{27, "TELECINE", RadarrSourceTelecine, 0, ModifierNone}, Weight: 5, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{29, "REGIONAL", RadarrSourceDVD, 480, ModifierRegional}, Weight: 6, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{28, "DVDSCR", RadarrSourceDVD, 480, ModifierScreener}, Weight: 7, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{1, "SDTV", RadarrSourceTV, 480, ModifierNone}, Weight: 8, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{2, "DVD", RadarrSourceDVD, 0, ModifierNone}, Weight: 9, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{23, "DVD-R", RadarrSourceDVD, 480, ModifierRemux}, Weight: 10, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{8, "WEBDL-480p", RadarrSourceWebDL, 480, ModifierNone}, Weight: 11, GroupName: "WEB 480p", MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{12, "WEBRip-480p", RadarrSourceWebRip, 480, ModifierNone}, Weight: 11, GroupName: "WEB 480p", MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{20, "Bluray-480p", RadarrSourceBluray, 480, ModifierNone}, Weight: 12, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{21, "Bluray-576p", RadarrSourceBluray, 576, ModifierNone}, Weight: 13, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{4, "HDTV-720p", RadarrSourceTV, 720, ModifierNone}, Weight: 14, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{5, "WEBDL-720p", RadarrSourceWebDL, 720, ModifierNone}, Weight: 15, GroupName: "WEB 720p", MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{14, "WEBRip-720p", RadarrSourceWebRip, 720, ModifierNone}, Weight: 15, GroupName: "WEB 720p", MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{6, "Bluray-720p", RadarrSourceBluray, 720, ModifierNone}, Weight: 16, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{9, "HDTV-1080p", RadarrSourceTV, 1080, ModifierNone}, Weight: 17, MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{3, "WEBDL-1080p", RadarrSourceWebDL, 1080, ModifierNone}, Weight: 18, GroupName: "WEB 1080p", MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{15, "WEBRip-1080p", RadarrSourceWebRip, 1080, ModifierNone}, Weight: 18, GroupName: "WEB 1080p", MaxSize: f(100), PreferredSize: f(95)},
	{Quality: Quality{7, "Bluray-1080p", RadarrSourceBluray, 1080, ModifierNone}, Weight: 19},
	{Quality: Quality{30, "Remux-1080p", RadarrSourceBluray, 1080, ModifierRemux}, Weight: 20},
	{Quality: Quality{16, "HDTV-2160p", RadarrSourceTV, 2160, ModifierNone}, Weight: 21},
	{Quality: Quality{18, "WEBDL-2160p", RadarrSourceWebDL, 2160, ModifierNone}, Weight: 22, GroupName: "WEB 2160p"},
	{Quality: Quality{17, "WEBRip-2160p", RadarrSourceWebRip, 2160, ModifierNone}, Weight: 22, GroupName: "WEB 2160p"},
	{Quality: Quality{19, "Bluray-2160p", RadarrSourceBluray, 2160, ModifierNone}, Weight: 23},
	{Quality: Quality{31, "Remux-2160p", RadarrSourceBluray, 2160, ModifierRemux}, Weight: 24},
	{Quality: Quality{22, "BR-DISK", RadarrSourceBluray, 1080, ModifierBRDisk}, Weight: 25},
	{Quality: Quality{10, "Raw-HD", RadarrSourceTV, 1080, ModifierRawHD}, Weight: 26},
}

var (
	qualityByName = map[App]map[string]QualityDefinition{AppSonarr: {}, AppRadarr: {}}
	qualityByID   = map[App]map[int]QualityDefinition{AppSonarr: {}, AppRadarr: {}}
)

func init() {
	for _, def := range sonarrQualities {
		qualityByName[AppSonarr][strings.ToLower(def.Quality.Name)] = def
		qualityByID[AppSonarr][def.Quality.ID] = def
	}
	for _, def := range radarrQualities {
		qualityByName[AppRadarr][strings.ToLower(def.Quality.Name)] = def
		qualityByID[AppRadarr][def.Quality.ID] = def
	}
}

// Qualities returns the default quality definitions of an app ordered from
// lowest to highest weight.
func Qualities(app App) []QualityDefinition {
	src := sonarrQualities
	if app == AppRadarr {
		src = radarrQualities
	}
	out := make([]QualityDefinition, len(src))
	copy(out, src)
	return out
}

// QualityByName looks up a quality by its *arr name (case-insensitive).
func QualityByName(app App, name string) (Quality, bool) {
	def, ok := qualityByName[app][strings.ToLower(strings.TrimSpace(name))]
	return def.Quality, ok
}

// QualityByID looks up a quality by its *arr ID.
func QualityByID(app App, qid int) (Quality, bool) {
	def, ok := qualityByID[app][qid]
	return def.Quality, ok
}

// DefaultDefinition returns the default definition for a quality.
func DefaultDefinition(app App, q Quality) (QualityDefinition, bool) {
	def, ok := qualityByID[app][q.ID]
	return def, ok
}

// UnknownQuality returns the "Unknown" quality of an app.
func UnknownQuality(app App) Quality {
	q, _ := QualityByID(app, 0)
	return q
}

// SourceName returns the lower-case API name of a source value
// (e.g. "web", "bluray").
func SourceName(app App, s Source) string {
	if app == AppRadarr {
		switch s {
		case RadarrSourceCAM:
			return "cam"
		case RadarrSourceTelesync:
			return "telesync"
		case RadarrSourceTelecine:
			return "telecine"
		case RadarrSourceWorkprint:
			return "workprint"
		case RadarrSourceDVD:
			return "dvd"
		case RadarrSourceTV:
			return "tv"
		case RadarrSourceWebDL:
			return "webdl"
		case RadarrSourceWebRip:
			return "webrip"
		case RadarrSourceBluray:
			return "bluray"
		}
		return "unknown"
	}
	switch s {
	case SonarrSourceTelevision:
		return "television"
	case SonarrSourceTelevisionRaw:
		return "televisionRaw"
	case SonarrSourceWeb:
		return "web"
	case SonarrSourceWebRip:
		return "webRip"
	case SonarrSourceDVD:
		return "dvd"
	case SonarrSourceBluray:
		return "bluray"
	case SonarrSourceBlurayRaw:
		return "blurayRaw"
	}
	return "unknown"
}

// SourceOptions lists the source values of an app for select inputs.
func SourceOptions(app App) []EnumOption {
	n := 7
	if app == AppRadarr {
		n = 9
	}
	out := make([]EnumOption, 0, n+1)
	for i := 0; i <= n; i++ {
		out = append(out, EnumOption{Value: i, Name: SourceName(app, Source(i))})
	}
	return out
}

// EnumOption is a (value, name) pair for select inputs.
type EnumOption struct {
	Value int    `json:"value"`
	Name  string `json:"name"`
}

// ModifierName returns Radarr's API name for a modifier.
func ModifierName(m Modifier) string {
	switch m {
	case ModifierRegional:
		return "regional"
	case ModifierScreener:
		return "screener"
	case ModifierRawHD:
		return "rawhd"
	case ModifierBRDisk:
		return "brdisk"
	case ModifierRemux:
		return "remux"
	}
	return "none"
}

// Resolutions are the values ResolutionSpecification accepts.
var Resolutions = []int{360, 480, 540, 576, 720, 1080, 2160}

// Revision is a release's proper/repack revision.
type Revision struct {
	Version  int  `json:"version"`
	Real     int  `json:"real"`
	IsRepack bool `json:"isRepack"`
}

// Compare orders revisions by real then version, like Sonarr/Radarr's
// Revision.CompareTo: a REAL release beats any PROPER/REPACK version.
func (r Revision) Compare(o Revision) int {
	if r.Real != o.Real {
		return cmpInt(r.Real, o.Real)
	}
	return cmpInt(r.Version, o.Version)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
