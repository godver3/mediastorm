package models

// ClientFilterSettings contains per-client filtering overrides.
// These fields use pointers to distinguish between "not set" (nil = use profile/global default)
// and explicit values (including zero/false).
type ClientFilterSettings struct {
	MaxSizeMovieGB   *float64  `json:"maxSizeMovieGb,omitempty"`
	MaxSizeEpisodeGB *float64  `json:"maxSizeEpisodeGb,omitempty"`
	MaxResolution    *string   `json:"maxResolution,omitempty"`
	ExcludeHdr       *bool     `json:"excludeHdr,omitempty"`
	PrioritizeHdr    *bool     `json:"prioritizeHdr,omitempty"`
	FilterOutTerms   *[]string `json:"filterOutTerms,omitempty"`
	PreferredTerms   *[]string `json:"preferredTerms,omitempty"`
}

// IsEmpty returns true if no settings are configured
func (c *ClientFilterSettings) IsEmpty() bool {
	return c.MaxSizeMovieGB == nil &&
		c.MaxSizeEpisodeGB == nil &&
		c.MaxResolution == nil &&
		c.ExcludeHdr == nil &&
		c.PrioritizeHdr == nil &&
		c.FilterOutTerms == nil &&
		c.PreferredTerms == nil
}
