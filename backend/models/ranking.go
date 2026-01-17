package models

import "novastream/config"

// UserRankingCriterion represents a per-user override for a ranking criterion.
// Pointer fields allow distinguishing "not set" (nil) from explicit values.
type UserRankingCriterion struct {
	ID      config.RankingCriterionID `json:"id"`
	Enabled *bool                     `json:"enabled,omitempty"`
	Order   *int                      `json:"order,omitempty"`
}

// UserRankingSettings holds per-user ranking overrides.
type UserRankingSettings struct {
	Criteria []UserRankingCriterion `json:"criteria,omitempty"`
}

// ClientRankingCriterion represents a per-client override for a ranking criterion.
type ClientRankingCriterion struct {
	ID      config.RankingCriterionID `json:"id"`
	Enabled *bool                     `json:"enabled,omitempty"`
	Order   *int                      `json:"order,omitempty"`
}
