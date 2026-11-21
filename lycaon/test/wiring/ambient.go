package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// AttachDefaultAmbient uses the same ambient workflow as session creation.
func AttachDefaultAmbient(t *testing.T, h *Harness, ctx context.Context, sessionID string) {
	t.Helper()
	if h == nil || h.Workflows.Manager == nil {
		t.Fatal("harness has no workflow manager; ambient attach cannot run")
	}
	_, err := h.Workflows.Manager.Ambient.EnsureSessionWorkflow(ctx, sessionID)
	testutil.FailErr(t, "EnsureSessionWorkflow", err)
}
