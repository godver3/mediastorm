package config

// RecommendationSettings controls profile-local inputs to the Similar-based
// recommendation engine. Recording search interests requires explicit opt-in.
type RecommendationSettings struct {
	UseWatchlist       *bool `json:"useWatchlist,omitempty"`
	UseCustomLists     *bool `json:"useCustomLists,omitempty"`
	UseSearchInterests *bool `json:"useSearchInterests,omitempty"`
}

func (s RecommendationSettings) Merge(profile *RecommendationSettings) RecommendationSettings {
	if profile == nil {
		return s
	}
	if profile.UseWatchlist != nil {
		s.UseWatchlist = profile.UseWatchlist
	}
	if profile.UseCustomLists != nil {
		s.UseCustomLists = profile.UseCustomLists
	}
	if profile.UseSearchInterests != nil {
		s.UseSearchInterests = profile.UseSearchInterests
	}
	return s
}
func (s RecommendationSettings) WithDefaults() RecommendationSettings {
	if s.UseWatchlist == nil {
		s.UseWatchlist = boolPtr(true)
	}
	if s.UseCustomLists == nil {
		s.UseCustomLists = boolPtr(true)
	}
	if s.UseSearchInterests == nil {
		s.UseSearchInterests = boolPtr(false)
	}
	return s
}
func (s RecommendationSettings) WatchlistEnabled() bool {
	return s.UseWatchlist == nil || *s.UseWatchlist
}
func (s RecommendationSettings) CustomListsEnabled() bool {
	return s.UseCustomLists == nil || *s.UseCustomLists
}
func (s RecommendationSettings) SearchInterestsEnabled() bool {
	return s.UseSearchInterests != nil && *s.UseSearchInterests
}
