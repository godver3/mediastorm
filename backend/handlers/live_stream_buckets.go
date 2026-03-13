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
	MaxStreams         int
	BucketKey          string
	BucketName         string
	StreamFormat       string
	ProbeSizeMB        int
	AnalyzeDurationSec int
	LowLatency         bool
}

func buildGlobalLiveSource(settings config.Settings) models.ResolvedLiveSource {
	maxStreams := settings.Live.MaxStreams
	if maxStreams < 0 {
		maxStreams = 0
	}
	return models.ResolvedLiveSource{
		Mode:                    settings.Live.Mode,
		PlaylistURL:             settings.Live.PlaylistURL,
		XtreamHost:              settings.Live.XtreamHost,
		XtreamUsername:          settings.Live.XtreamUsername,
		XtreamPassword:          settings.Live.XtreamPassword,
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

func resolveLiveStreamTarget(global models.ResolvedLiveSource, profile *models.UserSettings) liveStreamTarget {
	resolved := global
	if profile != nil {
		resolved = models.ResolveLiveSource(&profile.LiveTV, &global)
	}

	provider := "m3u"
	if strings.EqualFold(strings.TrimSpace(resolved.Mode), "xtream") {
		provider = "xtream"
	}
	maxStreams := resolved.MaxStreams
	if maxStreams < 0 {
		maxStreams = 0
	}
	bucketKey, bucketName := deriveLiveBucket(provider, resolved)
	return liveStreamTarget{
		Provider:           provider,
		MaxStreams:         maxStreams,
		BucketKey:          bucketKey,
		BucketName:         bucketName,
		StreamFormat:       resolved.StreamFormat,
		ProbeSizeMB:        resolved.ProbeSizeMB,
		AnalyzeDurationSec: resolved.AnalyzeDurationSec,
		LowLatency:         resolved.LowLatency,
	}
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
	} else {
		playlist := strings.TrimSpace(src.PlaylistURL)
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
