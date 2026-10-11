package runtime_test

import (
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"

	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestFrameLoaderAttachesTheSessionVerdictCall(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	walkPlanRunToReview(t.Context(), t, mgr, blueprintMgr, "sess-1")
	catalog := catalogSubmitVerdictSchema(t)
	var asked string
	loader := &workflowruntime.CoordinatorFrames{Runs: mgr.Store.Runs, Resolver: &mgr.Resolver, Snapshots: mgr.Snapshots, Policy: mgr.Policy, Obligations: mgr.Obligations, VerdictCatalog: func(_ context.Context, sessionID string) map[string]any {
		asked = sessionID
		return catalog
	}}
	frame, err := loader.BuildCoordinatorTurnFrame(t.Context(), "sess-1", nil)
	testutil.FailErr(t, "build frame", err)
	exit := frame.Runtime.PhaseExit
	if asked != "sess-1" || exit == nil || exit.SubmitVerdictArgsSchema == nil || !strings.Contains(exit.VerdictOutline, "verdict: APPROVED|NEEDS_REVISION}") {
		t.Fatalf("asked %q, phase exit = %+v", asked, exit)
	}
	frame, err = (&workflowruntime.CoordinatorFrames{Runs: mgr.Store.Runs, Resolver: &mgr.Resolver, Snapshots: mgr.Snapshots, Policy: mgr.Policy, Obligations: mgr.Obligations}).BuildCoordinatorTurnFrame(t.Context(), "sess-1", nil)
	testutil.FailErr(t, "build frame without catalog", err)
	if frame.Runtime.PhaseExit.SubmitVerdictArgsSchema != nil {
		t.Fatal("a loader without a catalog source composed a phase call")
	}
}
func shippedToolSchemas(t *testing.T) *toolschema.Config {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	return cfg
}
func catalogSubmitVerdictSchema(t *testing.T) map[string]any {
	t.Helper()
	meta, ok := shippedToolSchemas(t).ToolMeta("submit_verdict")
	if !ok {
		t.Fatal("submit_verdict schema missing")
	}
	return meta.ArgsSchema
}
