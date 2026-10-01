package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// startSecuritySurveyRun boots a workflow with two review_loop phases.
func startSecuritySurveyRun(t *testing.T) (*RunManager, string) {
	t.Helper()
	mgr, _, sessions, sqlDB := newRunManagerForEvents(t, "review-evidence-carry.db")
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		OperationID:     "op-" + sess.ID,
		WorkflowID:      "security-survey",
		WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "Start security-survey", err)
	return mgr, sess.ID
}

func stampVerdict(t *testing.T, mgr *RunManager, sessionID, key string, verdict map[string]string) {
	t.Helper()
	ctx := context.Background()
	run, err := mgr.Store.ActiveBySession(ctx, sessionID)
	testutil.FailErr(t, "ActiveBySession", err)
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	testutil.FailErr(t, "UpdateVars",
		mgr.Store.UpdateVars(ctx, run, "", StampReviewVerdict(vars, key, verdict)))
}

// Nothing stamped is the normal state of an early-phase leg.
func TestStampedReviewVerdictsIsEmptyBeforeAnyVerdict(t *testing.T) {
	mgr, sessionID := startSecuritySurveyRun(t)
	if got := mgr.StampedReviewVerdicts(context.Background(), sessionID); len(got) != 0 {
		t.Fatalf("got %+v, want none stamped yet", got)
	}
}

// The claims phase records the object later reviewers are briefed against.
func TestStampedReviewVerdictsCarriesTheRecordedClaims(t *testing.T) {
	mgr, sessionID := startSecuritySurveyRun(t)
	stampVerdict(t, mgr, sessionID, "survey_claims", map[string]string{
		"verdict":      "CLAIMED",
		"threat_model": "unauthenticated MCP surface",
		"claims":       `[{"id":"C1","statement":"loader imports arbitrary modules"}]`,
	})

	got := mgr.StampedReviewVerdicts(context.Background(), sessionID)
	if len(got) != 1 {
		t.Fatalf("got %d verdicts, want 1: %+v", len(got), got)
	}
	if got[0].EvidenceKey != "survey_claims" || got[0].Phase != "claims" {
		t.Fatalf("verdict = %+v", got[0])
	}
	if len(got[0].Fields) != 3 {
		t.Fatalf("fields = %+v", got[0].Fields)
	}
	if got[0].Fields[0].Name != "verdict" || got[0].Fields[0].Value != "CLAIMED" {
		t.Fatalf("the terminal decision must render first, got %+v", got[0].Fields)
	}
	// Remaining members are alphabetical, so one record reads one way.
	if got[0].Fields[1].Name != "claims" || got[0].Fields[2].Name != "threat_model" {
		t.Fatalf("fields not in deterministic order: %+v", got[0].Fields)
	}
}

// Verdicts follow manifest phase order, not map or write order.
func TestStampedReviewVerdictsFollowPhaseOrder(t *testing.T) {
	mgr, sessionID := startSecuritySurveyRun(t)
	stampVerdict(t, mgr, sessionID, "survey_challenged", map[string]string{"verdict": "CHALLENGED"})
	stampVerdict(t, mgr, sessionID, "survey_claims", map[string]string{"verdict": "CLAIMED"})

	got := mgr.StampedReviewVerdicts(context.Background(), sessionID)
	if len(got) != 2 {
		t.Fatalf("got %d verdicts, want 2", len(got))
	}
	if got[0].EvidenceKey != "survey_claims" || got[1].EvidenceKey != "survey_challenged" {
		t.Fatalf("phase order not preserved: %+v", got)
	}
}

// A session with no run carries nothing rather than failing the dispatch.
func TestStampedReviewVerdictsIsInertWithoutARun(t *testing.T) {
	mgr, _, sessions, sqlDB := newRunManagerForEvents(t, "review-evidence-carry-inert.db")
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if got := mgr.StampedReviewVerdicts(ctx, sess.ID); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
	if got := (*RunManager)(nil).StampedReviewVerdicts(ctx, sess.ID); got != nil {
		t.Fatalf("nil manager must be inert, got %+v", got)
	}
}
