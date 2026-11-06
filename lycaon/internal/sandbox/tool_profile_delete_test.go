package sandbox_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestImplementWithShellProfileAllowsDelete(t *testing.T) {
	cfg, err := sandbox.LoadConfig()
	testutil.FailErr(t, "load sandbox config", err)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	b := sandbox.NewBoundary(cfg, profiles)
	if err := b.AssertToolAllowed(context.Background(), "implement", "command", sandbox.ToolAccessProfile); err != nil {
		t.Fatalf("AssertToolAllowed command: %v", err)
	}
	if err := b.AssertToolAllowed(context.Background(), "implement", "restore_version", sandbox.ToolAccessProfile); err != nil {
		t.Fatalf("AssertToolAllowed restore_version: %v", err)
	}
}
