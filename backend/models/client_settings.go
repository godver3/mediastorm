package models

// ClientFilterSettings contains per-client overrides.
// These fields use pointers to distinguish between "not set" (nil = use profile/global default)
// and explicit values (including zero/false).
type ClientFilterSettings struct {
	// Filtering overrides
	MaxSizeMovieGB                   *float64     `json:"maxSizeMovieGb,omitempty"`
	MaxSizeEpisodeGB                 *float64     `json:"maxSizeEpisodeGb,omitempty"`
	MaxResolution                    *string      `json:"maxResolution,omitempty"`
	HDRDVPolicy                      *HDRDVPolicy `json:"hdrDvPolicy,omitempty"`
	PrioritizeHdr                    *bool        `json:"prioritizeHdr,omitempty"`
	FilterOutTerms                   *[]string    `json:"filterOutTerms,omitempty"`
	PreferredTerms                   *[]string    `json:"preferredTerms,omitempty"`
	NonPreferredTerms                *[]string    `json:"nonPreferredTerms,omitempty"`
	BypassFilteringForAIOStreamsOnly *bool        `json:"bypassFilteringForAioStreamsOnly,omitempty"`

	// Network settings for URL switching based on WiFi
	HomeWifiSSID     *string `json:"homeWifiSSID,omitempty"`
	HomeBackendUrl   *string `json:"homeBackendUrl,omitempty"`
	RemoteBackendUrl *string `json:"remoteBackendUrl,omitempty"`

	// Ranking criteria overrides
	RankingCriteria *[]ClientRankingCriterion `json:"rankingCriteria,omitempty"`
}

// IsEmpty returns true if no settings are configured
func (c *ClientFilterSettings) IsEmpty() bool {
	return c.MaxSizeMovieGB == nil &&
		c.MaxSizeEpisodeGB == nil &&
		c.MaxResolution == nil &&
		c.HDRDVPolicy == nil &&
		c.PrioritizeHdr == nil &&
		c.FilterOutTerms == nil &&
		c.PreferredTerms == nil &&
		c.NonPreferredTerms == nil &&
		c.BypassFilteringForAIOStreamsOnly == nil &&
		c.HomeWifiSSID == nil &&
		c.HomeBackendUrl == nil &&
		c.RemoteBackendUrl == nil &&
		c.RankingCriteria == nil
}
