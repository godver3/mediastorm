package updates

import "testing"

func TestParseReleaseTag(t *testing.T) {
	version, build := ParseReleaseTag("v1.5.0-20260618")
	if version != "1.5.0" || build != "20260618" {
		t.Fatalf("ParseReleaseTag = %q, %q", version, build)
	}
}

func TestIsNewerVersion(t *testing.T) {
	if !IsNewer("1.5.0", "20260618", "1.6.0", "20260601") {
		t.Fatal("expected newer semantic version to be available")
	}
	if !IsNewer("1.5.0", "20260618", "1.5.0", "20260619") {
		t.Fatal("expected newer build to be available")
	}
	if IsNewer("1.5.0", "20260618", "1.5.0", "20260618") {
		t.Fatal("did not expect same version/build to be available")
	}
	if IsNewer("unknown", "", "1.5.0", "20260618") {
		t.Fatal("did not expect unknown current version to report available")
	}
}
