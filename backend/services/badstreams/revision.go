package badstreams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"novastream/models"
	"novastream/utils/language"
)

// RevisionFor identifies active rules that could affect this playback intent.
// An empty title selects the profile/device manifest. Track rules are included
// before a probe chooses an index; episode rules never qualify a series card.
// Hit counters and timestamps of repeated permanent reports do not invalidate
// caches, but changed scope, removal and temporary-rule expiry do.
func (s *Service) RevisionFor(ctx PlaybackContext) string {
	if s == nil {
		return "none"
	}
	now := time.Now()
	s.mu.RLock()
	parts := make([]string, 0)
	for _, entry := range s.entries {
		if scope := entry.Scope; scope != nil {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue
			}
			if ctx.TitleID != "" && scope.TitleID != strings.ToLower(strings.TrimSpace(ctx.TitleID)) ||
				scope.ProfileID != "" && scope.ProfileID != ctx.ProfileID ||
				scope.ClientID != "" && scope.ClientID != ctx.ClientID ||
				ctx.TitleID != "" && scope.AudioLanguage != "" && language.NormalizeToCode(ctx.AudioLanguage) != scope.AudioLanguage {
				continue
			}
			if scope.SeasonNumber != nil && (ctx.Episode == nil || scope.EpisodeNumber == nil ||
				ctx.Episode.SeasonNumber != *scope.SeasonNumber || ctx.Episode.EpisodeNumber != *scope.EpisodeNumber ||
				models.EpisodeNumberingKey(ctx.Episode.Numbering) != scope.NumberingKey) {
				continue
			}
			if scope.SeasonNumber == nil && ctx.Episode != nil {
				continue
			}
		}
		data, _ := json.Marshal([]any{entry.ID, entry.NormalizedReleaseName, entry.ContentIdentity, entry.ServiceType, entry.Provider, entry.Scope, entry.ExpiresAt})
		parts = append(parts, string(data))
	}
	s.mu.RUnlock()
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
