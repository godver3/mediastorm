package handlers

import (
	"sort"
	"strings"

	"novastream/config"
	"novastream/models"
)

type liveUsageByUserRow struct {
	ProfileID string `json:"profileId"`
	User      string `json:"user"`
	Provider  string `json:"provider"`
	Bucket    string `json:"bucket"`
	Current   int    `json:"current"`
	Max       int    `json:"max"`
	Available int    `json:"available"`
	AtLimit   bool   `json:"atLimit"`
}

type liveUsageBucketRow struct {
	BucketID  string   `json:"bucketId"`
	Label     string   `json:"label"`
	Provider  string   `json:"provider"`
	Users     []string `json:"users"`
	Current   int      `json:"current"`
	Max       int      `json:"max"`
	Available int      `json:"available"`
	AtLimit   bool     `json:"atLimit"`
}

func (h *AdminUIHandler) buildDashboardLiveUsage(isAdmin bool, scopedUsers []models.User, allowedProfileIDs map[string]bool) (LiveUsageSummary, []liveUsageByUserRow, []liveUsageBucketRow) {
	settings := config.Settings{}
	if h.configManager != nil {
		if loaded, err := h.configManager.Load(); err == nil {
			settings = loaded
		}
	}
	global := buildGlobalLiveSource(settings)

	targetByProfile := map[string]liveStreamTarget{}
	userNameByProfile := map[string]string{}
	bucketUsers := map[string][]string{}
	for _, user := range scopedUsers {
		var userSettings *models.UserSettings
		if h.userSettingsService != nil {
			if us, err := h.userSettingsService.Get(user.ID); err == nil && us != nil {
				userSettings = us
			}
		}
		target := resolveLiveStreamTarget(global, userSettings)
		targetByProfile[user.ID] = target
		userNameByProfile[user.ID] = user.Name
		bucketUsers[target.BucketKey] = append(bucketUsers[target.BucketKey], user.Name)
	}

	bucketCurrent := map[string]int{}
	bucketProvider := map[string]string{}
	bucketLabel := map[string]string{}

	if h.hlsManager != nil {
		h.hlsManager.mu.RLock()
		for _, session := range h.hlsManager.sessions {
			session.mu.RLock()
			if !session.IsLive || session.Completed {
				session.mu.RUnlock()
				continue
			}
			profileID := strings.TrimSpace(session.ProfileID)
			profileName := strings.TrimSpace(session.ProfileName)
			provider := normalizeLiveProvider(session.LiveProvider)
			bucketID := strings.TrimSpace(session.LiveBucket)
			if bucketID == "" {
				bucketID = provider + ":default"
			}
			session.mu.RUnlock()

			isDefaultProfile := strings.EqualFold(profileID, "default") || profileID == ""
			hasValidProfileName := profileName != "" && !strings.EqualFold(profileName, "default")
			if isDefaultProfile && !hasValidProfileName {
				continue
			}
			if !isAdmin && !allowedProfileIDs[profileID] {
				continue
			}

			bucketCurrent[bucketID]++
			bucketProvider[bucketID] = provider
			if target, ok := targetByProfile[profileID]; ok {
				bucketLabel[bucketID] = target.BucketName
			}
			if profileName != "" && profileID != "" {
				userNameByProfile[profileID] = profileName
			}
		}
		h.hlsManager.mu.RUnlock()
	}

	byUser := make([]liveUsageByUserRow, 0, len(scopedUsers))
	for _, user := range scopedUsers {
		target, ok := targetByProfile[user.ID]
		if !ok {
			target = resolveLiveStreamTarget(global, nil)
		}
		current := bucketCurrent[target.BucketKey]
		available := 0
		if target.MaxStreams > 0 {
			available = target.MaxStreams - current
			if available < 0 {
				available = 0
			}
		}
		byUser = append(byUser, liveUsageByUserRow{
			ProfileID: user.ID,
			User:      user.Name,
			Provider:  target.Provider,
			Bucket:    target.BucketName,
			Current:   current,
			Max:       target.MaxStreams,
			Available: available,
			AtLimit:   target.MaxStreams > 0 && current >= target.MaxStreams,
		})
	}

	bucketRows := make([]liveUsageBucketRow, 0, len(bucketCurrent))
	for bucketID, current := range bucketCurrent {
		maxStreams := 0
		provider := bucketProvider[bucketID]
		label := bucketLabel[bucketID]
		if label == "" {
			if provider == "xtream" {
				label = "XTREAM shared"
			} else {
				label = "M3U shared"
			}
		}
		for profileID, target := range targetByProfile {
			if target.BucketKey != bucketID {
				continue
			}
			provider = target.Provider
			if maxStreams == 0 || (target.MaxStreams > 0 && target.MaxStreams < maxStreams) {
				maxStreams = target.MaxStreams
			}
			if _, ok := userNameByProfile[profileID]; !ok {
				userNameByProfile[profileID] = profileID
			}
		}

		users := append([]string(nil), bucketUsers[bucketID]...)
		sort.Strings(users)
		available := 0
		if maxStreams > 0 {
			available = maxStreams - current
			if available < 0 {
				available = 0
			}
		}
		bucketRows = append(bucketRows, liveUsageBucketRow{
			BucketID:  bucketID,
			Label:     label,
			Provider:  provider,
			Users:     users,
			Current:   current,
			Max:       maxStreams,
			Available: available,
			AtLimit:   maxStreams > 0 && current >= maxStreams,
		})
	}

	sort.Slice(byUser, func(i, j int) bool {
		if byUser[i].User == byUser[j].User {
			return byUser[i].ProfileID < byUser[j].ProfileID
		}
		return byUser[i].User < byUser[j].User
	})
	sort.Slice(bucketRows, func(i, j int) bool {
		if bucketRows[i].Label == bucketRows[j].Label {
			return bucketRows[i].BucketID < bucketRows[j].BucketID
		}
		return bucketRows[i].Label < bucketRows[j].Label
	})

	summary := LiveUsageSummary{
		Provider:         "m3u",
		CurrentStreams:   0,
		MaxStreams:       0,
		AvailableStreams: 0,
		AtLimit:          false,
		Providers:        []LiveProviderUsageEntry{},
	}
	if len(bucketRows) > 0 {
		first := bucketRows[0]
		summary.Provider = first.Provider
		summary.CurrentStreams = first.Current
		summary.MaxStreams = first.Max
		summary.AvailableStreams = first.Available
		summary.AtLimit = first.AtLimit
		summary.Providers = []LiveProviderUsageEntry{
			{
				Provider:  first.Provider,
				Current:   first.Current,
				Max:       first.Max,
				Available: first.Available,
				AtLimit:   first.AtLimit,
			},
		}
	}
	return summary, byUser, bucketRows
}

