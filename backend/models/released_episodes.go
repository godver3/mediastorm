package models

// ReleasedEpisode is one released regular-season episode of a series, as the
// unwatched-count badges see it. Absolute is the release absolute number when
// the catalog has one, so legacy history rows recorded under absolute
// numbering (e.g. S23E1163) still match their season-relative episode.
type ReleasedEpisode struct {
	Season   int `json:"s"`
	Episode  int `json:"e"`
	Absolute int `json:"a,omitempty"`
}

// ReleasedEpisodeSet answers "is this watched episode one of the released
// episodes the total counts?". Unwatched counts are total minus watched, so a
// watched row for an episode the catalog does not list (a renumbered reunion,
// a TVDB-only number, an episode the provider since removed) must not count,
// or the badge undercounts and can mark a series complete too early.
type ReleasedEpisodeSet struct {
	keys     map[[2]int]struct{}
	absolute map[int][2]int
}

// NewReleasedEpisodeSet builds a set from released episodes; specials and
// invalid numbers are ignored and duplicates collapse.
func NewReleasedEpisodeSet(episodes []ReleasedEpisode) *ReleasedEpisodeSet {
	set := &ReleasedEpisodeSet{
		keys:     make(map[[2]int]struct{}, len(episodes)),
		absolute: make(map[int][2]int),
	}
	for _, ep := range episodes {
		if ep.Season <= 0 || ep.Episode <= 0 {
			continue
		}
		key := [2]int{ep.Season, ep.Episode}
		set.keys[key] = struct{}{}
		if ep.Absolute > 0 {
			set.absolute[ep.Absolute] = key
		}
	}
	return set
}

// Len is the number of distinct released episodes; zero for a nil set.
func (s *ReleasedEpisodeSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.keys)
}

// Contains reports whether a history row's season/episode is a released
// episode. A number missing from the season is read as an absolute number
// only when that maps forward (or within the same season), mirroring the
// continue-watching reconciliation, so stale S17E03 rows are not taken as #3.
func (s *ReleasedEpisodeSet) Contains(season, episode int) bool {
	if s == nil || season <= 0 || episode <= 0 {
		return false
	}
	if _, ok := s.keys[[2]int{season, episode}]; ok {
		return true
	}
	if mapped, ok := s.absolute[episode]; ok && mapped[0] >= season {
		return true
	}
	return false
}
