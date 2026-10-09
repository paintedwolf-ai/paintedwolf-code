package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAppendWorkerSummaryAndTags(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	long := strings.Repeat("x", 5000)
	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary:        long,
		DelegationID:   "dep-1",
		LegID:          "leg-1",
		JobID:          "job-1",
		ChildSessionID: child.ID,
		AgentType:      "implementer",
	}); err != nil {
		t.Fatal(err)
	}
	tags, err := mgr.Workers.Summaries.Tags(ctx, parent.ID)
	testutil.FailErr(t, "mgr.WorkerSummaryTags failed", err)
	if len(tags) != 1 || tags[0].LegID != "leg-1" {
		t.Fatalf("tags = %+v", tags)
	}
}

func TestAppendWorkerSummaryOverBudgetRetainsPolicyFeedback(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mgr.SetOARPipeline(sessionTestOARPipeline(t), oar.NewRenderer(nil, nil))
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "security-reviewer"})
	testutil.FailErr(t, "create child", err)
	max := mgr.Workers.WorkerSummaryFinalizeOpts(ctx, parent).MaxChars
	if max <= 0 {
		t.Fatalf("worker summary max chars = %d", max)
	}
	long := strings.Repeat("x", max+1000)
	if len(long) <= max {
		t.Fatalf("len(long)=%d max=%d", len(long), max)
	}
	status, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Report: workercompletion.WorkerCompletionReport{Brief: long, LegStatus: "complete"},
		JobID:  "job-over", ChildSessionID: child.ID, AgentType: "security-reviewer",
	})
	testutil.FailErr(t, "AppendWorkerSummary", err)
	if status != "partial" {
		t.Fatalf("status = %q want partial from measured budget", status)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 2 || msgs[1].WorkerSummary == nil || msgs[1].WorkerSummary.Status != "partial" {
		t.Fatalf("msgs = %+v want task call/result with partial worker_summary", msgs)
	}
	if !strings.Contains(msgs[1].Content, "<task") {
		t.Fatalf("content = %q want the completion envelope as the model-facing body", msgs[1].Content)
	}
	if msgs[1].WorkerSummary == nil || !strings.Contains(msgs[1].WorkerSummary.Envelope, "<task") {
		t.Fatalf("envelope = %v want completion envelope", msgs[1].WorkerSummary)
	}
	if strings.Contains(msgs[1].WorkerSummary.Envelope, long) {
		t.Fatalf("envelope still contains the uncapped survey")
	}
	if !strings.Contains(msgs[1].WorkerSummary.Envelope, workercloseout.WorkerSummaryTooLongCode) {
		t.Fatalf("envelope = %q must retain %s from the evaluated budget", msgs[1].WorkerSummary.Envelope, workercloseout.WorkerSummaryTooLongCode)
	}
}

func TestAppendWorkerSummaryEmptyStatusPartial(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mgr.SetOARPipeline(sessionTestOARPipeline(t), oar.NewRenderer(nil, nil))
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID: "job-empty", ChildSessionID: child.ID, AgentType: "implementer",
	}); err != nil {
		testutil.FailErr(t, "mgr.AppendWorkerSummary failed", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	if len(msgs) != 2 || msgs[1].WorkerSummary == nil || msgs[1].WorkerSummary.Status != "partial" {
		t.Fatalf("msgs = %+v want task call/result with partial worker_summary", msgs)
	}
	if !strings.Contains(msgs[1].Content, "<task") {
		t.Fatalf("content = %q want the completion envelope as the model-facing body", msgs[1].Content)
	}
	toolEnvelope := ""
	if msgs[1].WorkerSummary != nil {
		toolEnvelope = msgs[1].WorkerSummary.Envelope
	}
	if !strings.Contains(toolEnvelope, `hint_code="WORKER_COMPLETION_REPORT_MISSING"`) &&
		!strings.Contains(toolEnvelope, "WORKER_COMPLETION_REPORT_MISSING") {
		t.Fatalf("tool envelope = %q want WORKER_COMPLETION_REPORT_MISSING", toolEnvelope)
	}
}

func TestAppendWorkerSummaryWriteWorkerOpenStatus(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	dir := t.TempDir()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	jobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent.ID,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		ChildSessionID:  child.ID,
		AgentType:       "implementer",
		Status:          api.WorkerStatusComplete,
		MergeStatus:     api.WorkerMergeStatusPending,
		WorkspaceRoot:   dir,
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py"}},
	})
	testutil.FailErr(t, "EnqueueWithProjectID", err)
	status, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Summary:        "Created game.py",
		Report:         workercompletion.WorkerCompletionReport{Brief: "Created game.py", LegStatus: "complete"},
		Status:         "complete",
	})
	testutil.FailErr(t, "AppendWorkerSummary", err)
	if status != "open" {
		t.Fatalf("status = %q want open", status)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	var summary *api.WorkerSummaryMeta
	for _, msg := range msgs {
		if msg.WorkerSummary != nil {
			summary = msg.WorkerSummary
			break
		}
	}
	if summary == nil {
		t.Fatalf("msgs = %+v want worker_summary", msgs)
	}
	if summary.Status != api.WorkerSummaryStatusOpen {
		t.Fatalf("worker_summary.status = %q", summary.Status)
	}
}

