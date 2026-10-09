package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSpawnChildStripsCoordinatorRejectMarkers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())

	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "store.Create failed", err)

	dirty := strings.Join([]string{
		"Implement the handler.",
		">>> Spec posture blocked",
		"Rejected: expand plan first",
		"Code: SPEC_EXPAND_MISSING",
		"Code: COORDINATOR_BATCH_ALREADY_CLOSED",
		"Call Task(agent_type=implementer) to spawn a worker via task tool.",
	}, "\n")

	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    dirty,
	})
	contractcheck.FailErr(t, "mgr.Workers.SpawnChild failed", err)

	msgs, err := mgr.Transcript.GetMessages(ctx, child.ID)
	contractcheck.FailErr(t, "mgr.GetMessages failed", err)
	if len(msgs) == 0 {
		t.Fatal("expected initial user prompt on child session")
	}
	for _, msg := range msgs {
		body := msg.Content
		if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
			body = msg.ToolResult.Content
		}
		if strings.Contains(body, ">>>") {
			t.Fatalf("child message contains coordinator block: %q", body)
		}
		if strings.Contains(body, "Rejected:") {
			t.Fatalf("child message contains reject marker: %q", body)
		}
		if strings.Contains(body, "Code: SPEC_") || strings.Contains(body, "Code: COORDINATOR_") {
			t.Fatalf("child message contains guidance code: %q", body)
		}
	}
	if !strings.Contains(msgs[0].Content, "Implement the handler") {
		t.Fatalf("sanitized prompt = %q want core task text preserved", msgs[0].Content)
	}
}
