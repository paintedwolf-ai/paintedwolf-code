package toolexecution_test

import (
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
