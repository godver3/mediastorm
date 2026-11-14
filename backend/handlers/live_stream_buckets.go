package handlers

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"novastream/config"
	"novastream/models"
)

type liveStreamTarget struct {
	Provider           string
	SourceID           string
	MaxStreams         int
	BucketKey          string
	BucketName         string
	StreamFormat       string
	ProbeSizeMB        int
	AnalyzeDurationSec int
	LowLatency         bool
	ProxyURL           string
	Stalker            stalkerSourceConfig
}

func buildGlobalLiveSource(settings config.Settings) models.ResolvedLiveSource {
	maxStreams := settings.Live.MaxStreams
	if maxStreams < 0 {
		maxStreams = 0
	}
	return models.ResolvedLiveSource{
		Mode:                    settings.Live.Mode,
		PlaylistURL:             settings.Live.PlaylistURL,
		HDHomeRunHost:           settings.Live.HDHomeRunHost,
		ManifestURL:             settings.Live.ManifestURL,
		ProxyURL:                settings.Live.ProxyURL,
		Sources:                 configPlaylistSourcesToModel(settings.Live.Sources),
		PlaylistSources:         configPlaylistSourcesToModel(settings.Live.PlaylistSources),
		XtreamHost:              settings.Live.XtreamHost,
		XtreamUsername:          settings.Live.XtreamUsername,
		XtreamPassword:          settings.Live.XtreamPassword,
		StalkerPortalURL:        settings.Live.StalkerPortalURL,
		StalkerMAC:              settings.Live.StalkerMAC,
		StalkerSerialNumber:     settings.Live.StalkerSerialNumber,
		StalkerDeviceID:         settings.Live.StalkerDeviceID,
		StalkerDeviceID2:        settings.Live.StalkerDeviceID2,
		StalkerSignature:        settings.Live.StalkerSignature,
		StalkerModel:            settings.Live.StalkerModel,
		MaxStreams:              maxStreams,
		PlaylistCacheTTLHours:   settings.Live.PlaylistCacheTTLHours,
		ProbeSizeMB:             settings.Live.ProbeSizeMB,
		AnalyzeDurationSec:      settings.Live.AnalyzeDurationSec,
		LowLatency:              settings.Live.LowLatency,
		StreamFormat:            settings.Live.StreamFormat,
		EnabledCategories:       settings.Live.Filtering.EnabledCategories,
		MaxChannels:             settings.Live.Filtering.MaxChannels,
		EPGEnabled:              settings.Live.EPG.Enabled,
		EPGXmltvUrl:             settings.Live.EPG.XmltvUrl,
		EPGRefreshIntervalHours: settings.Live.EPG.RefreshIntervalHours,
		EPGRetentionDays:        settings.Live.EPG.RetentionDays,
	}
}

func configPlaylistSourcesToModel(sources []config.LivePlaylistSource) []models.LivePlaylistSource {
	if len(sources) == 0 {
		return nil
	}
	result := make([]models.LivePlaylistSource, 0, len(sources))
	for _, src := range sources {
		result = append(result, models.LivePlaylistSource{
			ID:                    src.ID,
			Name:                  src.Name,
			Mode:                  src.Mode,
			PlaylistURL:           src.PlaylistURL,
			HDHomeRunHost:         src.HDHomeRunHost,
			ManifestURL:           src.ManifestURL,
			ProxyURL:              src.ProxyURL,
			XtreamHost:            src.XtreamHost,
			XtreamUsername:        src.XtreamUsername,
			XtreamPassword:        src.XtreamPassword,
			StalkerPortalURL:      src.StalkerPortalURL,
			StalkerMAC:            src.StalkerMAC,
			StalkerSerialNumber:   src.StalkerSerialNumber,
			StalkerDeviceID:       src.StalkerDeviceID,
			StalkerDeviceID2:      src.StalkerDeviceID2,
			StalkerSignature:      src.StalkerSignature,
			StalkerModel:          src.StalkerModel,
			MaxStreams:            src.MaxStreams,
			PlaylistCacheTTLHours: src.PlaylistCacheTTLHours,
			ProbeSizeMB:           src.ProbeSizeMB,
			AnalyzeDurationSec:    src.AnalyzeDurationSec,
			StreamFormat:          src.StreamFormat,
			EnabledCategories:     src.Filtering.EnabledCategories,
			Enabled:               src.Enabled,
		})
		if src.LowLatency {
			result[len(result)-1].LowLatency = &src.LowLatency
		}
		if src.Filtering.MaxChannels != 0 {
			result[len(result)-1].MaxChannels = &src.Filtering.MaxChannels
		}
	}
	return result
}

