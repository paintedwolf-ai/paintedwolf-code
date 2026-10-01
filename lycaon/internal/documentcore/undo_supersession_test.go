package documentcore

import "testing"

func TestSelectiveUndoDoesNotRestoreSupersededReplacements(t *testing.T) {
	engine := coreForTest(t)
	call := func(request Request) Snapshot { return callForTest(t, engine, request) }
	call(Request{Action: "open", Handle: 1, Client: 1})
	call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "heading\nagent target\nhuman target\n"}}})
	agent := call(Request{Action: "edit", Handle: 1, Client: 2, Edits: []Edit{{Index: 14, Delete: 6, Insert: "changed"}}, CaptureUndo: true})
	person := "heading\nagent changed\nhuman changed\nunsaved human line\n"
	call(Request{Action: "edit", Handle: 1, Client: 3, Edits: []Edit{{Delete: uint32(len(agent.Text)), Insert: person}}})
	current := call(Request{Action: "edit", Handle: 1, Client: 4, Edits: []Edit{{Insert: "outside "}}, Checkpoint: true})
	call(Request{Action: "open", Handle: 2, Client: 5, Update: current.Checkpoint})
	call(Request{Action: "compact", Handle: 2, RetainedUndo: [][]byte{agent.Undo}})
	undone := call(Request{Action: "undo", Handle: 2, Undo: agent.Undo})
	if undone.Text != current.Text {
		t.Fatalf("superseded replacement resurrected text: got %q, want %q", undone.Text, current.Text)
	}
}

func TestSelectiveUndoFiltersSupersededHunksIndependently(t *testing.T) {
	engine := coreForTest(t)
	call := func(request Request) Snapshot { return callForTest(t, engine, request) }
	call(Request{Action: "open", Handle: 1, Client: 1})
	call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "old left\nold right\n"}}})
	agent := call(Request{Action: "edit", Handle: 1, Client: 2, Edits: []Edit{{Index: 9, Delete: 3, Insert: "new"}, {Delete: 3, Insert: "new"}}, CaptureUndo: true})
	current := call(Request{Action: "edit", Handle: 1, Client: 3, Edits: []Edit{{Delete: 3, Insert: "human"}}})
	undone := call(Request{Action: "undo", Handle: 1, Client: 4, Undo: agent.Undo})
	if undone.Text != "human left\nold right\n" {
		t.Fatalf("selective undo changed a superseded hunk: %q", undone.Text)
	}
	redone := call(Request{Action: "undo", Handle: 1, Client: 4, Undo: undone.Undo})
	if redone.Text != current.Text {
		t.Fatalf("selective redo = %q, want %q", redone.Text, current.Text)
	}
}

func TestSelectiveUndoDeletionRequiresSurvivingContext(t *testing.T) {
	for _, replaceContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained context", true: "replaced context"}[replaceContext], func(t *testing.T) {
			engine := coreForTest(t)
			call := func(request Request) Snapshot { return callForTest(t, engine, request) }
			call(Request{Action: "open", Handle: 1, Client: 1})
			call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "left removed right"}}})
			agent := call(Request{Action: "edit", Handle: 1, Client: 2, Edits: []Edit{{Index: 5, Delete: 8}}, CaptureUndo: true})
			want := "left removed right"
			if replaceContext {
				want = "new human text"
				call(Request{Action: "edit", Handle: 1, Client: 3, Edits: []Edit{{Delete: uint32(len(agent.Text)), Insert: want}}})
			}
			undone := call(Request{Action: "undo", Handle: 1, Client: 4, Undo: agent.Undo})
			if undone.Text != want {
				t.Fatalf("selective deletion undo = %q, want %q", undone.Text, want)
			}
		})
	}
}

func TestSelectiveUndoWholeDocumentDeletion(t *testing.T) {
	for _, later := range []string{"", "human text"} {
		t.Run("later="+later, func(t *testing.T) {
			engine := coreForTest(t)
			call := func(request Request) Snapshot { return callForTest(t, engine, request) }
			call(Request{Action: "open", Handle: 1, Client: 1})
			call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "removed"}}})
			agent := call(Request{Action: "edit", Handle: 1, Client: 2, Edits: []Edit{{Delete: 7}}, CaptureUndo: true})
			want := "removed"
			if later != "" {
				call(Request{Action: "edit", Handle: 1, Client: 3, Edits: []Edit{{Insert: later}}})
				want = later
			}
			undone := call(Request{Action: "undo", Handle: 1, Client: 4, Undo: agent.Undo})
			if undone.Text != want {
				t.Fatalf("whole-document deletion undo = %q, want %q", undone.Text, want)
			}
		})
	}
}
