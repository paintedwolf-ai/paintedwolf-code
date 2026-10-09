package workflow

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func startInventoryReview(t *testing.T) (*RunManager, *api.WorkflowRun, string) {
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
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start review", err)
	return mgr, run, dir
}

func TestReviewInventoryRefusalDoesNotAdvanceOrConsumeReviewRound(t *testing.T) {
	mgr, run, dir := startInventoryReview(t)
	mgr.Inventory = fakeInventory{run: []api.CodeScan{{ID: "scan", Status: api.CodeScanStatusComplete,
		ScannerID: "secrets", Findings: []api.SecurityFinding{secretFinding("src/a.go")},
	}}}
	reg := catalogRegistry(t)
	testutil.FailErr(t, "register verdict", RegisterSubmitVerdictTool(reg, mgr))
	verdict := map[string]any{"verdict": "SELECTED", "claims": []any{}, "set_asides": []any{}}
	_, err := reg.Run(t.Context(), "submit_verdict", map[string]any{"verdict": verdict}, toolContext("coordinator", "sess-1", dir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != SubmitVerdictInventoryUnaccountedCode {
		t.Fatalf("unaccounted verdict = %v, want inventory rejection", err)
	}
	after, err := mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read run after refusal", err)
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read review attempts", err)
	if after.CurrentPhase != "judge" || ReviewLoopAttempt(vars, "judge") != 0 {
		t.Fatalf("refusal advanced or spent a round: phase=%s vars=%v", after.CurrentPhase, vars)
	}
	inventory, err := LoadRunInventory(t.Context(), mgr.Inventory, run.ID)
	testutil.FailErr(t, "load groups", err)
	verdict["claims"] = []any{map[string]any{"id": "c1", "title": "Assessed", "statement": "Observed evidence", "status": "held", "scan_group_ids": []string{inventory.Groups[0].ID}}}
	body, err := reg.Run(t.Context(), "submit_verdict", map[string]any{"verdict": verdict}, toolContext("coordinator", "sess-1", dir))
	testutil.FailErr(t, "submit accounted verdict", err)
	var result SubmitVerdictToolResult
	testutil.FailErr(t, "decode accepted verdict", json.Unmarshal([]byte(body), &result))
	if !result.Terminal {
		t.Fatalf("accounted verdict did not settle: %+v", result)
	}
}

func TestReviewInventoryUsesInheritedLinksAndSetAsides(t *testing.T) {
	claims, challenge := surveyPhases()
	claims.VerdictSchema["set_asides"] = "set_asides"
	challenge.VerdictSchema["set_asides"] = "set_asides"
	prior := phaseVerdict("claims", claims, "claims", `[{"id":"c1","title":"Assessed","statement":"s","status":"claimed","scan_group_ids":["group:linked"]}]`)
	prior.Record.Artifacts["set_asides"] = `[{"scanner":"secrets","paths":["**/*_test.go"],"reason":"fixtures"}]`
	current := phaseVerdict("challenge", challenge, "challenges", `[{"id":"c1","statement":"s","status":"survives"}]`)
	current.Record.Artifacts["set_asides"] = `[{"scan_group_ids":["group:mixed","group:locationless"],"reason":"reviewed samples"}]`
	phases := []PhaseVerdict{prior, current}
	facts := ReportDocumentFacts{
		Claims: ReconcileClaims(phases), SetAsides: RunSetAsides(phases),
		Inventory: RunInventory{Settled: true, Groups: []scanfindings.InventoryGroup{
			{ID: "group:linked", Paths: []string{"src/a.go"}},
			{ID: "group:fixture", Scanner: "secrets", Paths: []string{"src/a_test.go"}},
			{ID: "group:mixed", Scanner: "secrets", Paths: []string{"src/a_test.go", "src/sample.go"}},
			{ID: "group:locationless", Scanner: "secrets"},
		}},
	}
	if issue := checkInventoryAccounted(validDocument(), facts); issue.Code != "" {
		t.Fatalf("inherited accounting was lost: %+v", issue)
	}
	facts.SetAsides = RunSetAsides(phases[:1])
	if issue := checkInventoryAccounted(validDocument(), facts); issue.Count != 2 {
		t.Fatalf("path globs cleared mixed or locationless groups: %+v", issue)
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
			mgr.Inventory = fakeInventory{run: tc.scans}
			out, err := mgr.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{
				"verdict": "SELECTED", "claims": "[]", "set_asides": "[]",
			}, nil, nil)
			testutil.FailErr(t, "record inventory verdict", err)
			if out.Terminal != tc.terminal || (out.InventoryIssue == nil) != tc.terminal {
				t.Fatalf("outcome = %+v, want terminal=%v", out, tc.terminal)
			}
		})
	}
}

