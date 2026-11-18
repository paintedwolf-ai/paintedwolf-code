package store

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramObservationCommitsWithoutSourceBindings(t *testing.T) {
	st := newSQLTestStore(t)
	sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	receipt := &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "invocation", State: "running"}
	if handle, err := st.Evidence.CommitProgramObservation(t.Context(), sess.ID, "command", nil, "unbound"); err != nil || handle != "" {
		t.Fatalf("unbound outcome acquired handle: %q %v", handle, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if handle, err := st.Evidence.CommitProgramObservation(cancelled, sess.ID, "command", receipt, "cancelled"); err == nil || handle != "" {
		t.Fatalf("cancelled observation committed: %q %v", handle, err)
	}
	if handle, err := st.Evidence.CommitProgramObservation(t.Context(), "missing", "command", receipt, "wrong session"); err == nil || handle != "" {
		t.Fatalf("missing session acquired evidence: %q %v", handle, err)
	}
	first, err := st.Evidence.CommitProgramObservation(t.Context(), sess.ID, "command", receipt, "src/unseen.go:42: invented source")
	testutil.FailErr(t, "commit running observation", err)
	receipt.State = "completed"
	second, err := st.Evidence.CommitProgramObservation(t.Context(), sess.ID, "command_output", receipt, "test failed")
	testutil.FailErr(t, "commit settled observation", err)
	if first != "command#1" || second != "command#2" {
		t.Fatalf("observation handles = %q, %q", first, second)
	}
	ledger, err := st.Evidence.LoadLedger(t.Context(), sess.ID)
	testutil.FailErr(t, "load observations", err)
	if len(ledger.Handles) != 2 || len(ledger.ByPath) != 0 || ledger.Handles[second].SourceTool != "command_output" || ledger.Handles[first].SourceDelivery != nil {
		t.Fatalf("observations changed source authority: %+v", ledger)
	}
}
