package documentcore

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAuthorshipPreservesClockGapsAndDistinctWriters(t *testing.T) {
	engine, err := New(t.Context())
	testutil.FailErr(t, "open core", err)
	t.Cleanup(func() { testutil.FailErr(t, "close core", engine.Close(context.Background())) })
	call := func(request Request) Snapshot {
		t.Helper()
		snapshot, err := engine.Call(t.Context(), request)
		testutil.FailErr(t, request.Action, err)
		return snapshot
	}
	call(Request{Action: "open", Handle: 1, Client: 1})
	call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "abcd"}}})
	call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Index: 1, Delete: 1}}})
	call(Request{Action: "edit", Handle: 1, Client: 2, Edits: []Edit{{Index: 2, Insert: "🐺"}}})
	before := call(Request{Action: "inspect", Handle: 1, Authorship: true, OmitText: true})
	want := []AuthorshipSpan{{Index: 0, Length: 1, Client: 1, Clock: 0}, {Index: 1, Length: 1, Client: 1, Clock: 2},
		{Index: 2, Length: 2, Client: 2, Clock: 0}, {Index: 4, Length: 1, Client: 1, Clock: 3}}
	if !reflect.DeepEqual(before.Authors, want) || before.Text != "" {
		t.Fatalf("authorship = %+v, text %q", before.Authors, before.Text)
	}
	after := call(Request{Action: "compact", Handle: 1, Authorship: true})
	if !reflect.DeepEqual(after.Authors, want) || after.Text != "ac🐺d" {
		t.Fatalf("compaction changed authorship: %+v", after)
	}
}

func TestTrackedReplicaChangesDoNotCaptureUndo(t *testing.T) {
	engine := coreForTest(t)
	call := func(request Request) Snapshot { return callForTest(t, engine, request) }
	call(Request{Action: "open", Handle: 1, Client: 1})
	base := call(Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "abcd"}}, Checkpoint: true})
	call(Request{Action: "open", Handle: 2, Client: 2, Update: base.Checkpoint})
	peer := call(Request{Action: "edit", Handle: 2, Edits: []Edit{{Index: 1, Delete: 1, Insert: "X"}}})
	accepted := call(Request{Action: "apply", Handle: 1, Client: 2, Update: peer.Update, TrackChanges: true})
	if accepted.Text != "aXcd" || len(accepted.Undo) != 0 || accepted.UndoUnits != 0 {
		t.Fatalf("identity-only admission captured history or changed text: %+v", accepted)
	}
	if !reflect.DeepEqual(accepted.Inserted, []IdentityRange{{Client: 2, Start: 0, End: 1}}) ||
		!reflect.DeepEqual(accepted.Deleted, []IdentityRange{{Client: 1, Start: 1, End: 2}}) {
		t.Fatalf("identity-only admission lost attribution: inserted %+v, deleted %+v", accepted.Inserted, accepted.Deleted)
	}
}
