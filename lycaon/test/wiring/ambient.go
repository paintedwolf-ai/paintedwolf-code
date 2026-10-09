package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// AttachDefaultAmbient uses the same ambient workflow as session creation.
func AttachDefaultAmbient(t *testing.T, h *Harness, ctx context.Context, sessionID string) {
	t.Helper()
	if h == nil || h.WorkflowMgr == nil {
		t.Fatal("harness has no workflow manager; ambient attach cannot run")
	}
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = h.WorkflowMgr.Ambient.StartAmbient(ctx, sessionID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
}
