package remoteaccess

import (
	"strings"
	"testing"
)

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
