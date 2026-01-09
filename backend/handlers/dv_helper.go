package handlers

import (
	"fmt"
	"strconv"
	"strings"
)

// DVProfileError represents a Dolby Vision profile compatibility error
type DVProfileError struct {
	Profile     string
	ProfileNum  int
	Policy      string
	Description string
}

func (e *DVProfileError) Error() string {
	return fmt.Sprintf("DV_PROFILE_INCOMPATIBLE: %s", e.Description)
}

// ValidateDVProfile checks if a Dolby Vision profile is compatible with the given policy.
// Returns nil if compatible, or a DVProfileError if not.
//
// Policy values:
//   - "dolbyvision": Accept all DV profiles
//   - "hdr": Only accept DV profiles with HDR fallback (not profile 5)
//   - "sdr": No HDR support, will reject all DV content
func ValidateDVProfile(dvProfile string, policy string, hasDolbyVision bool) error {
	// If content doesn't have Dolby Vision, no validation needed
	if !hasDolbyVision {
		return nil
	}

	// Policy "dolbyvision" accepts all profiles
	if policy == "dolbyvision" {
		return nil
	}

	// Policy "hdr" requires a profile with HDR fallback layer
	if policy == "hdr" {
		profileNum := ParseDVProfile(dvProfile)
		if profileNum == 5 {
			return &DVProfileError{
				Profile:     dvProfile,
				ProfileNum:  profileNum,
				Policy:      policy,
				Description: "profile 5 has no HDR fallback layer",
			}
		}
	}

	return nil
}

// ParseDVProfile extracts the profile number from a Dolby Vision profile string.
// Example: "dvhe.05.06" -> 5, "8.4" -> 8
func ParseDVProfile(dvProfile string) int {
	parts := strings.Split(dvProfile, ".")
	if len(parts) < 2 {
		return 0
	}

	// Handle both "dvhe.05" and "05" formats
	profileStr := parts[0]
	if strings.HasPrefix(strings.ToLower(profileStr), "dvhe") || strings.HasPrefix(strings.ToLower(profileStr), "dvav") {
		if len(parts) >= 2 {
			profileStr = parts[1]
		}
	}

	// Remove leading zeros and parse
	profileStr = strings.TrimLeft(profileStr, "0")
	if profileStr == "" {
		return 0
	}

	profile, err := strconv.Atoi(profileStr)
	if err != nil {
		return 0
	}
	return profile
}

// IsDVProfile5 checks if the given profile is Dolby Vision profile 5
// which lacks an HDR fallback layer
func IsDVProfile5(dvProfile string) bool {
	return ParseDVProfile(dvProfile) == 5
}
