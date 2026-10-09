package session

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvaluateVerdictGroundingHostAssemblesMissingCitations(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	parent, err := st.Create(ctx, api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create parent", err)
	child, err := st.CreateChild(ctx, parent, api.SpawnChildRequest{
		AgentType: "skeptic", Prompt: "challenge the claims", WorkerJobID: "job-1",
	})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "record reviewer evidence", st.UpsertEvidenceRecord(ctx, child.ID, evidence.Record{
		Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "auth/session.go",
	}))
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID: "job-1", ChildSessionID: child.ID, AgentType: "skeptic", State: "complete",
		Report: workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "Challenged the claims."},
	})
	testutil.FailErr(t, "append worker closeout", st.AppendMessages(ctx, parent.ID,
		api.Message{Role: api.MessageRoleUser, Content: "review this"},
		api.Message{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-1", LegID: "leg-skeptic", ChildSessionID: child.ID,
			AgentType: "skeptic", Status: api.WorkerSummaryStatusComplete, Envelope: envelope,
			Grounding: &api.CitationGrounding{Traced: true, CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
		}},
	))

	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{{
		AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, ID: "job-1", ParentSessionID: parent.ID, ChildSessionID: child.ID, LegID: "leg-skeptic", CreatedAt: time.Now().UTC(),
	}}})
	eval, err := mgr.Closeout.EvaluateVerdictGrounding(ctx, parent.ID, nil, nil, []string{"skeptic"})
	testutil.FailErr(t, "evaluate verdict grounding", err)
	if eval.Code != "" || eval.Grounding == nil {
		t.Fatalf("evaluation = %+v want accepted grounding", eval)
	}
	if !eval.Grounding.HostAssembled || !eval.Grounding.Traced {
		t.Fatalf("grounding = %+v want traced host assembly", eval.Grounding)
	}
	if eval.Grounding.HintCode != guidance.VerdictCitationsRequiredCode {
		t.Fatalf("hint code = %q", eval.Grounding.HintCode)
	}
	if len(eval.Grounding.CitedEvidence) != 1 || eval.Grounding.CitedEvidence[0].Handle != "leg-skeptic:read#1" {
		t.Fatalf("cited evidence = %+v", eval.Grounding.CitedEvidence)
	}
}

func TestEvaluateVerdictGroundingRejectsInventedCitation(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	parent, err := st.Create(ctx, api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create parent", err)
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())

	eval, err := mgr.Closeout.EvaluateVerdictGrounding(ctx, parent.ID,
		[]api.CitationGroundingCitedEvidence{{Handle: "invented:read#99"}}, nil, nil)
	testutil.FailErr(t, "evaluate invented citation", err)
	if eval.Code != guidance.VerdictCitationUngroundedCode || eval.Grounding != nil {
		t.Fatalf("evaluation = %+v want ungrounded rejection", eval)
	}
}
