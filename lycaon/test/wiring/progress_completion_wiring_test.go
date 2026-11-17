package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Each completion edge records its finished steps.
func TestProgressCompletionAppendsTranscriptMessage(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "Create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	runUpdate := func(content string) {
		if _, err := h.ToolRegistry.Run(ctx, "update_progress", h.ProgressUpdateArgs(t, ctx, sess.ID, content), tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		}); err != nil {
			testutil.FailErr(t, "update_progress", err)
		}
	}

	completions := func() []wire.Message {
		msgs, err := h.Store.GetMessages(ctx, sess.ID)
		testutil.FailErr(t, "GetMessages", err)
		var out []wire.Message
		for _, m := range msgs {
			if wire.IsProgressCompleteMessage(m) {
				out = append(out, m)
			}
		}
		return out
	}

	// Pending work has no completion.
	runUpdate("## Progress\n- [ ] a\n- [ ] b\n")
	if got := completions(); len(got) != 0 {
		t.Fatalf("pending run: completions = %d want 0", len(got))
	}

	// Completing the plan records its finished steps.
	runUpdate("## Progress\n- [x] a\n- [x] b\n")
	got := completions()
	if len(got) != 1 {
		t.Fatalf("after completion: completions = %d want 1", len(got))
	}
	meta := got[0].ProgressComplete
	if meta == nil || len(meta.Steps) != 2 {
		t.Fatalf("completion meta = %+v want 2 steps", meta)
	}

	// New pending work creates another completion edge.
	runUpdate("## Progress\n- [x] a\n- [x] b\n- [ ] c\n")
	runUpdate("## Progress\n- [x] a\n- [x] b\n- [x] c\n")
	got = completions()
	if len(got) != 2 {
		t.Fatalf("after re-armed completion: completions = %d want 2", len(got))
	}
	if got[1].ProgressComplete == nil || len(got[1].ProgressComplete.Steps) != 3 {
		t.Fatalf("second completion = %+v want 3 steps", got[1].ProgressComplete)
	}
}
