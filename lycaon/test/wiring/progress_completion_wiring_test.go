package wiring

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Re-armed progress emits a distinct completion marker with an increasing sequence.
func TestProgressCompletionAppendsTranscriptMessage(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "Create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	revision, itemIDs := "", []any(nil)
	runUpdate := func(content string) {
		args := map[string]any{"content": content}
		if revision != "" {
			// One identity per row; a row the host has not seen yet is null.
			ids := append([]any(nil), itemIDs...)
			for strings.Count(content, "\n- [") > len(ids) {
				ids = append(ids, nil)
			}
			args["expected_revision"], args["item_ids"] = revision, ids
		}
		out, err := h.ToolRegistry.Run(ctx, "update_progress", args, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		})
		testutil.FailErr(t, "update_progress", err)
		var result struct {
			Snapshot struct {
				Revision string `json:"revision"`
				ItemIDs  []any  `json:"item_ids"`
			} `json:"snapshot"`
		}
		testutil.FailErr(t, "decode update_progress", json.Unmarshal([]byte(out), &result))
		revision, itemIDs = result.Snapshot.Revision, result.Snapshot.ItemIDs
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

	// Pending run: no completion marker yet.
	runUpdate("## Progress\n- [ ] a\n- [ ] b\n")
	if got := completions(); len(got) != 0 {
		t.Fatalf("pending run: completions = %d want 0", len(got))
	}

	// All done: one marker, seq 1, carrying the finished steps.
	runUpdate("## Progress\n- [x] a\n- [x] b\n")
	got := completions()
	if len(got) != 1 {
		t.Fatalf("after completion: completions = %d want 1", len(got))
	}
	meta := got[0].ProgressComplete
	if meta == nil || meta.Seq != 1 || len(meta.Steps) != 2 {
		t.Fatalf("completion meta = %+v want seq 1, 2 steps", meta)
	}

	// Re-arm with a new pending step, then finish again: a second marker, seq 2.
	runUpdate("## Progress\n- [x] a\n- [x] b\n- [ ] c\n")
	runUpdate("## Progress\n- [x] a\n- [x] b\n- [x] c\n")
	got = completions()
	if len(got) != 2 {
		t.Fatalf("after re-armed completion: completions = %d want 2", len(got))
	}
	if got[1].ProgressComplete == nil || got[1].ProgressComplete.Seq != 2 {
		t.Fatalf("second completion seq = %+v want 2", got[1].ProgressComplete)
	}
}