func resolveLiveStreamTarget(global models.ResolvedLiveSource, profile *models.UserSettings) liveStreamTarget {
	targets := resolveLiveStreamTargets(global, profile)
	if len(targets) > 0 {
		return targets[0]
	}
	return liveStreamTarget{Provider: "m3u", MaxStreams: 0, BucketKey: "m3u:default", BucketName: "M3U shared"}
}

func pickLiveStreamTarget(targets []liveStreamTarget, sourceID string) liveStreamTarget {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID != "" && sourceID != "all" {
		for _, target := range targets {
			if target.SourceID == sourceID {
				return target
			}
		}
	}
	if len(targets) > 0 {
		return targets[0]
	}
	return liveStreamTarget{Provider: "m3u", MaxStreams: 0, BucketKey: "m3u:default", BucketName: "M3U shared"}
}

// resolveProfileLiveStreamTargets returns the Live TV targets profileID may
// use, honoring each global source's Allowed Profiles. It returns nil when
// the profile has no usable source, the same case where the Live TV channel
// endpoint refuses the profile.
func resolveProfileLiveStreamTargets(settings config.Settings, profileID string, profile *models.UserSettings, accountID string) []liveStreamTarget {
	scoped := config.FilterLiveSettingsForAccount(settings, accountID)
	global := buildGlobalLiveSource(config.FilterSettingsForProfile(scoped, profileID))
	resolved := applyProfileLiveSources(global, settings, profileID, profile, accountID)
	if len(resolvedLiveSources(resolved)) == 0 {
		return nil
	}
	return resolveLiveStreamTargets(resolved, nil)
}

func resolveLiveStreamTargets(global models.ResolvedLiveSource, profile *models.UserSettings) []liveStreamTarget {
	resolved := global
	if profile != nil {
		resolved = models.ResolveLiveSource(&profile.LiveTV, &global)
	}

	sources := resolvedLiveSources(resolved)
	if len(sources) > 0 {
		targets := make([]liveStreamTarget, 0, len(sources))
		for _, source := range sources {
			provider := normalizeLiveProvider(source.Mode)
			streamFormat := strings.TrimSpace(source.StreamFormat)
			if streamFormat == "" {
				streamFormat = resolved.StreamFormat
			}
			maxStreams := source.MaxStreams
			if maxStreams < 0 {
				maxStreams = 0
			}
			bucketKey, bucketName := deriveLiveSourceBucket(provider, source)
			targets = append(targets, liveStreamTarget{
				Provider:           provider,
				SourceID:           source.ID,
				MaxStreams:         maxStreams,
				BucketKey:          bucketKey,
				BucketName:         bucketName,
				StreamFormat:       streamFormat,
				ProbeSizeMB:        resolved.ProbeSizeMB,
				AnalyzeDurationSec: resolved.AnalyzeDurationSec,
				LowLatency:         resolved.LowLatency,
				ProxyURL:           strings.TrimSpace(source.ProxyURL),
				Stalker:            stalkerConfigFromResolvedSource(source),
			})
		}
		return targets
	}

	provider := normalizeLiveProvider(resolved.Mode)
	maxStreams := resolved.MaxStreams
	if maxStreams < 0 {
		maxStreams = 0
	}
	bucketKey, bucketName := deriveLiveBucket(provider, resolved)
	return []liveStreamTarget{{
		Provider:           provider,
		MaxStreams:         maxStreams,
		BucketKey:          bucketKey,
		BucketName:         bucketName,
		StreamFormat:       resolved.StreamFormat,
		ProbeSizeMB:        resolved.ProbeSizeMB,
		AnalyzeDurationSec: resolved.AnalyzeDurationSec,
		LowLatency:         resolved.LowLatency,
		ProxyURL:           strings.TrimSpace(resolved.ProxyURL),
		Stalker: stalkerSourceConfig{
			PortalURL: resolved.StalkerPortalURL, MAC: resolved.StalkerMAC,
			SerialNumber: resolved.StalkerSerialNumber, DeviceID: resolved.StalkerDeviceID,
			DeviceID2: resolved.StalkerDeviceID2, Signature: resolved.StalkerSignature,
			Model: resolved.StalkerModel, ProxyURL: resolved.ProxyURL,
		},
	}}
}

