package documentcore

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func coreForTest(t *testing.T) *Engine {
	t.Helper()
	engine, err := New(context.Background())
	testutil.FailErr(t, "start document core", err)
	t.Cleanup(func() { testutil.FailErr(t, "close document core", engine.Close(context.Background())) })
	return engine
}

func callForTest(t *testing.T, engine *Engine, request Request) Snapshot {
	t.Helper()
	result, err := engine.Call(context.Background(), request)
	testutil.FailErr(t, "document "+request.Action, err)
	return result
}

func TestConcurrentTextSurvivesDuplicatesAndRestart(t *testing.T) {
	engine := coreForTest(t)
	callForTest(t, engine, Request{Action: "open", Handle: 1, Client: 1})
	base := callForTest(t, engine, Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "a🐺z"}}, Checkpoint: true})
	for _, id := range []uint32{2, 3} {
		callForTest(t, engine, Request{Action: "open", Handle: id, Client: id, Update: base.Checkpoint})
	}
	left := callForTest(t, engine, Request{Action: "edit", Handle: 2, Edits: []Edit{{Index: 1, Insert: "left"}}})
	right := callForTest(t, engine, Request{Action: "edit", Handle: 3, Edits: []Edit{{Index: 3, Insert: "right"}}})
	var result Snapshot
	for _, update := range [][]byte{right.Update, left.Update, right.Update, left.Update} {
		result = callForTest(t, engine, Request{Action: "apply", Handle: 1, Update: update, Checkpoint: true})
	}
	if result.Text != "aleft🐺rightz" {
		t.Fatalf("concurrent text = %q", result.Text)
	}
	other := coreForTest(t)
	restored := callForTest(t, other, Request{Action: "open", Handle: 1, Client: 4, Update: result.Checkpoint})
	if restored.Text != result.Text {
		t.Fatalf("checkpoint restored %q; want %q", restored.Text, result.Text)
	}
}

func TestCoreRejectsMalformedUpdates(t *testing.T) {
	engine := coreForTest(t)
	callForTest(t, engine, Request{Action: "open", Handle: 1, Client: 1})
	if _, err := engine.Call(context.Background(), Request{Action: "apply", Handle: 1, Update: []byte{255}}); err == nil {
		t.Fatal("malformed update accepted")
	}
}

func TestSelectiveUndoSurvivesRestartAndKeepsLaterPeerInsertions(t *testing.T) {
	engine := coreForTest(t)
	callForTest(t, engine, Request{Action: "open", Handle: 1, Client: 1})
	callForTest(t, engine, Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "A B"}}})
	agent := callForTest(t, engine, Request{Action: "edit", Handle: 1, Edits: []Edit{{Index: 2, Insert: "agent"}}, CaptureUndo: true, Checkpoint: true})
	callForTest(t, engine, Request{Action: "open", Handle: 2, Client: 2, Update: agent.Checkpoint})
	peer := callForTest(t, engine, Request{Action: "edit", Handle: 2, Edits: []Edit{{Index: 4, Insert: "mine"}}})
	current := callForTest(t, engine, Request{Action: "apply", Handle: 1, Update: peer.Update, Checkpoint: true})
	restarted := coreForTest(t)
	callForTest(t, restarted, Request{Action: "open", Handle: 1, Client: 1, Update: current.Checkpoint})
	undone := callForTest(t, restarted, Request{Action: "undo", Handle: 1, Undo: agent.Undo})
	if undone.Text != "A mineB" {
		t.Fatalf("selective undo = %q; peer insertion must survive", undone.Text)
	}
	redone := callForTest(t, restarted, Request{Action: "undo", Handle: 1, Undo: undone.Undo})
	if redone.Text != current.Text {
		t.Fatalf("selective redo = %q, want %q", redone.Text, current.Text)
	}
}
