package handlers

import (
	"os/exec"
	"testing"
)

func TestAccountParentAuthorizationScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	if output, err := exec.Command(node, "--test", "testdata/account_parent_auth.cjs").CombinedOutput(); err != nil {
		t.Fatalf("parent confirmation behavior: %v\n%s", err, output)
	}
}
