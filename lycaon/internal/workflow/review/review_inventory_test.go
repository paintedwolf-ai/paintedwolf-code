package review_test

import (
	"encoding/json"
	"errors"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func startInventoryReview(t *testing.T) (*workflow.RunManager, *api.WorkflowRun, string) {
	t.Helper()
	mgr, _, blueprints, dir := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	manifest := reviewLoopTestManifest()
	manifest.PhaseDefs[0].ActivityLabel = "Reviewing inventory"
	manifest.PhaseDefs[1].ActivityLabel = "Done"
	def := manifest.PhaseDefs[0].ReviewLoop
	def.RequireInventoryAccounted = true
	def.VerdictSchema = map[string]string{"verdict": "SELECTED", "claims": "claims", "set_asides": "set_asides"}
	def.ClaimStatuses = map[string]workflowdef.ClaimClass{"held": workflowdef.ClaimHeld}
	raw, err := workflowdef.MarshalManifestYAML(manifest)
	testutil.FailErr(t, "marshal inventory policy", err)
	manifest, err = workflowdef.ParseManifestYAML([]byte(raw))
	testutil.FailErr(t, "parse inventory policy", err)
	if !manifest.PhaseDefs[0].ReviewLoop.RequireInventoryAccounted {
		t.Fatal("inventory requirement lost during manifest round trip")
	}
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start review", err)
	return mgr, run, dir
}

func TestReviewInventoryRefusalDoesNotAdvanceOrConsumeReviewRound(t *testing.T) {
	mgr, run, dir := startInventoryReview(t)
	mgr.Coverage.Inventory = fakeInventory{run: []api.CodeScan{{ID: "scan", Status: api.CodeScanStatusComplete,
		ScannerID: "secrets", Findings: []api.SecurityFinding{secretFinding("src/a.go")},
	}}}
	reg := catalogRegistry(t)
	testutil.FailErr(t, "register verdict", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	verdict := map[string]any{"verdict": "SELECTED", "claims": []any{}, "set_asides": []any{}}
	_, err := reg.Run(t.Context(), "submit_verdict", map[string]any{"verdict": verdict}, toolContext("coordinator", "sess-1", dir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != workflowreview.SubmitVerdictInventoryUnaccountedCode {
		t.Fatalf("unaccounted verdict = %v, want inventory rejection", err)
	}
	after, err := mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read run after refusal", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read review attempts", err)
	if after.CurrentPhase != "judge" || runstate.ReviewLoopAttempt(vars, "judge") != 0 {
		t.Fatalf("refusal advanced or spent a round: phase=%s vars=%v", after.CurrentPhase, vars)
	}
	inventory, err := workflowreview.LoadRunInventory(t.Context(), mgr.Coverage.Inventory, run.ID)
	testutil.FailErr(t, "load groups", err)
	verdict["claims"] = []any{map[string]any{"id": "c1", "title": "Assessed", "statement": "Observed evidence", "status": "held", "scan_group_ids": []string{inventory.Groups[0].ID}}}
	body, err := reg.Run(t.Context(), "submit_verdict", map[string]any{"verdict": verdict}, toolContext("coordinator", "sess-1", dir))
	testutil.FailErr(t, "submit accounted verdict", err)
	var result workflowreview.SubmitVerdictToolResult
	testutil.FailErr(t, "decode accepted verdict", json.Unmarshal([]byte(body), &result))
	if !result.Terminal {
		t.Fatalf("accounted verdict did not settle: %+v", result)
	}
}

func TestReviewInventoryWaitsForScansAndAcceptsEmptyInventory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scans    []api.CodeScan
		terminal bool
	}{
		{name: "running scan", scans: []api.CodeScan{{ID: "running", Status: api.CodeScanStatusRunning}}},
		{name: "clean scan", scans: []api.CodeScan{{ID: "clean", Status: api.CodeScanStatusComplete}}, terminal: true},
		{name: "no scans", terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _, _ := startInventoryReview(t)
			mgr.Coverage.Inventory = fakeInventory{run: tc.scans}
			out, err := mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{
				"verdict": "SELECTED", "claims": "[]", "set_asides": "[]",
			}, nil, nil)
			testutil.FailErr(t, "record inventory verdict", err)
			if out.Terminal != tc.terminal || (out.InventoryIssue == nil) != tc.terminal {
				t.Fatalf("outcome = %+v, want terminal=%v", out, tc.terminal)
			}
		})
	}
}
