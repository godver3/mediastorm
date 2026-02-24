package metadata

import (
	"novastream/models"
	"sort"
	"strings"
)

// networkTimezoneMap maps known network names to IANA timezones.
// Used when TVDB doesn't provide a timezone on the originalNetwork object.
var networkTimezoneMap = map[string]string{
	// US networks
	"HBO":              "America/New_York",
	"Netflix":          "America/Los_Angeles",
	"Amazon":           "America/New_York",
	"Hulu":             "America/New_York",
	"Disney+":          "America/New_York",
	"Apple TV+":        "America/Los_Angeles",
	"NBC":              "America/New_York",
	"CBS":              "America/New_York",
	"ABC":              "America/New_York",
	"FOX":              "America/New_York",
	"The CW":           "America/New_York",
	"FX":               "America/New_York",
	"AMC":              "America/New_York",
	"Showtime":         "America/New_York",
	"Starz":            "America/New_York",
	"Paramount+":       "America/New_York",
	"Peacock":          "America/New_York",
	"Adult Swim":       "America/New_York",
	"Comedy Central":   "America/New_York",
	"Cartoon Network":  "America/New_York",
	"PBS":              "America/New_York",
	"Syfy":             "America/New_York",
	"USA Network":      "America/New_York",
	"TNT":              "America/New_York",
	"TBS":              "America/New_York",
	"A&E":              "America/New_York",
	"History":          "America/New_York",
	"MTV":              "America/New_York",
	"Bravo":            "America/New_York",
	"E!":               "America/New_York",
	"Freeform":         "America/New_York",
	"Lifetime":         "America/New_York",
	"BET":              "America/New_York",
	"VH1":              "America/New_York",
	"EPIX":             "America/New_York",
	"Cinemax":          "America/New_York",
	"Crunchyroll":      "America/Los_Angeles",
	"Max":              "America/New_York",
	// UK networks
	"BBC One":          "Europe/London",
	"BBC Two":          "Europe/London",
	"BBC Three":        "Europe/London",
	"ITV":              "Europe/London",
	"Channel 4":        "Europe/London",
	"Channel 5":        "Europe/London",
	"Sky Atlantic":     "Europe/London",
	"Sky One":          "Europe/London",
	"Sky":              "Europe/London",
	"Dave":             "Europe/London",
	"NOW":              "Europe/London",
	// South Korean networks
	"KBS":              "Asia/Seoul",
	"KBS2":             "Asia/Seoul",
	"MBC":              "Asia/Seoul",
	"SBS":              "Asia/Seoul",
	"tvN":              "Asia/Seoul",
	"JTBC":             "Asia/Seoul",
	"OCN":              "Asia/Seoul",
	// Japanese networks
	"NHK":              "Asia/Tokyo",
	"Fuji TV":          "Asia/Tokyo",
	"TBS (JP)":         "Asia/Tokyo",
	"TV Tokyo":         "Asia/Tokyo",
	"Tokyo MX":         "Asia/Tokyo",
	"TV Asahi":         "Asia/Tokyo",
	"Nippon TV":        "Asia/Tokyo",
	// Australian
	"ABC (AU)":         "Australia/Sydney",
	"Nine Network":     "Australia/Sydney",
	"Seven Network":    "Australia/Sydney",
	"Network 10":       "Australia/Sydney",
	"Stan":             "Australia/Sydney",
	"Binge":            "Australia/Sydney",
}

// networkKeysByLength holds network keys sorted by length descending (then
// alphabetical) so that partial matching always picks the longest (most
// specific) match first, making the result deterministic.
var networkKeysByLength []string

func init() {
	networkKeysByLength = make([]string, 0, len(networkTimezoneMap))
	for k := range networkTimezoneMap {
		networkKeysByLength = append(networkKeysByLength, k)
	}
	sort.Slice(networkKeysByLength, func(i, j int) bool {
		if len(networkKeysByLength[i]) != len(networkKeysByLength[j]) {
			return len(networkKeysByLength[i]) > len(networkKeysByLength[j])
		}
		return networkKeysByLength[i] < networkKeysByLength[j]
	})
}

// countryTimezoneMap maps country codes to default IANA timezones.
// Fallback when network name isn't in networkTimezoneMap.
var countryTimezoneMap = map[string]string{
	"usa": "America/New_York",
	"can": "America/New_York",
	"gbr": "Europe/London",
	"aus": "Australia/Sydney",
	"jpn": "Asia/Tokyo",
	"kor": "Asia/Seoul",
	"deu": "Europe/Berlin",
	"fra": "Europe/Paris",
	"ita": "Europe/Rome",
	"esp": "Europe/Madrid",
	"bra": "America/Sao_Paulo",
	"ind": "Asia/Kolkata",
	"nzl": "Pacific/Auckland",
	"swe": "Europe/Stockholm",
	"nor": "Europe/Oslo",
	"dnk": "Europe/Copenhagen",
	"fin": "Europe/Helsinki",
	"nld": "Europe/Amsterdam",
	"bel": "Europe/Brussels",
	"aut": "Europe/Vienna",
	"che": "Europe/Zurich",
	"irl": "Europe/Dublin",
	"pol": "Europe/Warsaw",
	"tur": "Europe/Istanbul",
	"zaf": "Africa/Johannesburg",
	"mex": "America/Mexico_City",
	"arg": "America/Argentina/Buenos_Aires",
	"chn": "Asia/Shanghai",
	"twn": "Asia/Taipei",
	"hkg": "Asia/Hong_Kong",
	"sgp": "Asia/Singapore",
	"tha": "Asia/Bangkok",
	"mys": "Asia/Kuala_Lumpur",
	"phl": "Asia/Manila",
	"idn": "Asia/Jakarta",
	"isr": "Asia/Jerusalem",
}

// inferTimezoneFromNetwork returns an IANA timezone string by matching the
// network name or falling back to the country code. Returns empty string if
// no match is found.
func inferTimezoneFromNetwork(networkName, country string) string {
	if networkName != "" {
		// Direct match
		if tz, ok := networkTimezoneMap[networkName]; ok {
			return tz
		}
		// Partial match (case-insensitive), longest key first for determinism
		lower := strings.ToLower(networkName)
		for _, key := range networkKeysByLength {
			if strings.Contains(lower, strings.ToLower(key)) {
				return networkTimezoneMap[key]
			}
		}
	}
	// Fall back to country code
	if country != "" {
		if tz, ok := countryTimezoneMap[strings.ToLower(country)]; ok {
			return tz
		}
	}
	return ""
}

// applyAirTimeFromTVDB sets AirsTime and AirsTimezone on a Title from TVDB extended data.
func applyAirTimeFromTVDB(title *models.Title, airsTime string, networkName string, networkCountry string) {
	airsTime = strings.TrimSpace(airsTime)
	if airsTime != "" {
		title.AirsTime = airsTime
	}
	if title.AirsTimezone == "" && title.AirsTime != "" {
		tz := inferTimezoneFromNetwork(networkName, networkCountry)
		if tz != "" {
			title.AirsTimezone = tz
		}
	}
}
