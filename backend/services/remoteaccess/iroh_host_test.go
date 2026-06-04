package remoteaccess

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// NewIrohHostManager must derive the persistent secret-key path from the data dir so the
// host keeps a stable node ID across restarts. With no data dir it stays ephemeral.
func TestNewIrohHostManagerSecretFileFromDataDir(t *testing.T) {
	t.Setenv("MEDIASTORM_IROH_SECRET_FILE", "")
	dataDir := t.TempDir()

	m := NewIrohHostManager(t.TempDir(), dataDir)
	want := filepath.Join(dataDir, defaultSecretFileName)
	if m.secretFile != want {
		t.Fatalf("secretFile = %q, want %q", m.secretFile, want)
	}

	ephemeral := NewIrohHostManager(t.TempDir(), "")
	if ephemeral.secretFile != "" {
		t.Fatalf("secretFile = %q, want empty when no data dir", ephemeral.secretFile)
	}
}

// MEDIASTORM_IROH_SECRET_FILE overrides the data-dir-derived default.
func TestNewIrohHostManagerSecretFileEnvOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom_secret.key")
	t.Setenv("MEDIASTORM_IROH_SECRET_FILE", override)

	m := NewIrohHostManager(t.TempDir(), t.TempDir())
	if m.secretFile != override {
		t.Fatalf("secretFile = %q, want override %q", m.secretFile, override)
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
