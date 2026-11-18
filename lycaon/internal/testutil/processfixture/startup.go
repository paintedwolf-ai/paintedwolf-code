// Package processfixture runs opt-in startup assertions in a fresh test process.
package processfixture

import (
	"os"
	"os/exec"
	"regexp"
	"testing"
)

const startupCase = "PAINTEDWOLF_TEST_STARTUP_CASE"

// Startup prepares durable state before restarting; only the child returns true.
func Startup(t *testing.T, prepare func()) bool {
	t.Helper()
	if os.Getenv(startupCase) == t.Name() {
		return true
	}
	if prepare != nil {
		prepare()
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1")
	command.Env = append(os.Environ(), startupCase+"="+t.Name())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process startup assertion: %v\n%s", err, output)
	}
	return false
}
