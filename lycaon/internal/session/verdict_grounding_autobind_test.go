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
	envelope := FormatWorkerCompletionEnvelope(WorkerCompletionEnvelope{
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
	eval, err := mgr.EvaluateVerdictGrounding(ctx, parent.ID, nil, nil, []string{"skeptic"})
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

	eval, err := mgr.EvaluateVerdictGrounding(ctx, parent.ID,
		[]api.CitationGroundingCitedEvidence{{Handle: "invented:read#99"}}, nil, nil)
	testutil.FailErr(t, "evaluate invented citation", err)
	if eval.Code != guidance.VerdictCitationUngroundedCode || eval.Grounding != nil {
		t.Fatalf("evaluation = %+v want ungrounded rejection", eval)
	}
}

func TestBindReviewerVerdictEvidenceUsesEachReviewersLedger(t *testing.T) {
	reviewers := []guidance.ReviewerEvidence{
		{
			Agent:  "skeptic",
			LegIDs: []string{"leg-skeptic"},
			Ledgers: []evidence.Ledger{evidence.AssembleLedger([]evidence.Record{{
				Handle: "read#1", Kind: "read", Path: "auth/session.go",
			}})},
		},
		{
			Agent:  "web-researcher",
			LegIDs: []string{"leg-web"},
			Ledgers: []evidence.Ledger{evidence.AssembleLedger([]evidence.Record{{
				Handle: "web_search#1", Kind: "web_search", URL: "https://example.com/advisory",
			}})},
		},
	}
	union := evidence.AssembleLedger([]evidence.Record{
		{Handle: "leg-skeptic:read#1", Kind: "read", Path: "auth/session.go"},
		{Handle: "leg-web:web_search#1", Kind: "web_search", URL: "https://example.com/advisory"},
	})

	cited, urls, assembled := bindObservedReviewerVerdictEvidence(nil, nil, reviewers, union)
	if !assembled {
		t.Fatal("expected host assembly")
	}
	if len(cited) != 2 || cited[0].Handle != "leg-skeptic:read#1" || cited[1].Handle != "leg-web:web_search#1" {
		t.Fatalf("cited evidence = %+v", cited)
	}
	if len(urls) != 0 {
		t.Fatalf("URLs = %v want handles preferred", urls)
	}
}

func TestBindReviewerVerdictEvidencePreservesExplicitCitation(t *testing.T) {
	explicit := []api.CitationGroundingCitedEvidence{{Handle: "invented:read#99"}}
	reviewers := []guidance.ReviewerEvidence{{
		Agent:  "skeptic",
		LegIDs: []string{"leg-skeptic"},
		Ledgers: []evidence.Ledger{evidence.AssembleLedger([]evidence.Record{{
			Handle: "read#1", Kind: "read", Path: "auth/session.go",
		}})},
	}}

	cited, _, assembled := bindObservedReviewerVerdictEvidence(explicit, nil, reviewers, evidence.InitLedger())
	if !assembled || len(cited) != 2 {
		t.Fatalf("assembled/cited = %v/%+v", assembled, cited)
	}
	if cited[0].Handle != explicit[0].Handle {
		t.Fatalf("explicit citation was rewritten: %+v", cited)
	}
}

func TestBindReviewerVerdictEvidenceFallsBackToCoordinatorLedger(t *testing.T) {
	union := evidence.AssembleLedger([]evidence.Record{{Handle: "read#1", Kind: "read", Path: "main.go"}})
	cited, _, assembled := bindObservedReviewerVerdictEvidence(nil, nil, nil, union)
	if !assembled || len(cited) != 1 || cited[0].Handle != "read#1" {
		t.Fatalf("assembled/cited = %v/%+v", assembled, cited)
	}
}
