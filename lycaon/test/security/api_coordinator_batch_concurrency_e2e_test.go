package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestHTTPCoordinatorBatchThreeTasksAndFollowUpE2E(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "^HTTP_BATCH_THREE",
			ToolCalls: []llm.MockToolCall{
				{ID: "tc1", Name: "task", Args: wiring.TaskToolArgs("repo-researcher", "Survey lockfiles.")},
				{ID: "tc2", Name: "task", Args: wiring.TaskToolArgs("path-explorer", "Map src/ paths.")},
				{ID: "tc3", Name: "task", Args: wiring.TaskToolArgs("code-reviewer", "Review main.go structure.")},
			},
			FollowUpText: "The batch is dispatched.",
		},
	}})
	recording := llm.NewRecordingClient(mock)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(recording))
	ctx := context.Background()
	dir := t.TempDir()
	seedCoordinatorRepo(t, dir)

	sess := createSessionHTTP(t, h.Server, dir)
	h.SeedProgress(t, ctx, sess.ID)
	acceptPromptHTTP(t, h.Server, sess.ID, "HTTP_BATCH_THREE")
	waitTaskEnqueueCountHTTP(t, h.Server, sess.ID, 3, 10*time.Second)
	waitTranscriptContainsHTTP(t, h.Server, sess.ID, "The batch is dispatched.", 10*time.Second)

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	enqueued := 0
	for _, msg := range msgs {
		if taskMessageEnqueued(msg) {
			enqueued++
		}
	}
	if enqueued != 3 {
		t.Fatalf("enqueued task tool messages = %d want 3", enqueued)
	}
	if len(recording.AllRequests()) != 2 {
		t.Fatalf("coordinator requests = %d want 2 (batch and follow-up)", len(recording.AllRequests()))
	}

	inFlight, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, api.WorkerStatusPending, api.WorkerStatusRunning)
	testutil.FailErr(t, "ListBySession", err)
	if len(inFlight) != 3 {
		t.Fatalf("in-flight jobs = %d want 3", len(inFlight))
	}
}

func TestHTTPCoordinatorBatchCapRejectE2E(t *testing.T) {
	wantCap := spawn.MaxInFlightTaskWorkers
	agents := []string{"repo-researcher", "path-explorer", "code-reviewer", "web-researcher", "security-reviewer"}
	calls := make([]llm.MockToolCall, 0, wantCap+1)
	for i := 0; i < wantCap+1; i++ {
		calls = append(calls, llm.MockToolCall{
			ID:   fmt.Sprintf("tc%d", i+1),
			Name: "task",
			Args: wiring.TaskToolArgs(agents[i%len(agents)], fmt.Sprintf("leg %d", i+1)),
		})
	}
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: "^HTTP_OVER_CAP", ToolCalls: calls},
		{Pattern: ".", Text: "FAIL_IF_UNBOUNDED_SECOND_COORDINATOR_TURN"},
	}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	seedCoordinatorRepo(t, dir)
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)
	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "HTTP_OVER_CAP"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	enqueued := 0
	for _, msg := range msgs {
		if taskMessageEnqueued(msg) {
			enqueued++
		}
	}
	if enqueued != wantCap {
		t.Fatalf("enqueued = %d want %d before cap reject", enqueued, wantCap)
	}
	if !strings.Contains(flattenMessages(msgs), workeradmission.CoordinatorWorkerInFlightCode) {
		t.Fatalf("expected %s over HTTP on cap exceed, transcript:\n%s", workeradmission.CoordinatorWorkerInFlightCode, flattenMessages(msgs))
	}
}

func TestHTTPCoordinatorBatchRosterAndPackBoardE2E(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "^HTTP_ROSTER",
		ToolCalls: []llm.MockToolCall{
			{ID: "tc1", Name: "task", Args: wiring.TaskToolArgs("repo-researcher", "a")},
			{ID: "tc2", Name: "task", Args: wiring.TaskToolArgs("path-explorer", "b")},
		},
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	seedCoordinatorRepo(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	h.SeedProgress(t, ctx, sess.ID)

	acceptPromptHTTP(t, h.Server, sess.ID, "HTTP_ROSTER")
	waitTaskEnqueueCountHTTP(t, h.Server, sess.ID, 2, 10*time.Second)
	waitToolFeedbackHTTP(t, h.Server, sess.ID, "BANNER_WORKER_INFLIGHT_ROSTER", 10*time.Second)

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if !strings.Contains(flattenMessages(msgs), "batch dispatch") {
		t.Fatal("expected batch roster note on ≥2 task() over HTTP")
	}

	raw, err := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{"detail_level": "compact"}, securityToolContext(sess.ID, sess.WorkspacePath, "coordinator"))
	testutil.FailErr(t, "pack_board", err)
	if !strings.Contains(raw, "in flight") {
		t.Fatalf("pack_board missing in-flight roster: %q", raw)
	}
}

func seedCoordinatorRepo(t *testing.T, dir string) {
	t.Helper()
	testutil.FailErr(t, "seed coordinator repo", os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixture\n"), 0o600))
}

func flattenMessages(msgs []api.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}
