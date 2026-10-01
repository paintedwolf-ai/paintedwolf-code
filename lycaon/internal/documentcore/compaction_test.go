package documentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompactionPreservesLatePeerEditsAndRetainedUndo(t *testing.T) {
	engine, err := New(t.Context())
	testutil.FailErr(t, "open core", err)
	t.Cleanup(func() { testutil.FailErr(t, "close core", engine.Close(context.Background())) })
	call := func(request Request) Snapshot {
		t.Helper()
		result, err := engine.Call(t.Context(), request)
		testutil.FailErr(t, request.Action, err)
		return result
	}
	call(Request{Action: "open", Handle: 1, Client: 1})
	base := call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: strings.Repeat("a", 4096)}}, Checkpoint: true})
	call(Request{Action: "open", Handle: 2, Client: 2, Update: base.Checkpoint})
	pending := call(Request{Action: "edit", Handle: 2, Edits: []Edit{{Index: 100, Insert: "peer"}}})
	call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Delete: 4096, Insert: "middle"}}, CaptureUndo: true})
	latest := call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Delete: 6, Insert: "new"}}, CaptureUndo: true, Checkpoint: true})
	call(Request{Action: "open", Handle: 3, Client: 1, Update: latest.Checkpoint})
	compacted := call(Request{Action: "compact", Handle: 1, RetainedUndo: [][]byte{latest.Undo}, Checkpoint: true})
	if compacted.Text != latest.Text || len(compacted.Checkpoint) >= len(latest.Checkpoint)/2 {
		t.Fatalf("compaction did not release old text: %d -> %d", len(latest.Checkpoint), len(compacted.Checkpoint))
	}
	left := call(Request{Action: "apply", Handle: 1, Update: pending.Update})
	right := call(Request{Action: "apply", Handle: 3, Update: pending.Update})
	if left.Text != right.Text || !strings.Contains(left.Text, "peer") {
		t.Fatalf("late edit changed across compaction: %q / %q", left.Text, right.Text)
	}
	left = call(Request{Action: "undo", Handle: 1, Undo: latest.Undo})
	right = call(Request{Action: "undo", Handle: 3, Undo: latest.Undo})
	if left.Text != right.Text || !strings.Contains(left.Text, "middle") || !strings.Contains(left.Text, "peer") {
		t.Fatalf("selective undo changed across compaction: %q / %q", left.Text, right.Text)
	}
}
