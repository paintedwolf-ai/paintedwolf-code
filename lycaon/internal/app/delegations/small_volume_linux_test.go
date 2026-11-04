package delegations

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

const inMountNamespaceEnv = "LYCAON_TEST_IN_MOUNT_NAMESPACE"

// ranInOwnMountNamespace runs the test again inside a private mount namespace, whose mounts
// vanish with its process, and reports true once that run passed. A host that refuses
// unprivileged namespaces cannot run this test.
func ranInOwnMountNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(inMountNamespaceEnv) != "" {
		return false
	}
	if err := exec.CommandContext(t.Context(), "unshare", "-Urm", "true").Run(); err != nil {
		t.Skipf("unprivileged mount namespaces unavailable: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), "unshare", "-Urm", self, "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), inMountNamespaceEnv+"=1")
	out, err := cmd.CombinedOutput()
	t.Logf("namespace run:\n%s", out)
	if err != nil {
		t.Fatalf("namespace run: %v", err)
	}
	return true
}

func mountSmallVolume(t *testing.T, path string) {
	t.Helper()
	if err := unix.Mount("none", path, "tmpfs", 0, "size=16m"); err != nil {
		t.Fatalf("mount small volume: %v", err)
	}
}
