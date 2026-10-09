package wiring

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const batchConcurrencyUserPrompt = "BATCH_THREE_PARALLEL_SCOUTS"

func batchThreeTaskMock() *llm.MockProvider {
	return llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "^" + batchConcurrencyUserPrompt,
			ToolCalls: []llm.MockToolCall{
				{ID: "tc1", Name: "task", Args: TaskToolArgs("repo-researcher", "Survey lockfiles and package manifests.")},
				{ID: "tc2", Name: "task", Args: TaskToolArgs("path-explorer", "Map src/ structure with line-cited excerpts.")},
				{ID: "tc3", Name: "task", Args: TaskToolArgs("implementer", "Add a // BATCH-MARKER comment to main.go.")},
			},
			FollowUpText: "The batch is dispatched.",
		},
		{
			Pattern: ".",
			Text:    "UNEXPECTED_EXTRA_COORDINATOR_TURN",
		},
	}})
}

func batchTaskAndReadMock() *llm.MockProvider {
	return llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "^BATCH_TASK_AND_READ",
			ToolCalls: []llm.MockToolCall{
				{ID: "tc1", Name: "task", Args: TaskToolArgs("path-explorer", "List top-level paths under src/.")},
				{ID: "tc2", Name: "read", Args: map[string]any{"path": "README.md"}},
			},
			FollowUpText: "The task is dispatched and the file is read.",
		},
	}})
}

func batchOverCapTaskMock() *llm.MockProvider {
	cap := spawn.MaxInFlightTaskWorkers
	calls := make([]llm.MockToolCall, 0, cap+1)
	agents := []string{"repo-researcher", "path-explorer", "implementer", "code-reviewer", "web-researcher", "security-reviewer"}
	for i := 0; i < cap+1; i++ {
		calls = append(calls, llm.MockToolCall{
			ID:   fmt.Sprintf("tc%d", i+1),
			Name: "task",
			Args: TaskToolArgs(agents[i%len(agents)], fmt.Sprintf("leg %d", i+1)),
		})
	}
	return llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: "^BATCH_OVER_CAP", ToolCalls: calls},
	}})
}

func countTaskEnqueuedToolMessages(msgs []api.Message) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if strings.Contains(msg.Content, `"status":"enqueued"`) || strings.Contains(msg.Content, `"status": "enqueued"`) {
			n++
		}
	}
	return n
}

func messageContains(msgs []api.Message, substr string) bool {
	for _, msg := range msgs {
		if strings.Contains(msg.Content, substr) {
			return true
		}
	}
	return false
}

func TestCoordinatorBatchThreeTasksAndFollowUpE2E(t *testing.T) {
	rec := llm.NewRecordingClient(batchThreeTaskMock())
	h := BuildForTest(t, WithLLMClient(rec))
	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "write README", writeReadme(t, dir))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, batchConcurrencyUserPrompt); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	msgs, err := h.SessionMgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if got := countTaskEnqueuedToolMessages(msgs); got != 3 {
		t.Fatalf("enqueued task tool messages = %d want 3", got)
	}
	if messageContains(msgs, "UNEXPECTED_EXTRA_COORDINATOR_TURN") {
		t.Fatal("unbounded coordinator completion ran")
	}
	if len(rec.AllRequests()) != 2 {
		t.Fatalf("coordinator LLM requests = %d want 2 (batch and follow-up)", len(rec.AllRequests()))
	}

	inFlight, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, api.WorkerStatusPending, api.WorkerStatusRunning)
	testutil.FailErr(t, "ListBySession", err)
	if len(inFlight) != 3 {
		t.Fatalf("in-flight worker jobs = %d want 3", len(inFlight))
	}
	agents := map[string]bool{}
	for _, task := range inFlight {
		agents[task.AgentType] = true
	}
	for _, want := range []string{"repo-researcher", "path-explorer", "implementer"} {
		if !agents[want] {
			t.Fatalf("missing agent %q in queue: %+v", want, inFlight)
		}
	}
}

func TestCoordinatorBatchTaskAndReadFollowUpE2E(t *testing.T) {
	rec := llm.NewRecordingClient(batchTaskAndReadMock())
	h := BuildForTest(t, WithLLMClient(rec))
	ctx := context.Background()
	dir := t.TempDir()
	if err := writeReadme(t, dir); err != nil {
		testutil.FailErr(t, "writeReadme", err)
	}

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "BATCH_TASK_AND_READ"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	msgs, err := h.SessionMgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if countTaskEnqueuedToolMessages(msgs) != 1 {
		t.Fatalf("expected one enqueued task(), got %d enqueued messages", countTaskEnqueuedToolMessages(msgs))
	}
	if !messageContains(msgs, "README") && !messageContains(msgs, "readme") {
		t.Fatalf("expected read tool result for README.md in messages")
	}
	if len(rec.AllRequests()) != 2 {
		t.Fatalf("LLM requests = %d want 2 (batch and follow-up)", len(rec.AllRequests()))
	}
}

