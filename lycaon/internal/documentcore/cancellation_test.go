package documentcore

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCanceledCallerDoesNotCloseSharedDocuments(t *testing.T) {
	engine, err := New(t.Context())
	testutil.FailErr(t, "open core", err)
	t.Cleanup(func() { testutil.FailErr(t, "close core", engine.Close(context.Background())) })
	_, err = engine.Call(t.Context(), Request{Action: "open", Handle: 1, Client: 1})
	testutil.FailErr(t, "open first document", err)
	_, err = engine.Call(t.Context(), Request{Action: "open", Handle: 2, Client: 2})
	testutil.FailErr(t, "open second document", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = engine.Call(ctx, Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "retained"}}})
	testutil.FailErr(t, "finish bounded canceled call", err)
	_, err = engine.Call(t.Context(), Request{Action: "inspect", Handle: 2})
	testutil.FailErr(t, "read unaffected document", err)
	snapshot, err := engine.Call(t.Context(), Request{Action: "inspect", Handle: 1})
	testutil.FailErr(t, "read first document", err)
	if snapshot.Text != "retained" || engine.Closed() {
		t.Fatal("request cancellation closed shared state")
	}
}
