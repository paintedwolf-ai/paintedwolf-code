package tools_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
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

// isolatedApprovalStore loads bundled defaults without the developer's global overlay.
func isolatedApprovalStore(t *testing.T) *settings.ApprovalStore {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "no-global-approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	return store
}
