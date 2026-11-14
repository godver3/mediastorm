package handlers

import (
	"os"
	"os/exec"
	"testing"
)

// This opt-in suite targets a separately started loopback-only demo application.
// Its synthetic fixture and demo guard run before any profile mutation.
func TestFullApplicationBrowser(t *testing.T) {
	base := os.Getenv("MEDIASTORM_FULL_APPLICATION_BASE_URL")
	if base == "" {
		t.Skip("set an isolated loopback demo application and synthetic fixture to run the full client suite")
	}
	python, chrome, credentials := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM"), os.Getenv("MEDIASTORM_FULL_APPLICATION_CREDENTIALS")
	if python == "" || chrome == "" || credentials == "" {
		t.Fatal("full application browser requires interpreter, Chromium and a synthetic fixture credential file")
	}
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	command := exec.CommandContext(t.Context(), python, "testdata/family_application_browser.py", "--base-url", base, "--chromium", chrome, "--credentials-file", credentials, "--artifacts", artifacts)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("full application browser: %v\n%s", err, out)
	}
}

func TestFullApplicationFixtureGuards(t *testing.T) {
	python := os.Getenv("MEDIASTORM_BROWSER_PYTHON")
	if python == "" {
		t.Skip("set the browser interpreter to check application fixture guards")
	}
	// Run optimized too: safety boundaries must not depend on Python assertions.
	for _, optimized := range []bool{false, true} {
		args := []string{"testdata/family_application_guard_test.py"}
		if optimized {
			args = append([]string{"-O"}, args...)
		}
		command := exec.CommandContext(t.Context(), python, args...)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("application fixture guards optimized=%v: %v\n%s", optimized, err, out)
		}
	}
}
