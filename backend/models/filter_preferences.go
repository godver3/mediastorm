package models

// WithoutPreferences returns filter settings with every preference filter
// removed (size and resolution limits, HDR policy, required/blocked and
// ranking terms) while keeping operational settings: service priority,
// preferred scraper, unknown-track policy, adaptive playback and provider
// restrictions. Quality presets use it so their profile decides what is
// wanted; adaptive caps are applied on top afterwards.
func (f FilterSettings) WithoutPreferences() FilterSettings {
	return FilterSettings{
		PreferredScraper:                       f.PreferredScraper,
		ServicePriority:                        f.ServicePriority,
		UnknownTrackPolicy:                     f.UnknownTrackPolicy,
		AdaptivePlaybackEnabled:                f.AdaptivePlaybackEnabled,
		AdaptiveTargetBufferFactor:             f.AdaptiveTargetBufferFactor,
		RealDebridRestrictedTermsFilterEnabled: f.RealDebridRestrictedTermsFilterEnabled,
		RequiredTerms:                          []string{},
		FilterOutTerms:                         []string{},
		PreferredTerms:                         []string{},
		NonPreferredTerms:                      []string{},
		DownloadPreferredTerms:                 []string{},
	}
}
