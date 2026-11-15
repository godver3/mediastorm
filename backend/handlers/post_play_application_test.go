package handlers

import (
	"os"
	"os/exec"
	"testing"
)

func TestPostPlayApplicationBrowser(t *testing.T) {
	base := os.Getenv("MEDIASTORM_FULL_APPLICATION_BASE_URL")
	if base == "" {
		t.Skip("isolated loopback demo application and synthetic fixture required")
	}
	python, chrome, credentials := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM"), os.Getenv("MEDIASTORM_FULL_APPLICATION_CREDENTIALS")
	if python == "" || chrome == "" || credentials == "" {
		t.Fatal("browser interpreter, Chromium and private synthetic fixture required")
	}
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	command := exec.CommandContext(t.Context(), python, "testdata/post_play_application_browser.py", "--base-url", base, "--chromium", chrome, "--credentials-file", credentials, "--artifacts", artifacts)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("post-play application browser: %v\n%s", err, out)
	}
}
