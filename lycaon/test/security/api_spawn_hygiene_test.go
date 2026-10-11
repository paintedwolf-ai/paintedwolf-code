package security

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"

	"github.com/lycaon/lycaon/internal/llm"
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

	testdbseed.InsertProjectRoot(t, h.DB, testdbseed.DefaultProjectID, t.TempDir())
	r, err := h.Delegations.Manager.Create(ctx, api.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      dirtyTask,
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "h.Delegations.Manager.Create failed", err)
	if _, err := h.Delegations.Manager.DispatchLeg(ctx, r.ID, r.Legs[0].ID, ""); err != nil {
		testutil.FailErr(t, "h.Delegations.Manager.DispatchLeg failed", err)
	}

	testutil.WaitFor(t, 10*time.Second, func() bool {
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
