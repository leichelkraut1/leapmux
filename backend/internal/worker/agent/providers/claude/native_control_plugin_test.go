package claude

import (
	"os/exec"
	"testing"
)

func TestNativeControlPluginApprovalTurnHeartbeat(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable for the public plugin behavior test")
	}
	command := exec.CommandContext(t.Context(), node, "--test", "testdata/native_control_plugin_test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("public plugin approval regressions: %v\n%s", err, output)
	}
}
