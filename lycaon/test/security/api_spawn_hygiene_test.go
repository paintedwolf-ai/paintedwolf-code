package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestDelegationSpawnWorkerPromptHygiene(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "done",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dirtyTask := strings.Join([]string{
		"add handler",
		">>> Spec posture blocked",
		"Code: SPEC_SCOPE_MISSING",
	}, "\n")

	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	h.SeedProgress(t, ctx, parent.ID)
	_, err = h.ToolRegistry.Run(
		ctx,
		"task",
		wiring.TaskToolArgs(orchestration.ProfileRepoResearcher, dirtyTask),
		h.CoordinatorToolContext(t, ctx, parent, dir),
	)
	testutil.FailErr(t, "task tool failed", err)

	testutil.WaitFor(t, "the worker sends a request", func() bool {
		return len(rec.AllRequests()) > 0
	})

	for _, req := range rec.AllRequests() {
		for _, msg := range req.Messages {
			if msg.Role != api.MessageRoleUser && msg.Role != api.MessageRoleSystem {
				continue
			}
			if strings.Contains(msg.Content, ">>>") {
				t.Fatalf("worker-bound message contains coordinator block: role=%s content=%q", msg.Role, msg.Content)
			}
			if strings.Contains(msg.Content, "Code: SPEC_") {
				t.Fatalf("worker-bound message contains spec code: role=%s content=%q", msg.Role, msg.Content)
			}
		}
	}
}
