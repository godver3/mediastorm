package models

import (
	"testing"
	"time"
)

func TestComputeAdaptiveCaps_Disabled(t *testing.T) {
	now := time.Now()

	// Disabled globally yields empty caps even with measurements present.
	a := &AdaptivePlaybackSettings{MeasuredMbps: FloatPtr(100), DisplayDV: BoolPtr(true)}
	if caps := ComputeAdaptiveCaps(false, 0.7, a, now); caps != (AdaptiveCaps{}) {
		t.Fatalf("disabled should yield empty caps, got %+v", caps)
	}

	// Enabled but no measurements yields empty caps.
	if caps := ComputeAdaptiveCaps(true, 0.7, nil, now); caps != (AdaptiveCaps{}) {
		t.Fatalf("nil measurements should yield empty caps, got %+v", caps)
	}
}

func TestComputeAdaptiveCaps_SizeCaps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := &AdaptivePlaybackSettings{
		MeasuredMbps: FloatPtr(100),
		MeasuredAt:   Int64Ptr(now.Unix()),
	}

	caps := ComputeAdaptiveCaps(true, 0.7, a, now)

	// movie: 100 * 7200 * 0.7 / 8000 = 63.0
	if caps.MaxSizeMovieGB == nil || *caps.MaxSizeMovieGB != 63.0 {
		t.Fatalf("movie cap = %v, want 63.0", caps.MaxSizeMovieGB)
	}
	// episode: 100 * 2700 * 0.7 / 8000 = 23.625 -> 23.6
	if caps.MaxSizeEpisodeGB == nil || *caps.MaxSizeEpisodeGB != 23.6 {
		t.Fatalf("episode cap = %v, want 23.6", caps.MaxSizeEpisodeGB)
	}
	// No display info probed -> no HDR policy override.
	if caps.HDRDVPolicy != nil {
		t.Fatalf("HDR policy should be nil without display probe, got %v", *caps.HDRDVPolicy)
	}
}

func TestComputeAdaptiveCaps_StaleMeasurementDropsSizeCaps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	stale := now.Add(-25 * time.Hour).Unix()
	a := &AdaptivePlaybackSettings{
		MeasuredMbps: FloatPtr(100),
		MeasuredAt:   Int64Ptr(stale),
		DisplayHDR:   BoolPtr(true),
	}

	caps := ComputeAdaptiveCaps(true, 0.7, a, now)
	if caps.MaxSizeMovieGB != nil || caps.MaxSizeEpisodeGB != nil {
		t.Fatalf("stale measurement should drop size caps, got %+v", caps)
	}
	// Display caps still apply regardless of measurement age.
	if caps.HDRDVPolicy == nil || *caps.HDRDVPolicy != HDRDVPolicyIncludeHDR {
		t.Fatalf("HDR policy = %v, want %q", caps.HDRDVPolicy, HDRDVPolicyIncludeHDR)
	}
}

func TestComputeAdaptiveCaps_HDRDVPolicyMapping(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		hdr  *bool
		dv   *bool
		want *HDRDVPolicy
	}{
		{"dv wins", BoolPtr(true), BoolPtr(true), policyPtr(HDRDVPolicyIncludeHDRDV)},
		{"hdr only", BoolPtr(true), BoolPtr(false), policyPtr(HDRDVPolicyIncludeHDR)},
		{"sdr", BoolPtr(false), BoolPtr(false), policyPtr(HDRDVPolicyNoExclusion)},
		{"unprobed", nil, nil, nil},
		{"dv only nil hdr", nil, BoolPtr(true), policyPtr(HDRDVPolicyIncludeHDRDV)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &AdaptivePlaybackSettings{DisplayHDR: tc.hdr, DisplayDV: tc.dv}
			caps := ComputeAdaptiveCaps(true, 0.7, a, now)
			switch {
			case tc.want == nil && caps.HDRDVPolicy != nil:
				t.Fatalf("expected nil policy, got %v", *caps.HDRDVPolicy)
			case tc.want != nil && caps.HDRDVPolicy == nil:
				t.Fatalf("expected %v, got nil", *tc.want)
			case tc.want != nil && *caps.HDRDVPolicy != *tc.want:
				t.Fatalf("policy = %v, want %v", *caps.HDRDVPolicy, *tc.want)
			}
		})
	}
}

