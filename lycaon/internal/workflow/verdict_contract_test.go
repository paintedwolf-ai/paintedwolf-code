package workflow_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/verdictcall"
)

func TestOfferedVerdictAcceptsHostFactIDsAndNestedCitations(t *testing.T) {
	f := reviewLoopFixtures[1]
	loop := reviewLoopDef(t, f)
	schema := offeredVerdictSchema(t, f)
	facts := reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "execute/leg-1"}, {ID: "ingest/scans"}}, Gaps: []reviewcoverage.Fact{{ID: "gap/123"}, {ID: "question/claim-1"}, {ID: "worker/job-1/scope"}}}
	testutil.FailErr(t, "host ids fit offered schema", verdictcall.CheckCoverageIDs(schema, loop, facts))
	call := verdictCall(t, loop, loadVerdictFixture(t, f.fixture))
	verdict := call["verdict"].(map[string]any)
	coverage := verdict["coverage"].(map[string]any)
	coverage["assessments"] = []any{map[string]any{"id": "execute/leg-1", "disposition": "satisfied", "reason": "Observed trace", "cited_evidence": []any{map[string]any{"handle": "read#1"}}}}
	testutil.FailErr(t, "real host id accepted", tools.ValidateToolArgs(schema, call))
	coverage["assessments"].([]any)[0].(map[string]any)["cited_evidence"] = []any{map[string]any{"evidence": "read#1"}}
	if tools.ValidateToolArgs(schema, call) == nil {
		t.Fatal("nested citation bypassed shared schema")
	}
}
