package review_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
)

func TestMissingClaimOutcomeReturnsExactClaimIDsWithoutInventingWork(t *testing.T) {
	mgr, run, manifest := reviewAssignmentFixture(t)
	manifest.PhaseDefs[0].ReviewLoop.VerdictSchema["claims"] = workflowdef.VerdictClaimsType
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	dir := t.TempDir()
	mgr.Verdicts.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.Verdicts.EvidenceProjectDir = func(context.Context, string) (string, error) { return dir, nil }
	record := evidence.GateRecord(evidence.GateTypeSurveyClaims, "candidate", run.ID, evidence.GateVerdictPassed, "SELECTED", map[string]any{"verdict": "SELECTED", "claims": `[{"id":"exact/claim-7","title":"Boundary","statement":"Investigate the boundary","status":"claimed"}]`}, "", "", "", 0, time.Now().UTC())
	testutil.FailErr(t, "record candidate claim", mgr.Verdicts.EvidenceStore.Append(t.Context(), dir, record))
	def := *manifest.PhaseDefs[1].ReviewLoop
	def.FollowupAttempts = 2
	def.VerdictSchema = map[string]string{"verdict": "SELECTED", "claims": workflowdef.VerdictClaimsType, "coverage": workflowdef.VerdictCoverageType}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read question state", err)
	_, err = mgr.Verdicts.Questions.Prepare(t.Context(), run, def, map[string]string{"verdict": "SELECTED", "claims": "[]", "coverage": `{"revision":"x","assessments":[]}`}, vars)
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil || rejected.Code != workflowvalidation.ReviewLoopVerdictInvalidCode || rejected.Data["action"] != "edit_submission" {
		t.Fatalf("wrong correction: %v", err)
	}
	missing, ok := rejected.Data["missing_claim_ids"].([]string)
	if !ok || len(missing) != 1 || missing[0] != "exact/claim-7" || rejected.Data["question_id"] != nil || rejected.Data["work_ids"] != nil {
		t.Fatalf("invented work instead of exact claim repair: %+v", rejected.Data)
	}
}
