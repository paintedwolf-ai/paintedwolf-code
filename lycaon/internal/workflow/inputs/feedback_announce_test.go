package inputs_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func feedbackMessages(msgs []api.Message) []api.Message {
	var out []api.Message
	for _, m := range msgs {
		if m.Kind == api.MessageKindWorkflowFeedback {
			out = append(out, m)
		}
	}
	return out
}

// Starting Decide without a request drops its request question into the transcript.
func TestStartAnnouncesFeedbackToTranscript(t *testing.T) {
	mgr, sessStore, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()

	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	msgs, err := sessStore.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	feedback := feedbackMessages(msgs)
	if len(feedback) != 1 {
		t.Fatalf("want 1 feedback message after start, got %d", len(feedback))
	}
	if strings.TrimSpace(feedback[0].Content) == "" {
		t.Fatal("feedback message must carry the phase prompt")
	}
	if feedback[0].Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("feedback must be transcript-visible, got %q", feedback[0].Visibility)
	}
	if api.IsPromptHistoryMessage(feedback[0]) {
		t.Fatal("feedback message must be excluded from LLM prompt history")
	}

	// Re-announcing with the persisted (already-marked) vars appends nothing new.
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	_, card := runstate.StampFeedbackAnnouncement(run, runstate.WorkflowRequestFeedbackID, &workflowdef.UserFeedbackPrompt{Prompt: "q"}, vars)
	mgr.Feedback.Cards.AppendAnnouncement(ctx, "sess-1", card)
	msgs, err = sessStore.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	if got := len(feedbackMessages(msgs)); got != 1 {
		t.Fatalf("re-announce should be idempotent, got %d feedback messages", got)
	}
}
