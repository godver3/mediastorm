package models

import (
	"slices"
	"time"
)

// Taste interests are an explicit starting selection, independent of ratings,
// watched state, watchlists and external activity services.
type TasteInterest struct {
	Title   Title     `json:"title"`
	Aliases []string  `json:"aliases"`
	AddedAt time.Time `json:"addedAt"`
}
type TasteOnboardingState struct {
	Status    string
	Source    string
	Interests []TasteInterest
	UpdatedAt time.Time
}

// MergeTasteInterests preserves verified identities and original selection
// dates for titles kept by an edit. Omitted titles are deliberately removed.
func MergeTasteInterests(previous, next []TasteInterest) []TasteInterest {
	out := []TasteInterest{}
	matches := func(a, b TasteInterest) bool {
		if a.Title.MediaType != b.Title.MediaType {
			return false
		}
		for _, key := range a.Aliases {
			if slices.Contains(b.Aliases, key) {
				return true
			}
		}
		return false
	}
	merge := func(current, old TasteInterest) TasteInterest {
		current.Aliases = append(append([]string(nil), current.Aliases...), old.Aliases...)
		slices.Sort(current.Aliases)
		current.Aliases = slices.Compact(current.Aliases)
		if !old.AddedAt.IsZero() && (current.AddedAt.IsZero() || old.AddedAt.Before(current.AddedAt)) {
			current.AddedAt = old.AddedAt
		}
		if current.Title.Name == "" {
			current.Title.Name = old.Title.Name
		}
		if current.Title.Year == 0 {
			current.Title.Year = old.Title.Year
		}
		if current.Title.TMDBID == 0 {
			current.Title.TMDBID = old.Title.TMDBID
		}
		if current.Title.TVDBID == 0 {
			current.Title.TVDBID = old.Title.TVDBID
		}
		if current.Title.IMDBID == "" {
			current.Title.IMDBID = old.Title.IMDBID
		}
		return current
	}
	for _, item := range next {
		for _, old := range previous {
			if matches(item, old) {
				item = merge(item, old)
			}
		}
		// A newly resolved title can bridge two previously separate aliases.
		// Repeat after merging so transitive duplicates also converge.
		for {
			changed := false
			kept := []TasteInterest{}
			for _, old := range out {
				if matches(item, old) {
					item = merge(item, old)
					changed = true
				} else {
					kept = append(kept, old)
				}
			}
			out = kept
			if !changed {
				break
			}
		}
		out = append(out, item)
	}
	return out
}