func TestAppendWorkerSummaryUnmetEvidenceKeepsDeliveredWork(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewManager(mem, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-ev")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "game.py"), []byte("print(1)\n"), 0o644))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py"}}
	baseline := testbaseline.Capture(t, primary)
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	jobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:                "fixture",
		Brief:                 "fixture",
		ParentSessionID:       parent.ID,
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		ChildSessionID:        child.ID,
		AgentType:             "implementer",
		Status:                api.WorkerStatusComplete,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "EnqueueWithProjectID", err)
	status, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Summary:        "Created game.py",
		Report:         workercompletion.WorkerCompletionReport{Brief: "Created game.py", LegStatus: "complete"},
		Status:         "complete",
	})
	testutil.FailErr(t, "AppendWorkerSummary", err)
	if status != "open" {
		t.Fatalf("status = %q want open", status)
	}
	msgs, err := mem.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	var summary *api.WorkerSummaryMeta
	for _, msg := range msgs {
		if msg.WorkerSummary != nil {
			summary = msg.WorkerSummary
			break
		}
	}
	if summary == nil {
		t.Fatal("want worker_summary")
	}
	if summary.Status != api.WorkerSummaryStatusOpen {
		t.Fatalf("worker_summary.status = %q want open", summary.Status)
	}
}

func TestAppendWorkerSummaryEligibleOverlayOpens(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewManager(mem, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	ledger := sourceledger.New(testdbfixture.Open(t, "source.db"), t.TempDir())
	t.Cleanup(func() { testutil.FailErr(t, "close source snapshot store", ledger.SnapshotStore().Close()) })
	mgr.SetSourceLedger(ledger)
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-ok")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "game.py"), []byte("print(1)\n"), 0o644))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py"}}
	baseline := testbaseline.Capture(t, primary)
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	jobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:                "fixture",
		Brief:                 "fixture",
		ParentSessionID:       parent.ID,
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		ChildSessionID:        child.ID,
		AgentType:             "implementer",
		Status:                api.WorkerStatusComplete,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "EnqueueWithProjectID", err)
	revision, digest := sourceledger.VerificationState(ctx, ledger, overlay)
	testutil.FailErr(t, "AppendMessages", mem.AppendMessages(ctx, child.ID, api.Message{
		Role: api.MessageRoleTool, WorkerID: jobID,
		ToolResult: &api.ToolResult{
			Content: `{"outcome":"passed"}`,
			Invocation: &api.InvocationReceipt{
				ID: "receipt-verify", Tool: "verify", Status: api.InvocationStatusCompleted,
				Evidence:       api.InvocationEvidence{Kind: "result", Ref: "message-1"},
				SourceRevision: revision, SourceRootDigest: digest,
				SourceVerdict: api.SourceVerdictPassed,
			},
		},
	}))
	status, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Summary:        "Created game.py",
		Report:         workercompletion.WorkerCompletionReport{Brief: "Created game.py", LegStatus: "complete"},
		Status:         "complete",
	})
	testutil.FailErr(t, "AppendWorkerSummary", err)
	if status != "open" {
		t.Fatalf("status = %q want open", status)
	}
}

func TestAppendWorkerSummaryRejectsInvalidState(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewManager(mem, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)

	_, err = mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID: "job-invalid", ChildSessionID: "child-invalid", AgentType: "implementer", Status: "completed",
	})
	if err == nil {
		t.Fatal("invalid worker state accepted")
	}
}

func TestAppendWorkerSummaryParentEnvelopeOmitsHostLedger(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewManager(mem, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "security-reviewer"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "seed child receipt", mem.AppendMessages(ctx, child.ID, api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Invocation: &api.InvocationReceipt{
				ID:     "6bef141c-dd8d-480c-b428-edb5338297fd",
				Tool:   "grep",
				Status: api.InvocationStatusCompleted,
				Evidence: api.InvocationEvidence{
					Kind: "result", Ref: "fb4779ad-d68e-42af-94ca-71702963f597",
				},
				SourceRevision:   "223bb346-4a7d-4325-8023-7855fffc2f54:240",
				SourceRootDigest: "f7945ae9afbd0dfcd5469ff89715fb0e",
			},
		},
	}))
	_, err = mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID:          "job-survey",
		ChildSessionID: child.ID,
		AgentType:      "security-reviewer",
		Summary:        "Surveyed authentication.",
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Surveyed authentication.",
		},
		Status: "complete",
	})
	testutil.FailErr(t, "AppendWorkerSummary", err)
	msgs, err := mem.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	var envelope string
	for _, m := range msgs {
		if m.WorkerSummary != nil {
			envelope = m.WorkerSummary.Envelope
			if envelope == "" {
				envelope = m.Content
			}
			break
		}
	}
	if envelope == "" {
		t.Fatal("missing parent worker envelope")
	}
	for _, banned := range []string{
		"invocation_receipts", "6bef141c", "source_revision", "source_root_digest",
		"<digest>", "<task_result>",
	} {
		if strings.Contains(envelope, banned) {
			t.Fatalf("parent envelope leaked %q: %s", banned, envelope)
		}
	}
	if !strings.Contains(envelope, "<report_json>") || !strings.Contains(envelope, "Surveyed authentication.") {
		t.Fatalf("expected summary + report_json: %s", envelope)
	}
}
