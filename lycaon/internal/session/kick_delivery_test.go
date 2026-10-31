package session_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// budgetJobs serves fixed worker jobs by id.
type budgetJobs map[string]*api.WorkerTask

func (budgetJobs) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func (budgetJobs) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) { return nil, nil }

func (j budgetJobs) Get(jobID string) (*api.WorkerTask, bool) {
	task, ok := j[jobID]
	return task, ok
}

func (budgetJobs) ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error) {
	return nil, nil
}

func TestTurnDeliversEveryQueuedKickAndDropsAnsweredBudgetRequests(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	st := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(st, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	request := &api.WorkerBudgetRequest{Rounds: 4, RequestedMax: 24, RemainingWork: []string{"trace the alternate callers"}, ToolLoopsUsed: 15, RequestedAt: time.Now().UTC()}
	mgr.SetWorkerQueue(budgetJobs{
		"job-open":     {ID: "job-open", Status: api.WorkerStatusRunning, MaxToolLoops: 20, BudgetRequest: request},
		"job-answered": {ID: "job-answered", Status: api.WorkerStatusRunning, MaxToolLoops: 24},
	})

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Coordinator.Guidance.Emit(ctx, sess.ID, anchor.ComposeDone, anchor.Envelope{})
	for _, job := range []string{"job-answered", "job-open"} {
		facts := kick.WorkerBudgetFacts{JobID: job, Used: 15, Max: 20, HostMax: 120, Request: request}
		mgr.Coordinator.Guidance.Emit(ctx, sess.ID, anchor.WorkerBudgetRequested, anchor.Envelope{Subject: job, WorkerBudget: &facts})
	}
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "prompt", err)
	}
	var delivered []string
	for _, msg := range rec.LastRequest().Messages {
		switch {
		case strings.Contains(msg.Content, "Compose succeeded"):
			delivered = append(delivered, "compose")
		case strings.Contains(msg.Content, "Worker `job-open` asked"):
			delivered = append(delivered, "job-open")
		case strings.Contains(msg.Content, "Worker `job-answered` asked"):
			delivered = append(delivered, "job-answered")
		}
	}
	if strings.Join(delivered, ",") != "compose,job-open" {
		t.Fatalf("delivered kicks = %v, want the older compose kick and job-open's open request", delivered)
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok {
		t.Fatalf("kick %q still queued after the turn", id)
	}
}

func TestObserveImplementerFinishWithoutWrite(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	child := &api.Session{
		ID:              "child-1",
		ParentSessionID: "parent-1",
		AgentType:       orchestration.ProfileImplementer,
		WorkspacePath:   t.TempDir(),
	}
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	// A rejected edit provides no artifact proof.
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "edit", Args: map[string]any{"path": "game.py"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "Rejected: missing path",
			ToolResult: &api.ToolResult{ToolCallID: "tc1", Outcome: api.ToolResultOutcomeRejected},
		},
	}
	gc := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(
		context.Background(),
		child,
		msgs,
		"I attempted to edit game.py.",
		child.WorkspacePath,
		nil,
		gc,
	)
	if !evaluateHasCode(t, gc, "WORKER_IMPLEMENT_NO_ARTIFACT", oar.AnchorWorkerReportCheck) {
		t.Fatal("expected WORKER_IMPLEMENT_NO_ARTIFACT decision")
	}
	formatted, err := rejectFmt.Format("WORKER_IMPLEMENT_NO_ARTIFACT", gc.RejectData["WORKER_IMPLEMENT_NO_ARTIFACT"])
	testutil.FailErr(t, "Format", err)
	if !strings.Contains(formatted, "WORKER_IMPLEMENT_NO_ARTIFACT") {
		t.Fatalf("reject=%q", formatted)
	}

	gc2 := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(
		context.Background(),
		child,
		[]api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "write", Args: map[string]any{"path": "game.py"}},
				},
			},
			{Role: api.MessageRoleTool, Content: "Wrote game.py", ToolResult: &api.ToolResult{ToolCallID: "tc1", Outcome: api.ToolResultOutcomeCompleted}},
		},
		"Created game.py",
		child.WorkspacePath,
		nil,
		gc2,
	)
	if evaluateHasCode(t, gc2, "WORKER_IMPLEMENT_NO_ARTIFACT", oar.AnchorWorkerReportCheck) {
		t.Fatal("write proof should not reject")
	}
}

