package remoteaccess

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NewIrohHostManager must derive the persistent secret-key path from the data dir so the
// host keeps a stable node ID across restarts. With no data dir it stays ephemeral.
func TestNewIrohHostManagerSecretFileFromDataDir(t *testing.T) {
	t.Setenv("MEDIASTORM_IROH_SECRET_FILE", "")
	dataDir := t.TempDir()

	m := NewIrohHostManager(t.TempDir(), dataDir, 0)
	want := filepath.Join(dataDir, defaultSecretFileName)
	if m.secretFile != want {
		t.Fatalf("secretFile = %q, want %q", m.secretFile, want)
	}

	ephemeral := NewIrohHostManager(t.TempDir(), "", 0)
	if ephemeral.secretFile != "" {
		t.Fatalf("secretFile = %q, want empty when no data dir", ephemeral.secretFile)
	}
}

// MEDIASTORM_IROH_SECRET_FILE overrides the data-dir-derived default.
func TestNewIrohHostManagerSecretFileEnvOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom_secret.key")
	t.Setenv("MEDIASTORM_IROH_SECRET_FILE", override)

	m := NewIrohHostManager(t.TempDir(), t.TempDir(), 0)
	if m.secretFile != override {
		t.Fatalf("secretFile = %q, want override %q", m.secretFile, override)
	}
}

// The proxy origin must follow the backend's configured Server.Port so operators who
// change the listen port (e.g. 4000) aren't left with the host dialing a dead 7777 → 502.
func TestNewIrohHostManagerOriginFromServerPort(t *testing.T) {
	t.Setenv("MEDIASTORM_IROH_ORIGIN", "")
	t.Setenv("REMOTE_ACCESS_ORIGIN", "")

	m := NewIrohHostManager(t.TempDir(), "", 4000)
	if want := "http://127.0.0.1:4000"; m.origin != want {
		t.Fatalf("origin = %q, want %q", m.origin, want)
	}

	// Port 0 (unknown) falls back to the legacy default.
	fallback := NewIrohHostManager(t.TempDir(), "", 0)
	if fallback.origin != defaultIrohOrigin {
		t.Fatalf("origin = %q, want legacy default %q", fallback.origin, defaultIrohOrigin)
	}
}

// MEDIASTORM_IROH_ORIGIN / REMOTE_ACCESS_ORIGIN override the port-derived default.
func TestNewIrohHostManagerOriginEnvOverridesPort(t *testing.T) {
	t.Setenv("REMOTE_ACCESS_ORIGIN", "")
	t.Setenv("MEDIASTORM_IROH_ORIGIN", "http://backend:9999")

	m := NewIrohHostManager(t.TempDir(), "", 4000)
	if want := "http://backend:9999"; m.origin != want {
		t.Fatalf("origin = %q, want env override %q", m.origin, want)
	}
}

// buildCommand passes --secret-file to the host only when a path is configured.
func TestBuildCommandSecretFileArg(t *testing.T) {
	withSecret := &IrohHostManager{
		workDir:    t.TempDir(),
		bind:       defaultIrohBind,
		origin:     defaultIrohOrigin,
		secretFile: "/data/iroh_host_secret.key",
	}
	cmd := withSecret.buildCommand(context.Background())
	if !hasArgPair(cmd.Args, "--secret-file", "/data/iroh_host_secret.key") {
		t.Fatalf("args missing --secret-file pair: %v", cmd.Args)
	}

	withoutSecret := &IrohHostManager{
		workDir: t.TempDir(),
		bind:    defaultIrohBind,
		origin:  defaultIrohOrigin,
	}
	cmd = withoutSecret.buildCommand(context.Background())
	for _, arg := range cmd.Args {
		if arg == "--secret-file" {
			t.Fatalf("args should omit --secret-file when unset: %v", cmd.Args)
		}
	}
}

// buildCommand must run a prebuilt binary directly (no cargo) when one is dropped straight
// into the work dir — the layout container images use (see backend/Dockerfile iroh stage).
func TestBuildCommandUsesBareWorkDirBinary(t *testing.T) {
	workDir := t.TempDir()
	binary := filepath.Join(workDir, irohBinaryName)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	m := &IrohHostManager{workDir: workDir, bind: defaultIrohBind, origin: defaultIrohOrigin}
	cmd := m.buildCommand(context.Background())
	if cmd.Path != binary {
		t.Fatalf("cmd.Path = %q, want prebuilt binary %q", cmd.Path, binary)
	}
	if cmd.Dir != workDir {
		t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, workDir)
	}
}

// target/release wins over target/debug and the bare path; with no binary at all,
// buildCommand falls back to `cargo run` for local dev.
func TestResolveIrohBinaryPriorityAndFallback(t *testing.T) {
	workDir := t.TempDir()
	if _, ok := resolveIrohBinary(workDir); ok {
		t.Fatalf("expected no binary in empty work dir")
	}

	debug := filepath.Join(workDir, "target", "debug", irohBinaryName)
	if err := os.MkdirAll(filepath.Dir(debug), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(debug, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveIrohBinary(workDir); got != debug {
		t.Fatalf("resolveIrohBinary = %q, want debug %q", got, debug)
	}

	release := filepath.Join(workDir, "target", "release", irohBinaryName)
	if err := os.MkdirAll(filepath.Dir(release), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveIrohBinary(workDir); got != release {
		t.Fatalf("resolveIrohBinary = %q, want release %q (release should win)", got, release)
	}

	cargoFallback := &IrohHostManager{workDir: t.TempDir(), bind: defaultIrohBind, origin: defaultIrohOrigin}
	cmd := cargoFallback.buildCommand(context.Background())
	if filepath.Base(cmd.Args[0]) != "cargo" {
		t.Fatalf("expected cargo fallback when no binary present, got %v", cmd.Args)
	}
}

func hasArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// scanOutput must only treat an explicit "invite=<blob>" line as the invite. A regression
// once stored the publisher's "rendezvous_published code_key=<key> " log line (note the
// trailing space) as the invite, because a bare TrimSpace-difference check fired on it.
func TestScanOutputCapturesOnlyInviteLine(t *testing.T) {
	const wantInvite = "mshost-iroh-abc123"
	output := strings.NewReader(strings.Join([]string{
		"http_speed_host=http://0.0.0.0:0/speed",
		"invite=" + wantInvite,
		"rendezvous_published code_key=8x8a4qamyhm3oko969ans1cut8cfyk8wyy6iarktcnbzrxnbhaco ",
		"rendezvous_publish_error error=boom",
	}, "\n"))

	m := &IrohHostManager{state: "starting"}
	m.scanOutput(output, false)

	if m.invite != wantInvite {
		t.Fatalf("invite = %q, want %q", m.invite, wantInvite)
	}
}

func TestScanOutputIgnoresRendezvousLogWithoutInvite(t *testing.T) {
	output := strings.NewReader(strings.Join([]string{
		"rendezvous_published code_key=8x8a4qamyhm3oko969ans1cut8cfyk8wyy6iarktcnbzrxnbhaco ",
		"some other log line with trailing space ",
	}, "\n"))

	m := &IrohHostManager{state: "starting"}
	m.scanOutput(output, false)

	if m.invite != "" {
		t.Fatalf("invite = %q, want empty (no invite line present)", m.invite)
	}
}
