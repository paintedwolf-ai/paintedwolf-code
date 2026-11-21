package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/closeoutevidence"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// The closeout audit reads the union of the coordinator's ledger and every
// dispatched leg's ledger from the durable stores: a citation only a worker leg
// observed is grounded, and one nobody observed is refused.
func TestSynthesisGroundingInvariants_closeoutCitationAuditUsesCloseoutEvidenceUnion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	sqlDB := testdbfixture.Open(t, "closeout-union.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, root)
	sessions := store.NewSQL(sqlDB)
	parent, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := sessions.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: orchestration.ProfileRepoResearcher, Prompt: "survey"})
	testutil.FailErr(t, "create child", err)
	grepJSON := `{"matches":[{"path":"internal/retention.go","line":1,"content":"package retention"}]}`
	_, _, err = sessions.CommitEvidenceToolResult(ctx, child.ID, root, "grep", map[string]any{"path": ".", "pattern": "retention"}, grepJSON)
	testutil.FailErr(t, "commit leg evidence", err)

	queue := worker.NewSQLQueue(sqlDB, 4)
	_, err = queue.Enqueue(ctx, api.WorkerTask{
		ID: "leg-1", Prompt: "survey", Brief: "survey", ParentSessionID: parent.ID, ChildSessionID: child.ID,
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: root, AgentType: orchestration.ProfileRepoResearcher,
		Status: api.WorkerStatusPending,
	})
	testutil.FailErr(t, "enqueue leg", err)
	ledger := closeoutevidence.New(sessions)
	ledger.SetWorkers(queue)

	audit := func(markdown string) guard.CloseoutGroundingVerdict {
		t.Helper()
		verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(ctx, ledger, parent, nil, "implement_synthesis",
			guidance.ParseCloseoutDraft(markdown, citationmd.Options{}), evidence.CitationRoots{ProjectDir: root})
		testutil.FailErr(t, "EvaluateCoordinatorCloseoutGrounding", err)
		return verdict
	}
	if v := audit(`Retention surveyed [internal/retention.go:1 "package retention"].`); v.Code != "" || v.Grounding == nil || !v.Grounding.Traced {
		t.Fatalf("leg-observed citation verdict = %+v, want traced", v)
	}
	if v := audit(`A phantom path [phantom.go:1 "package phantom"].`); v.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("unobserved citation code = %q, want %q", v.Code, guidance.SynthHandleNotInLegsCode)
	}
}

func TestSynthesisGroundingInvariants_dispatchedLegEvidenceIsObserved(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx := context.Background()
	store := storetest.New(t, testdbseed.DefaultProjectID)
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create parent session", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "path-explorer", Prompt: "survey"})
	testutil.FailErr(t, "CreateChild", err)

	grepJSON := `{"matches":[{"path":"src/a.go","line":1,"content":"package a"}]}`
	_, _, err = store.CommitEvidenceToolResult(ctx, child.ID, root, "grep", map[string]any{"path": ".", "pattern": "package"}, grepJSON)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)

	ev, err := guidance.UnionLegEvidence(ctx, store, []guidance.EvidenceLeg{{ChildSessionID: child.ID}})
	testutil.FailErr(t, "UnionLegEvidence", err)
	if !evidence.PathObserved(ev.Ledger, "src/a.go") {
		t.Fatalf("dispatched leg evidence not observed; paths=%v", evidence.ObservedPathsSorted(ev.Ledger))
	}
}
