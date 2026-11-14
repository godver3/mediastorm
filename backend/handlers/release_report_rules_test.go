package handlers

import (
	"os/exec"
	"testing"
)

func TestWebPreparedReplacementRules(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the JavaScript ownership and handoff tests")
	}
	cmd := exec.CommandContext(t.Context(), node, "--test", "testdata/release_report_rules.test.cjs")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepared replacement rules: %v\n%s", err, output)
	}
}
