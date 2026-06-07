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
	// adaptiveSpeedBucketMbps quantizes measured throughput before deriving caps.
	// Speed tests wobble run-to-run; bucketing keeps the resulting caps stable so
	// the prequeue/prewarm cache scope key doesn't thrash on minor fluctuations.
	adaptiveSpeedBucketMbps = 25.0
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
	if c.MaxSizeMovieGB != nil {
		f.MaxSizeMovieGB = c.MaxSizeMovieGB
	}
	if c.MaxSizeEpisodeGB != nil {
		f.MaxSizeEpisodeGB = c.MaxSizeEpisodeGB
	}
	if c.HDRDVPolicy != nil {
		f.HDRDVPolicy = *c.HDRDVPolicy
	}
}

// normalizeBufferFactor clamps the configured buffer factor to (0,1], falling back
// to the default when unset or out of range.
func normalizeBufferFactor(f float64) float64 {
	if f <= 0 || f > 1 {
		return adaptiveDefaultBufferFactor
	}
	return f
}

// bucketSpeedMbps rounds a measured throughput to the nearest adaptiveSpeedBucketMbps,
// with a floor of one bucket so a slow connection never rounds down to "no limit".
func bucketSpeedMbps(mbps float64) float64 {
	bucket := float64(int(mbps/adaptiveSpeedBucketMbps+0.5)) * adaptiveSpeedBucketMbps
	if bucket < adaptiveSpeedBucketMbps {
		bucket = adaptiveSpeedBucketMbps
	}
	return bucket
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
			// Bucket the speed so minor run-to-run wobble doesn't shift the caps
			// (keeps the prequeue/prewarm cache scope stable).
			mbps := bucketSpeedMbps(*a.MeasuredMbps)
			movie := sizeCapGB(mbps, adaptiveMovieRuntimeSec, factor)
			episode := sizeCapGB(mbps, adaptiveEpisodeRuntimeSec, factor)
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