func TestWorkerParentDeliveryPreservesFrozenDecision(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	database := testdbfixture.Open(t, "worker-feedback.db")
	mem := store.NewSQL(database)
	mgr := session.NewHost(mem, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	parent, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := mem.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	rule := &oar.Rule{OAR: "1.0", ID: "CHANGED_POLICY", Kind: oar.KindPolicy, Anchor: oar.AnchorWorkerReportCheck, Effect: oar.EffectBlock, Enforcement: "enforce", OnError: "fail_closed", OnFire: []oar.OnFireAction{oar.OnFireIncrementCounter}, Copy: oar.Copy{What: "Changed after queue commit"}}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorWorkerReportCheck)
	mgr.SetOARPipeline(pipeline, oar.NewRenderer(nil, nil))
	frozen := api.WorkerResult{Status: "partial", HintCode: "ORIGINAL_POLICY", Summary: "Retained work", PolicyFeedback: &api.WorkerPolicyFeedback{Code: "ORIGINAL_POLICY", Effect: "block", Copy: map[string]string{"what": "Original {{ literal }}", "cause": "Original measured facts", "why": "Preserve observation", "fix": "Inspect retained result", "instead": "Report uncertainty"}}, Grounding: &api.CitationGrounding{Traced: false}}
	data, err := json.Marshal(frozen)
	testutil.FailErr(t, "persist result", err)
	var result api.WorkerResult
	testutil.FailErr(t, "reload result", json.Unmarshal(data, &result))
	status, err := mgr.Workers.Results.ProjectResult(t.Context(), workeroutcomes.SummaryInput{JobID: "job-feedback", ChildSessionID: child.ID, ParentSessionID: parent.ID, AgentType: "implementer"}, result)
	testutil.FailErr(t, "project frozen result", err)
	if status != "partial" {
		t.Fatalf("partial result promoted to %s", status)
	}
	if n := pipeline.Counters().Get(child.ID, rule.Qualified(), oar.CounterFire); n != 0 {
		t.Fatalf("parent generated %d new policy occurrences", n)
	}
	messages, err := mem.GetMessages(t.Context(), parent.ID)
	testutil.FailErr(t, "read parent projection", err)
	found := false
	for _, message := range messages {
		if strings.Contains(message.Content, "Original {{ literal }}") && strings.Contains(message.Content, "ORIGINAL_POLICY") {
			found = true
		}
		if strings.Contains(message.Content, "CHANGED_POLICY") {
			t.Fatal("parent substituted current policy")
		}
	}
	if !found {
		t.Fatalf("frozen feedback missing in parent messages: %+v", messages)
	}
}

func TestAppendWorkerSummaryDoesNotEchoEnvelopeInAssistantProse(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
	enqueueJSON := `{"agent_type":"implementer","job_id":"` + jobID + `","status":"enqueued"}`
	tr := guidance.ComposeToolResult(enqueueJSON, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{WorkerID: jobID, AgentType: "implementer"})
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    enqueueJSON,
		ToolResult: tr,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary:        "done",
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleAssistant || msg.WorkerSummary == nil {
			continue
		}
		if strings.Contains(msg.Content, "<task") {
			t.Fatalf("assistant must not echo worker envelope: %q", msg.Content)
		}
	}
	var finalized bool
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if !strings.Contains(msg.Content, `<task job_id="`+jobID+`"`) {
			t.Fatalf("tool body must carry the completion envelope, got %q", msg.Content)
		}
		if msg.WorkerSummary != nil && strings.Contains(msg.WorkerSummary.Envelope, `<task job_id="`+jobID+`"`) {
			if msg.WorkerSummary.ChildSessionID != child.ID {
				t.Fatalf("worker_summary child_session_id = %q want %q", msg.WorkerSummary.ChildSessionID, child.ID)
			}
			finalized = true
		}
	}
	if !finalized {
		t.Fatal("expected completion envelope on worker_summary")
	}
}

func TestAppendWorkerSummaryProjectsEnvelopeToModel(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "security-reviewer"})
	testutil.FailErr(t, "create child session", err)
	jobID := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	enqueueJSON := `{"agent_type":"security-reviewer","job_id":"` + jobID + `","status":"enqueued"}`
	tr := guidance.ComposeToolResult(enqueueJSON, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{WorkerID: jobID, AgentType: "security-reviewer"})
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    enqueueJSON,
		ToolResult: tr,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	const finding = "urlscan API key is a test fixture, not a live secret"
	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary:        finding,
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "security-reviewer",
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)

	projected := transcript.Project(msgs)
	var body string
	for _, msg := range projected {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, jobID) {
			body = msg.Content
		}
	}
	if body == "" {
		t.Fatal("no projected tool message carried the worker job")
	}
	if !strings.Contains(body, finding) {
		t.Fatalf("projected worker result dropped the summary; body = %q", body)
	}
	if strings.Contains(body, `"status":"enqueued"`) {
		t.Fatalf("projected body still claims the worker is enqueued; body = %q", body)
	}
}