func TestComputeAdaptiveCaps_BufferFactorClamp(t *testing.T) {
	now := time.Now()
	a := &AdaptivePlaybackSettings{MeasuredMbps: FloatPtr(100)}

	// Out-of-range factor falls back to default (0.7): 100*7200*0.7/8000 = 63.0.
	caps := ComputeAdaptiveCaps(true, 5, a, now)
	if caps.MaxSizeMovieGB == nil || *caps.MaxSizeMovieGB != 63.0 {
		t.Fatalf("movie cap with clamped factor = %v, want 63.0", caps.MaxSizeMovieGB)
	}

	// Zero factor also falls back to default.
	caps = ComputeAdaptiveCaps(true, 0, a, now)
	if caps.MaxSizeMovieGB == nil || *caps.MaxSizeMovieGB != 63.0 {
		t.Fatalf("movie cap with zero factor = %v, want 63.0", caps.MaxSizeMovieGB)
	}

	// Valid custom factor applies: 100*7200*0.5/8000 = 45.0.
	caps = ComputeAdaptiveCaps(true, 0.5, a, now)
	if caps.MaxSizeMovieGB == nil || *caps.MaxSizeMovieGB != 45.0 {
		t.Fatalf("movie cap with factor 0.5 = %v, want 45.0", caps.MaxSizeMovieGB)
	}
}

func TestComputeAdaptiveCaps_SpeedBucketingIsStable(t *testing.T) {
	now := time.Now()
	// Speeds that wobble within the same 25 Mbps bucket (37.5–62.5 -> 50) must
	// produce identical caps so the prequeue/prewarm scope key stays stable.
	want := ComputeAdaptiveCaps(true, 0.7, &AdaptivePlaybackSettings{MeasuredMbps: FloatPtr(50)}, now)
	for _, mbps := range []float64{50.3, 51.1, 48.0, 56.0, 44.5} {
		got := ComputeAdaptiveCaps(true, 0.7, &AdaptivePlaybackSettings{MeasuredMbps: FloatPtr(mbps)}, now)
		if got.MaxSizeMovieGB == nil || want.MaxSizeMovieGB == nil || *got.MaxSizeMovieGB != *want.MaxSizeMovieGB {
			t.Fatalf("mbps %v: movie cap %v, want stable %v", mbps, got.MaxSizeMovieGB, want.MaxSizeMovieGB)
		}
	}

	// A slow connection must still get a real (non-zero) cap, not "no limit".
	slow := ComputeAdaptiveCaps(true, 0.7, &AdaptivePlaybackSettings{MeasuredMbps: FloatPtr(3)}, now)
	if slow.MaxSizeMovieGB == nil || *slow.MaxSizeMovieGB <= 0 {
		t.Fatalf("slow link should still get a positive cap, got %v", slow.MaxSizeMovieGB)
	}
}

func TestAdaptiveCaps_ApplyTo(t *testing.T) {
	movie := 10.0
	policy := HDRDVPolicyIncludeHDR
	caps := AdaptiveCaps{MaxSizeMovieGB: &movie, HDRDVPolicy: &policy}

	f := FilterSettings{
		MaxSizeMovieGB:   FloatPtr(99),
		MaxSizeEpisodeGB: FloatPtr(5),
		HDRDVPolicy:      HDRDVPolicyNoExclusion,
	}
	caps.ApplyTo(&f)

	if *f.MaxSizeMovieGB != 10.0 {
		t.Fatalf("movie not overridden: %v", *f.MaxSizeMovieGB)
	}
	// Episode untouched because caps did not produce it.
	if *f.MaxSizeEpisodeGB != 5 {
		t.Fatalf("episode should be untouched: %v", *f.MaxSizeEpisodeGB)
	}
	if f.HDRDVPolicy != HDRDVPolicyIncludeHDR {
		t.Fatalf("policy not overridden: %v", f.HDRDVPolicy)
	}
}

func policyPtr(p HDRDVPolicy) *HDRDVPolicy { return &p }
