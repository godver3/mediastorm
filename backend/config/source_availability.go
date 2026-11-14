package config

// SourceAvailabilitySettings limits optional, search-only background checks.
// No provider is selected implicitly, including on an existing installation.
type SourceAvailabilitySettings struct {
	Enabled           bool     `json:"enabled"`
	IndexerNames      []string `json:"indexerNames"`
	RefreshHours      int      `json:"refreshHours"`
	MaxTitlesPerCycle int      `json:"maxTitlesPerCycle"`
}

func (s SourceAvailabilitySettings) EffectiveRefreshHours() int {
	switch s.RefreshHours {
	case 6, 8, 12, 24:
		return s.RefreshHours
	default:
		return 6
	}
}
func (s SourceAvailabilitySettings) EffectiveMaxTitles() int {
	if s.MaxTitlesPerCycle < 1 {
		return 20
	}
	if s.MaxTitlesPerCycle > 50 {
		return 50
	}
	return s.MaxTitlesPerCycle
}
