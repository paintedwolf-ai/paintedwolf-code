package delegations

import (
	"os"
	"os/exec"
	"testing"
)

const smallVolumeEnv = "LYCAON_TEST_SMALL_VOLUME"

// smallVolume runs the test again inside a private mount namespace holding a small tmpfs, which
// vanishes with the process, and returns "" once that run has passed. A host that refuses
// unprivileged namespaces cannot run this test.
func smallVolume(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv(smallVolumeEnv); dir != "" {
		return dir
	}
	dir := t.TempDir()
	if err := exec.CommandContext(t.Context(), "unshare", "-Urm", "true").Run(); err != nil {
		t.Skipf("unprivileged mount namespaces unavailable: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary: %v", err)
	}
	script := `mount -t tmpfs -o size=16m none "$1" && shift && exec "$@"`
	cmd := exec.CommandContext(t.Context(), "unshare", "-Urm", "sh", "-c", script, "sh", dir, self, "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), smallVolumeEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	t.Logf("namespace run:\n%s", out)
	if err != nil {
		t.Fatalf("namespace run: %v", err)
	}
	return ""
}
