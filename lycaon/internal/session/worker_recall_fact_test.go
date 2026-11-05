package session_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func renderViewRows(id string) []api.Message {
	return []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: id, Name: "render_view", Args: map[string]any{"markup": "<div/>"}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"mime":"image/png"}`}},
	}
}

func surfaceClaimReport(handle string) api.Message {
	return completeLegToolRow(map[string]any{
		"leg_status": "complete",
		"findings":   []any{map[string]any{"evidence": handle, "note": "The dialog renders centered"}},
		"brief":      "The dialog renders centered",
	})
}

// The worker repair names recall only when the worker's latest turn could call it.
func TestWorkerGroundingRepairRecallFollowsOfferedTools(t *testing.T) {
	cases := []struct {
		name         string
		workerTurn   []string
		retryTurn    []string
		wantRecall   []bool
		wantAttempts int
	}{
		{name: "offered on the worker turn", workerTurn: []string{"read", "recall", "complete_leg"}, retryTurn: []string{"read", "recall", "complete_leg"}, wantRecall: []bool{true, true}, wantAttempts: 2},
		{name: "absent after restart, offered on the retry turn", workerTurn: nil, retryTurn: []string{"recall", "complete_leg"}, wantRecall: []bool{false, true}, wantAttempts: 2},
		{name: "never offered", workerTurn: []string{"read", "complete_leg"}, retryTurn: []string{"read", "complete_leg"}, wantRecall: []bool{false, false}, wantAttempts: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := loadWorkerSummaryTestFinalizeOpts(t)
			opts.ProjectDir = t.TempDir()
			opts.AgentType = "path-explorer"
			opts.MaxGroundingRetries = 2
			opts.OfferedTools = tc.workerTurn
			opts.RenderWorkerKick = prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}).RenderKick

			resolver := &stubSummaryResolver{msgs: append(renderViewRows("rv1"), surfaceClaimReport("render#1"))}
			var retries []string
			resolver.promptFunc = func(_ context.Context, _ string, text string) (*promptresult.Result, error) {
				retries = append(retries, text)
				// Each retry renders again, so the offender set changes and stuck detection keeps retrying.
				resolver.msgs = append(resolver.msgs, renderViewRows(fmt.Sprintf("rv%d", len(retries)+1))...)
				resolver.msgs = append(resolver.msgs, surfaceClaimReport(fmt.Sprintf("render#%d", len(retries)+1)))
				resolver.stampEvidenceHandles()
				return &promptresult.Result{MessageID: "retry", OfferedTools: tc.retryTurn}, nil
			}

			_, err := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
			testutil.FailErr(t, "finalize worker summary", err)
			if len(retries) != tc.wantAttempts {
				t.Fatalf("retries = %d want %d:\n%s", len(retries), tc.wantAttempts, strings.Join(retries, "\n---\n"))
			}
			for i, text := range retries {
				if !strings.Contains(text, "Code: SURFACE_CLAIM_UNGROUNDED") {
					t.Fatalf("retry %d is not the surface claim repair:\n%s", i+1, text)
				}
				header, reject, ok := strings.Cut(text, "\n\nRejected: ")
				if !ok {
					t.Fatalf("retry %d has no rejection after the kick:\n%s", i+1, text)
				}
				if got := strings.Contains(header, `recall(query="handle:`); got != tc.wantRecall[i] {
					t.Errorf("retry %d kick names recall = %v want %v:\n%s", i+1, got, tc.wantRecall[i], header)
				}
				if got := strings.Contains(reject, `recall(query="handle:`); got != tc.wantRecall[i] {
					t.Errorf("retry %d repair names recall = %v want %v:\n%s", i+1, got, tc.wantRecall[i], reject)
				}
				if !tc.wantRecall[i] && strings.Contains(text, "recall") {
					t.Errorf("retry %d names recall while it is not callable:\n%s", i+1, text)
				}
			}
		})
	}
}

// A grounding-rejected worker report is superseded in place: id, ord, and
// content are preserved and the row count is stable.
func TestSupersedeWorkerReportPreservesRowInPlace(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	reportID := "rejected-report"
	if err := store.AppendMessages(ctx, sess.ID, api.Message{
		ID:      reportID,
		Role:    api.MessageRoleAssistant,
		Content: `{"leg_status":"complete","brief":"ungrounded survey"}`,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	before, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before", err)
	var beforeOrd int64
	for _, m := range before {
		if m.ID == reportID {
			beforeOrd = m.Ord
		}
	}

	if err := mgr.Runner.Transcript.SupersedeWorkerReport(ctx, sess.ID, reportID); err != nil {
		testutil.FailErr(t, "SupersedeWorkerReport", err)
	}

	after, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after", err)
	// Zero delete: the row count is stable across the retract.
	if len(after) != len(before) {
		t.Fatalf("message count = %d want %d (no physical delete on retract)", len(after), len(before))
	}
	var superseded *api.Message
	for i := range after {
		if after[i].ID == reportID {
			superseded = &after[i]
		}
	}
	if superseded == nil {
		t.Fatal("rejected report must remain in transcript (superseded, not deleted)")
	}
	if superseded.Kind != api.MessageKindSuperseded {
		t.Fatalf("kind = %q want superseded", superseded.Kind)
	}
	if superseded.Ord != beforeOrd {
		t.Fatalf("ord = %d want %d (ord preserved on supersede)", superseded.Ord, beforeOrd)
	}
	if superseded.Content == "" {
		t.Fatal("content must be preserved on supersede (row kept in search)")
	}
}

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

func testTaskBrief(goal string) map[string]any {
	return map[string]any{"goal": goal, "done_when": []any{"Return grounded results."}}
}
