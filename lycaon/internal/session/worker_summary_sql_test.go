package session_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCanceledWorkerProjectionPreservesProofOnRetry(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "worker-cancellation.db")
	s := store.NewSQL(sqlDB)
	mgr := session.NewHost(s, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	parent, err := s.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create cancellation parent", err)
	child, err := s.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create cancellation child", err)
	task := workeroutcomes.SummaryInput{JobID: "job-cancel", ChildSessionID: child.ID, ParentSessionID: parent.ID, AgentType: "implementer"}
	result := api.WorkerResult{Status: "canceled", Response: "canceled", Summary: "Canceled after a bounded edit", ChangeReport: &api.WorkerChangeReport{
		ChangedPaths: []string{"README.md"}, ReceiptCount: 1, MutationTools: []string{"edit"},
	}}
	for attempt := 0; attempt < 2; attempt++ {
		status, err := mgr.Workers.Results.ProjectResult(t.Context(), task, result)
		testutil.FailErr(t, "project cancellation result", err)
		if status != "canceled" {
			t.Fatalf("status=%s", status)
		}
	}
	messages, err := s.GetMessages(t.Context(), parent.ID)
	testutil.FailErr(t, "read cancellation projection", err)
	if len(messages) != 2 || messages[1].WorkerSummary == nil {
		t.Fatalf("expected one call/result pair after retry: %+v", messages)
	}
	meta := messages[1].WorkerSummary
	if meta.Status != api.WorkerSummaryStatusCanceled || meta.ChildSessionID != child.ID {
		t.Fatalf("cancellation identity=%+v", meta)
	}
	envelope, ok := workercompletion.ParseWorkerCompletionEnvelope(meta.Envelope)
	if !ok || envelope.State != "canceled" || envelope.Summary != result.Summary || !reflect.DeepEqual(envelope.Proof.ChangedPaths, result.ChangeReport.ChangedPaths) {
		t.Fatalf("cancellation proof=%+v parsed=%v", envelope, ok)
	}
}

func TestAppendWorkerSummarySQLStoreToolEnvelope(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "worker-summary.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	ctx := context.Background()
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary:        "implemented feature X",
		DelegationID:   "dep-1",
		LegID:          "leg-1",
		JobID:          "job-1",
		ChildSessionID: child.ID,
		AgentType:      "implementer",
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}

	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 2 || msgs[1].Role != api.MessageRoleTool {
		t.Fatalf("msgs = %d want task call/result envelope: %+v", len(msgs), msgs)
	}
	if msgs[1].WorkerSummary == nil || msgs[1].WorkerSummary.WorkerID != "job-1" {
		t.Fatalf("worker_summary = %+v want job-1", msgs[1].WorkerSummary)
	}
	if !strings.Contains(msgs[1].Content, `<task`) {
		t.Fatalf("content = %q want the completion envelope as the model-facing body", msgs[1].Content)
	}
	if msgs[1].WorkerSummary == nil || !strings.Contains(msgs[1].WorkerSummary.Envelope, `<task`) {
		t.Fatalf("tool envelope = %v", msgs[1].WorkerSummary)
	}
}
