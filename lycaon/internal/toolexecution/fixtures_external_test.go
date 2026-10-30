package toolexecution_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fixtureToolProfiles(t *testing.T) []sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	return profiles
}

func fixtureSandboxConfig() sandbox.Config {
	return sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}
}

func setSocketReferenceState(t *testing.T) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ns-") //nolint:usetesting // Native Unix reference addresses have a kernel length limit.
	testutil.FailErr(t, "native socket reference state", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
}