func TestInventoryPreviewChecksEveryLocation(t *testing.T) {
	selector := scanfindings.SetAside{Scanner: "sast", Paths: []string{"**/*_test.go"}}
	cases := []struct {
		name            string
		paths           []string
		selected, mixed bool
	}{
		{"fixture", []string{"internal/a_test.go"}, true, false},
		{"mixed", []string{"internal/a_test.go", "internal/live.go"}, false, true},
		{"locationless", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			group := scanfindings.InventoryGroup{Scanner: "sast", Paths: tc.paths}
			if selector.Selects(group) != tc.selected || selectorPartlyMatches(selector, group) != tc.mixed {
				t.Fatal("selector preview misclassified complete location set")
			}
		})
	}
}

func TestInventoryAccountingRequiresBoundScansAndPreservesRevision(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	scans := []api.CodeScan{{ID: "scan", ScannerID: "sast", Status: api.CodeScanStatusComplete, Findings: []api.SecurityFinding{scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "sast", RuleID: "rule", Level: api.FindingLevelHigh})}}}
	mgr.Inventory = fakeInventory{run: scans}
	raw, err := InventoryAccounting{mgr}.QueryWorkflowInventory(t.Context(), run.SessionID, map[string]any{"scan_ids": []string{"scan"}, "view": "accounting"})
	testutil.FailErr(t, "query accounting", err)
	var got struct {
		Revision    string `json:"inventory_revision"`
		Unaccounted int    `json:"unaccounted_groups"`
		Candidate   string `json:"candidate_state"`
	}
	testutil.FailErr(t, "decode accounting", json.Unmarshal([]byte(raw), &got))
	revision, err := scanfindings.InventoryRevision(scans)
	testutil.FailErr(t, "inventory revision", err)
	if got.Revision != revision || got.Unaccounted != 1 || got.Candidate != "absent" {
		t.Fatalf("incorrect accounting: %s", raw)
	}
	for _, args := range []map[string]any{
		{"scan_ids": []any{"unbound"}},
		{"scan_ids": []any{"scan"}, "inventory_revision": "stale"},
		{"scan_ids": []any{"scan"}, "path": "subset.go"},
	} {
		if _, err := (InventoryAccounting{mgr}).QueryWorkflowInventory(t.Context(), run.SessionID, args); err == nil {
			t.Fatalf("accepted invalid inventory query: %+v", args)
		}
	}
}

// Every defect in the model-authored candidate is labeled invalid; only a
// structurally valid candidate yields a draft account.
func TestCandidateAccountLabelsCandidateDefects(t *testing.T) {
	mgr, run, _ := startInventoryReview(t)
	manifest, err := mgr.manifestForRun(t.Context(), run)
	testutil.FailErr(t, "manifest", err)
	phase, _ := manifest.PhaseByID(run.CurrentPhase)
	groups := []scanfindings.InventoryGroup{{ID: "group:a", Scanner: "secrets", Paths: []string{"src/a.go"}}}
	candidate := func(groupID string, setAsides []any) map[string]any {
		claim := map[string]any{"id": "c1", "title": "Assessed", "statement": "Observed evidence", "status": "held", "scan_group_ids": []any{groupID}}
		return map[string]any{"verdict": map[string]any{"verdict": "SELECTED", "claims": []any{claim}, "set_asides": setAsides}}
	}
	accounting := InventoryAccounting{mgr}
	account, err := accounting.candidateAccount(t.Context(), run, *phase.ReviewLoop, candidate("group:a", []any{}), groups)
	testutil.FailErr(t, "valid candidate", err)
	if !account.Linked["group:a"] {
		t.Fatalf("valid candidate did not link its group: %+v", account)
	}
	for name, args := range map[string]map[string]any{
		"malformed arguments": {"verdict": "not an object"},
		"undeclared decision": {"verdict": map[string]any{"verdict": "UNDECLARED", "claims": []any{}, "set_asides": []any{}}},
		"reasonless set-aside": candidate("group:a", []any{map[string]any{"scan_group_ids": []any{"group:a"}}}),
		"unknown scan group":  candidate("group:missing", []any{}),
	} {
		if _, err := accounting.candidateAccount(t.Context(), run, *phase.ReviewLoop, args, groups); !errors.Is(err, errCandidateInvalid) {
			t.Errorf("%s: err = %v, want candidate defect", name, err)
		}
	}
}