func stalkerConfigFromResolvedSource(source resolvedM3USource) stalkerSourceConfig {
	return stalkerSourceConfig{
		PortalURL: source.StalkerPortalURL, MAC: source.StalkerMAC,
		SerialNumber: source.StalkerSerialNumber, DeviceID: source.StalkerDeviceID,
		DeviceID2: source.StalkerDeviceID2, Signature: source.StalkerSignature,
		Model: source.StalkerModel, ProxyURL: source.ProxyURL,
	}
}

func deriveLiveSourceBucket(provider string, source resolvedM3USource) (string, string) {
	resolved := models.ResolvedLiveSource{
		Mode:             source.Mode,
		PlaylistURL:      source.PlaylistURL,
		ManifestURL:      source.ManifestURL,
		XtreamHost:       source.XtreamHost,
		XtreamUsername:   source.XtreamUsername,
		XtreamPassword:   source.XtreamPassword,
		StalkerPortalURL: source.StalkerPortalURL,
		StalkerMAC:       source.StalkerMAC,
	}
	// Stremio sources have no PlaylistURL; carry the manifest URL in the
	// PlaylistURL slot so deriveLiveBucket can derive a stable per-addon bucket.
	if normalizeLiveProvider(source.Mode) == "stremio" {
		resolved.PlaylistURL = source.ManifestURL
		resolved.ManifestURL = source.ManifestURL
	}
	key, label := deriveLiveBucket(provider, resolved)
	name := strings.TrimSpace(source.Name)
	if name != "" {
		label = name
	}
	return key, label
}

func deriveLiveBucket(provider string, src models.ResolvedLiveSource) (string, string) {
	normalizedProvider := normalizeLiveProvider(provider)
	identity := ""
	label := strings.ToUpper(normalizedProvider)

	if normalizedProvider == "xtream" {
		host := normalizeHost(src.XtreamHost)
		user := strings.TrimSpace(strings.ToLower(src.XtreamUsername))
		identity = "xtream|" + host + "|" + user
		if host == "" {
			label = "XTREAM shared"
		} else {
			label = fmt.Sprintf("XTREAM %s", host)
		}
	} else if normalizedProvider == "stremio" {
		manifest := strings.TrimSpace(src.ManifestURL)
		if manifest == "" {
			// Array-source callers carry the manifest URL in PlaylistURL to preserve
			// the existing deriveLiveBucket signature.
			manifest = strings.TrimSpace(src.PlaylistURL)
		}
		host := normalizeHost(manifest)
		identity = "stremio|" + strings.ToLower(manifest)
		if host == "" {
			label = "Stremio shared"
		} else {
			label = fmt.Sprintf("Stremio %s", host)
		}
	} else if normalizedProvider == "stalker" {
		portal := strings.TrimSpace(src.StalkerPortalURL)
		host := normalizeHost(portal)
		identity = "stalker|" + strings.ToLower(portal) + "|" + strings.ToLower(strings.TrimSpace(src.StalkerMAC))
		if host == "" {
			label = "Stalker shared"
		} else {
			label = fmt.Sprintf("Stalker %s", host)
		}
	} else {
		playlist := strings.TrimSpace(src.PlaylistURL)
		if playlist == "" && len(src.PlaylistSources) > 0 {
			playlist = strings.TrimSpace(src.PlaylistSources[0].PlaylistURL)
		}
		host := normalizeHost(playlist)
		identity = "m3u|" + strings.ToLower(playlist)
		if host == "" {
			label = "M3U shared"
		} else {
			label = fmt.Sprintf("M3U %s", host)
		}
	}

	sum := sha1.Sum([]byte(identity))
	bucketID := hex.EncodeToString(sum[:8])
	return normalizedProvider + ":" + bucketID, label
}

func normalizeHost(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	return strings.TrimPrefix(host, "www.")
}
