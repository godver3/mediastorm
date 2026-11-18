package models

import "time"

// Adaptive playback tuning constants. These convert a measured client<->backend
// throughput into "largest file whose average bitrate streams comfortably."
const (
	// adaptiveEpisodeRuntimeSec is the assumed episode runtime used to convert
	// throughput into a per-episode size cap (~45 min).
	adaptiveEpisodeRuntimeSec = 45 * 60
	// adaptiveMovieRuntimeSec is the assumed movie runtime used to convert
	// throughput into a per-movie size cap (~120 min).
	adaptiveMovieRuntimeSec = 120 * 60
	// adaptiveDefaultBufferFactor is the fraction of measured throughput a file's
	// average bitrate may consume and still be considered comfortably streamable.
	adaptiveDefaultBufferFactor = 0.7
	// adaptiveMeasurementMaxAge bounds how long a throughput measurement stays
	// usable before the adaptive size caps are ignored (display caps still apply).
	adaptiveMeasurementMaxAge = 24 * time.Hour
)

// AdaptivePlaybackSettings holds the per-device measurements a client reports for
// adaptive filter caps. The on/off switch and buffer factor live in the global
// FilterSettings (admin UI); this struct carries only what the device can measure
// about itself. The backend converts these into transient filter overrides at
// search time (never persisted into the flat filter fields).
type AdaptivePlaybackSettings struct {
	MeasuredMbps *float64 `json:"measuredMbps,omitempty"` // Tier A client<->backend throughput
	MeasuredAt   *int64   `json:"measuredAt,omitempty"`   // Unix seconds; stale measurements drop size caps
	DisplayHDR   *bool    `json:"displayHdr,omitempty"`   // Display reports HDR10/HLG support
	DisplayDV    *bool    `json:"displayDv,omitempty"`    // Display reports Dolby Vision support
}

// AdaptiveThroughputContext is volatile, route-specific state supplied with a
// search or prequeue request. It is never stored in client settings.
type AdaptiveThroughputContext struct {
	MeasuredMbps float64
	MeasuredAt   int64
}

// AdaptiveSettingsForRequest combines durable display capability with the
// caller's current route estimate. Persisted legacy throughput is ignored.
func AdaptiveSettingsForRequest(display *AdaptivePlaybackSettings, throughput *AdaptiveThroughputContext) *AdaptivePlaybackSettings {
	if display == nil && throughput == nil {
		return nil
	}
	combined := &AdaptivePlaybackSettings{}
	if display != nil {
		combined.DisplayHDR = display.DisplayHDR
		combined.DisplayDV = display.DisplayDV
	}
	if throughput != nil {
		combined.MeasuredMbps = FloatPtr(throughput.MeasuredMbps)
		combined.MeasuredAt = &throughput.MeasuredAt
	}
	return combined
}

// AdaptiveCaps is the result of evaluating AdaptivePlaybackSettings. Each field is
// nil when adaptive should not override that filter value, so callers can overlay
// only what was actually computed.
type AdaptiveCaps struct {
	MaxSizeMovieGB   *float64
	MaxSizeEpisodeGB *float64
	HDRDVPolicy      *HDRDVPolicy
}

// ApplyTo overlays the computed caps onto a FilterSettings, overriding only the
// fields adaptive actually produced.
func (c AdaptiveCaps) ApplyTo(f *FilterSettings) {
	if f == nil {
		return
	}
	// Size caps only ever tighten the configured limit: a fast connection must
	// not lift a stricter user/profile/client size limit (0 = unlimited).
	f.MaxSizeMovieGB = tighterSizeLimit(f.MaxSizeMovieGB, c.MaxSizeMovieGB)
	f.MaxSizeEpisodeGB = tighterSizeLimit(f.MaxSizeEpisodeGB, c.MaxSizeEpisodeGB)
	if c.HDRDVPolicy != nil {
		f.HDRDVPolicy = *c.HDRDVPolicy
	}
}

// tighterSizeLimit returns the stricter of a configured size limit and an
// adaptive cap. A nil or non-positive configured limit means unlimited.
func tighterSizeLimit(configured, adaptive *float64) *float64 {
	if adaptive == nil {
		return configured
	}
	if configured != nil && *configured > 0 && *configured <= *adaptive {
		return configured
	}
	return adaptive
}

// normalizeBufferFactor clamps the configured buffer factor to (0,1], falling back
// to the default when unset or out of range.
func normalizeBufferFactor(f float64) float64 {
	if f <= 0 || f > 1 {
		return adaptiveDefaultBufferFactor
	}
	return f
}

// sizeCapGB converts a runtime (seconds) into a size cap in decimal GB given the
// measured throughput (Mbps) and buffer factor.
//
//	bytes  = mbps * 1e6 / 8 * runtimeSec * factor
//	GB     = bytes / 1e9 = mbps * runtimeSec * factor / 8000
func sizeCapGB(mbps float64, runtimeSec int, factor float64) float64 {
	gb := mbps * float64(runtimeSec) * factor / 8000.0
	// Round to one decimal place for stable, human-readable caps.
	return float64(int(gb*10+0.5)) / 10
}

// ComputeAdaptiveCaps evaluates a client's reported measurements into concrete
// filter overrides. It returns zero-value caps (all nil) when adaptive playback is
// disabled globally or no measurements were reported.
//
// Size caps are produced only when a fresh throughput measurement is available.
// The HDR/DV policy is produced only when the display capability was probed
// (DisplayHDR or DisplayDV non-nil) so platforms without a probe never get their
// HDR content silently filtered out.
func ComputeAdaptiveCaps(enabled bool, bufferFactor float64, a *AdaptivePlaybackSettings, now time.Time) AdaptiveCaps {
	var caps AdaptiveCaps
	if !enabled || a == nil {
		return caps
	}

	// Size caps from throughput, when a fresh measurement exists.
	if a.MeasuredMbps != nil && *a.MeasuredMbps > 0 {
		fresh := true
		if a.MeasuredAt != nil {
			age := now.Sub(time.Unix(*a.MeasuredAt, 0))
			fresh = age >= 0 && age <= adaptiveMeasurementMaxAge
		}
		if fresh {
			factor := normalizeBufferFactor(bufferFactor)
			movie := sizeCapGB(*a.MeasuredMbps, adaptiveMovieRuntimeSec, factor)
			episode := sizeCapGB(*a.MeasuredMbps, adaptiveEpisodeRuntimeSec, factor)
			caps.MaxSizeMovieGB = &movie
			caps.MaxSizeEpisodeGB = &episode
		}
	}

	// HDR/DV policy from display capability, when probed.
	if a.DisplayDV != nil || a.DisplayHDR != nil {
		policy := HDRDVPolicyNoExclusion
		if BoolVal(a.DisplayDV, false) {
			policy = HDRDVPolicyIncludeHDRDV
		} else if BoolVal(a.DisplayHDR, false) {
			policy = HDRDVPolicyIncludeHDR
		}
		caps.HDRDVPolicy = &policy
	}

	return caps
}

// AdaptiveSearchSummary describes the actual adaptive filter used by a search.
type AdaptiveSearchSummary struct {
	Enabled       bool     `json:"enabled"`
	MeasuredMbps  float64  `json:"measuredMbps,omitempty"`
	MeasuredAt    int64    `json:"measuredAt,omitempty"`
	MaxSizeGB     *float64 `json:"maxSizeGB,omitempty"`
	FilteredCount int      `json:"filteredCount"`
	Bypassed      bool     `json:"bypassed,omitempty"`
}