func TestCoordinatorBatchOverCapRejectedInSameTurnE2E(t *testing.T) {
	h := BuildForTest(t, WithLLMClient(batchOverCapTaskMock()))
	ctx := context.Background()
	dir := t.TempDir()
	testutil.FailErr(t, "write README", writeReadme(t, dir))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "BATCH_OVER_CAP"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	msgs, err := h.SessionMgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if got := countTaskEnqueuedToolMessages(msgs); got != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("enqueued = %d want %d before cap reject", got, spawn.MaxInFlightTaskWorkers)
	}
	if !messageContains(msgs, workeradmission.CoordinatorWorkerInFlightCode) {
		t.Fatalf("expected %s on over-cap task() in batch, msgs=%+v", workeradmission.CoordinatorWorkerInFlightCode, msgs)
	}

	inFlight, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, api.WorkerStatusPending, api.WorkerStatusRunning)
	testutil.FailErr(t, "ListBySession", err)
	if len(inFlight) != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("queue in-flight = %d want cap %d", len(inFlight), spawn.MaxInFlightTaskWorkers)
	}
}

func TestCoordinatorBatchRosterNoteOnMultiTaskE2E(t *testing.T) {
	h := BuildForTest(t, WithLLMClient(batchThreeTaskMock()))
	ctx := context.Background()
	dir := t.TempDir()
	testutil.FailErr(t, "write README", writeReadme(t, dir))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, batchConcurrencyUserPrompt); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	msgs, err := h.SessionMgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if !messageContains(msgs, "batch dispatch") {
		t.Fatal("expected batch roster note when ≥2 task() enqueue in one turn")
	}
	if !messageContains(msgs, "repo-researcher") || !messageContains(msgs, "path-explorer") {
		t.Fatalf("roster note missing agent types: %+v", msgs)
	}
}

func TestCoordinatorBatchRosterNoteAbsentOnSingleTaskE2E(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "^SINGLE_TASK_ONLY",
		ToolCalls: []llm.MockToolCall{{
			ID: "tc1", Name: "task", Args: TaskToolArgs("implementer", "do work"),
		}},
	}}})
	h := BuildForTest(t, WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "SINGLE_TASK_ONLY"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	msgs, err := h.SessionMgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if messageContains(msgs, "batch dispatch") {
		t.Fatal("batch roster note must not appear for single task() enqueue")
	}
	if !messageContains(msgs, "BANNER_TASK_QUEUED") {
		t.Fatalf("expected per-task enqueue banner on single spawn, msgs=%+v", msgs)
	}
}

func TestCoordinatorPackBoardShowsInFlightRosterE2E(t *testing.T) {
	h := BuildForTest(t, WithLLMClient(batchThreeTaskMock()))
	ctx := context.Background()
	dir := t.TempDir()
	testutil.FailErr(t, "write README", writeReadme(t, dir))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, batchConcurrencyUserPrompt); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	raw, err := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{"detail_level": "compact"}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    sess.ID,
		Agent:        "coordinator",
	})
	testutil.FailErr(t, "pack_board", err)
	for _, want := range []string{"in flight", "repo-researcher", "path-explorer", "implementer"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("pack_board = %q missing %q", raw, want)
		}
	}
}

func TestCoordinatorBatchEnqueuesBeforeTurnEndsNotSerialFirstOnlyE2E(t *testing.T) {
	// One turn may enqueue several tasks.
	rec := llm.NewRecordingClient(batchThreeTaskMock())
	h := BuildForTest(t, WithLLMClient(rec))
	ctx := context.Background()
	dir := t.TempDir()
	testutil.FailErr(t, "write README", writeReadme(t, dir))

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, batchConcurrencyUserPrompt); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	inFlight, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, api.WorkerStatusPending)
	testutil.FailErr(t, "ListBySession", err)
	if len(inFlight) != 3 {
		t.Fatalf("pending jobs = %d want 3 (serial-first bug would leave 1)", len(inFlight))
	}
}

func writeReadme(t *testing.T, dir string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, "README.md"), []byte("# batch read fixture\n"), 0o644)
}
