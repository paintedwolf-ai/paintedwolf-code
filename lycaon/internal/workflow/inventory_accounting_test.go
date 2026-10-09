package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type workflowFakeInventory struct {
	run []api.CodeScan
}

func (f workflowFakeInventory) RunScans(context.Context, string) ([]api.CodeScan, error) {
	return f.run, nil
}

func (f workflowFakeInventory) Scan(_ context.Context, id string) (*api.CodeScan, error) {
	for _, s := range f.run {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, nil
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
	mgr.Coverage.Inventory = workflowFakeInventory{run: scans}
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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start review", err)
	return mgr, run, dir
}

// Every defect in the model-authored candidate is labeled invalid; only a
// structurally valid candidate yields a draft account.
func TestCandidateAccountLabelsCandidateDefects(t *testing.T) {
	mgr, run, _ := startInventoryReview(t)
	manifest, err := mgr.Resolver.ForRun(t.Context(), run)
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
