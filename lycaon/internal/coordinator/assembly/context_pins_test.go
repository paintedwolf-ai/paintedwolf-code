package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderedSystemPromptSurvivesProviderContextFit(t *testing.T) {
	eng := prefixStabilityEngine(t)
	sess := &api.Session{ID: "pinned-system", AgentType: orchestration.ProfileCoordinator,
		Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}
	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{
		{ID: "request", Role: api.MessageRoleUser, Content: "Build the report", ContextPinned: true},
	}, nil)
	testutil.FailErr(t, "assemble prompt", err)
	if len(msgs) == 0 || !msgs[0].ContextPinned || msgs[0].Role != api.MessageRoleSystem {
		t.Fatal("rendered system instructions are not pinned")
	}
	fitted := compaction.DeterministicFit(compaction.CompactionConfig{KeepRecentMessages: 1}, compaction.ContextMessagesFromAPI(msgs), 1)
	var system, request bool
	for _, msg := range fitted {
		system = system || msg.Content == msgs[0].Content
		request = request || msg.ID == "request"
	}
	if !system || !request {
		t.Fatal("provider fit dropped system instructions or the current request")
	}
}