type vodAccountUsageRow struct {
	AccountID   string   `json:"accountId"`
	AccountName string   `json:"accountName"`
	Profiles    []string `json:"profiles"`
	Current     int      `json:"current"`
	Max         int      `json:"max"`
	Available   int      `json:"available"`
	AtLimit     bool     `json:"atLimit"`
}

// buildVODStreamUsage builds per-account VOD stream usage data for the dashboard.
func (h *AdminUIHandler) buildVODStreamUsage(isAdmin bool, scopedUsers []models.User, allowedProfileIDs map[string]bool) []vodAccountUsageRow {
	if h.accountsService == nil {
		return nil
	}

	// Group profiles by account
	accountProfiles := map[string][]string{}
	accountNames := map[string]string{}
	for _, u := range scopedUsers {
		if !isAdmin && !allowedProfileIDs[u.ID] {
			continue
		}
		accountProfiles[u.AccountID] = append(accountProfiles[u.AccountID], u.Name)
	}

	// Get account names and max streams
	accounts := h.accountsService.List()
	for _, acc := range accounts {
		accountNames[acc.ID] = acc.Username
	}

	tracker := GetStreamTracker()
	var rows []vodAccountUsageRow

	for _, acc := range accounts {
		if acc.MaxStreams <= 0 {
			continue
		}
		if _, hasScopedProfiles := accountProfiles[acc.ID]; !hasScopedProfiles && !isAdmin {
			continue
		}

		usage := tracker.GetAccountStreamUsage(acc.ID, acc.MaxStreams)
		profiles := accountProfiles[acc.ID]
		sort.Strings(profiles)

		rows = append(rows, vodAccountUsageRow{
			AccountID:   acc.ID,
			AccountName: acc.Username,
			Profiles:    profiles,
			Current:     usage.CurrentStreams,
			Max:         usage.MaxStreams,
			Available:   usage.AvailableStreams,
			AtLimit:     usage.AtLimit,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].AccountName < rows[j].AccountName
	})

	return rows
}
