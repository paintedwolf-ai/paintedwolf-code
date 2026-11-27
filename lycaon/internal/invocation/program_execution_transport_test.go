package invocation_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExecutionTransportPersistsAndOlderReceiptsReadCleanly(t *testing.T) {
	f := newProgramReceiptFixture(t)
	olderChild := f.child(t)
	f.start(t, olderChild)
	olderID := olderChild.Invocation().Identity.ExecutionID
	failed := toolcontract.ProducerResult{Status: 128, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectUnknown}
	testutil.FailErr(t, "settle older child without transport", f.recorder.SettleChild(t.Context(), olderID, toolcontract.ProgramChildSettlement{Result: failed, OwnerInvoked: true, DetailReference: "output:older"}))

	currentChild := f.child(t)
	f.start(t, currentChild)
	currentID := currentChild.Invocation().Identity.ExecutionID
	testutil.FailErr(t, "settle current child", f.recorder.SettleChild(t.Context(), currentID, toolcontract.ProgramChildSettlement{Result: failed, OwnerInvoked: true, DetailReference: "output:current"}))

	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(t.Context(), blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
	lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
	testutil.FailErr(t, "admit output lifetime", err)
	testutil.FailErr(t, "restore output lifetime", lifetime.Restore(t.Context()))

	transport := wire.CommandExecutionTransport{
		Cause:        "endpoint_denied",
		Stage:        "policy",
		Destination:  "127.0.0.1:2222",
		Host:         "127.0.0.1",
		Port:         2222,
		PolicyReason: "loopback_port_ungranted",
		Attempts: []wire.CommandExecutionTransportAttempt{
			{Ordinal: 1, Destination: "127.0.0.1:2201", Host: "127.0.0.1", Port: 2201, Stage: "dial", Outcome: "established", ReplyCode: 0},
			{Ordinal: 2, Destination: "127.0.0.1:2222", Host: "127.0.0.1", Port: 2222, Stage: "policy", Outcome: "endpoint_denied", ReplyCode: 2, PolicyReason: "loopback_port_ungranted"},
		},
	}
	testutil.FailErr(t, "attach execution transport", lifetime.AttachExecutionTransport(t.Context(), f.program, currentID, transport))

	if err := lifetime.AttachExecutionTransport(t.Context(), f.program, currentID, wire.CommandExecutionTransport{Cause: "bogus"}); err == nil {
		t.Fatal("invalid transport cause accepted")
	}

	status := 128
	testutil.FailErr(t, "settle program", f.recorder.SettleProgram(t.Context(), f.program, toolcontract.ProgramSettlement{State: "completed", Status: &status, DetailReference: "output:program-source:" + f.program}))

	reopened := invocation.NewSQLRecorder(f.database)
	page, err := reopened.ReadProgramPage(t.Context(), f.session, f.program, "", 20)
	testutil.FailErr(t, "read program page after reopen", err)
	byID := map[string]wire.CommandExecutionReceipt{}
	for _, row := range page.Executions {
		byID[row.ID] = row
	}
	if byID[olderID].Transport != nil {
		t.Fatalf("older receipt fabricated transport: %+v", byID[olderID].Transport)
	}
	got := byID[currentID].Transport
	if got == nil || got.Cause != "endpoint_denied" || got.PolicyReason != "loopback_port_ungranted" || len(got.Attempts) != 2 || got.Attempts[0].Outcome != "established" || got.Attempts[1].Outcome != "endpoint_denied" {
		t.Fatalf("reopened execution transport = %+v", got)
	}

	doc, err := reopened.ReadReceiptDocument(t.Context(), f.session, []invocation.ReceiptScope{{ProgramID: f.program}}, "", 0, 20)
	testutil.FailErr(t, "read receipt document", err)
	records := decodeReceiptRows(t, doc.Rows)
	foundOlder, foundCurrent := false, false
	for _, rec := range records {
		if rec.Type != "execution" {
			continue
		}
		switch rec.Execution.ID {
		case olderID:
			foundOlder = true
			if rec.Execution.Transport != nil {
				t.Fatalf("older document execution fabricated transport: %+v", rec.Execution.Transport)
			}
		case currentID:
			foundCurrent = true
			if rec.Execution.Transport == nil || rec.Execution.Transport.Destination != "127.0.0.1:2222" || len(rec.Execution.Transport.Attempts) != 2 {
				t.Fatalf("current document execution transport = %+v", rec.Execution.Transport)
			}
		}
	}
	if !foundOlder || !foundCurrent {
		t.Fatalf("missing execution rows: older=%v current=%v", foundOlder, foundCurrent)
	}

	projected, err := reopened.ProjectProgram(t.Context(), f.session, f.program, 4)
	testutil.FailErr(t, "project program", err)
	if len(projected.Nonzero) != 2 ||
		projected.Nonzero[0].Receipt.ID != currentID || projected.Nonzero[0].Receipt.Transport == nil || projected.Nonzero[0].Receipt.Transport.Cause != "endpoint_denied" ||
		projected.Nonzero[1].Receipt.ID != olderID || projected.Nonzero[1].Receipt.Transport != nil {
		t.Fatalf("projected nonzero = %+v", projected.Nonzero)
	}
}
