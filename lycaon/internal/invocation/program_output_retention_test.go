package invocation_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

func TestProgramOutputLifetimeReconstructsActualClaimsAndKeepsReachableBodies(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
	spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(bytes.NewBufferString("screened complete output"))), tooloutput.StreamSpillOptions{Store: store, ClaimID: child.Invocation().Identity.ExecutionID, MaximumBytes: 64, PreviewBytes: 4, Admission: quota, WorkingMemory: quota, RetainedState: quota})
	testutil.FailErr(t, "retain actual source-owned screened body", err)
	output := invocation.RetainedOutput{ExecutionID: child.Invocation().Identity.ExecutionID, Stream: "stdout", Reference: "output:actual-retained-result", Complete: true}
	lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, quota)
	testutil.FailErr(t, "admit invocation output lifecycle", err)
	if err = lifetime.Attach(t.Context(), f.program, output, spill); err == nil {
		t.Fatal("new work bypassed boot reference reconstruction")
	}
	testutil.FailErr(t, "reconstruct empty durable output set", lifetime.Restore(t.Context()))
	claim := spill.RetentionClaimID()
	testutil.FailErr(t, "durably attach same live body token", lifetime.Attach(t.Context(), f.program, output, spill))
	testutil.FailErr(t, "collect leaves still-reachable body open", lifetime.Collect(t.Context()))
	reader, err := lifetime.Open(t.Context(), f.session, f.program, output.Reference)
	testutil.FailErr(t, "open actual scoped durable output", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read complete output beyond preview", err)
	testutil.FailErr(t, "close actual output reader", reader.Close())
	if string(got) != "screened complete output" {
		t.Fatal("preview substituted for retained stream")
	}
	if _, err = lifetime.Open(t.Context(), "foreign-session", f.program, output.Reference); err == nil {
		t.Fatal("durable output crossed session")
	}
	if err = lifetime.Close(t.Context()); err == nil {
		t.Fatal("still-reachable output released runtime accounting")
	}
	claimed, err := store.HasRetentionClaim(claim, spill.Blob.ID)
	testutil.FailErr(t, "preserve actual marker through blocked close", err)
	if !claimed {
		t.Fatal("blocked lifecycle close removed recovery marker")
	}
	// A new host reconstructs the exact marker and independently admits all body
	// accounting before it accepts another stream. No new claim is manufactured.
	recoveredQuota := &outputQuota{}
	recovered, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, recoveredQuota)
	testutil.FailErr(t, "prepare new host output lifecycle", err)
	testutil.FailErr(t, "restore actual durable body and quota", recovered.Restore(t.Context()))
	reader, err = recovered.Open(t.Context(), f.session, f.program, output.Reference)
	testutil.FailErr(t, "open exact recovered output", err)
	testutil.FailErr(t, "close recovered reader", reader.Close())
	_, err = f.database.ExecContext(t.Context(), `DELETE FROM invocation_programs WHERE id=?`, f.program)
	testutil.FailErr(t, "release actual durable fixture references", err)
	testutil.FailErr(t, "collect recovered physical body before quota", recovered.Close(t.Context()))
	testutil.FailErr(t, "settle former host body accounting", lifetime.Close(t.Context()))
	if quota.disk != 0 || quota.state != 0 || quota.memory != 0 || recoveredQuota.disk != 0 || recoveredQuota.state != 0 || recoveredQuota.memory != 0 {
		t.Fatalf("output lifetime accounting leaked: old=%+v recovered=%+v", quota, recoveredQuota)
	}
}

func TestProgramOutputLifetimeRetainsAmbiguousWrapperUntilActualAttachmentReconciles(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
	lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, quota)
	testutil.FailErr(t, "admit output lifecycle", err)
	testutil.FailErr(t, "restore empty output references", lifetime.Restore(t.Context()))
	spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(bytes.NewBufferString("actual bytes"))), tooloutput.StreamSpillOptions{Store: store, ClaimID: child.Invocation().Identity.ExecutionID, MaximumBytes: 64, Admission: quota, WorkingMemory: quota, RetainedState: quota})
	testutil.FailErr(t, "retain pending body", err)
	_, err = f.database.ExecContext(t.Context(), `CREATE TRIGGER fixture_output_fault BEFORE INSERT ON invocation_program_outputs BEGIN SELECT RAISE(ABORT,'fixture output failure'); END`)
	testutil.FailErr(t, "install output ledger failure", err)
	if err = lifetime.Attach(t.Context(), f.program, invocation.RetainedOutput{ExecutionID: child.Invocation().Identity.ExecutionID, Stream: "stdout", Reference: "output:not-committed", Complete: true}, spill); err == nil {
		t.Fatal("ledger failure reported known attachment")
	}
	// The lifetime still owns the wrapper, so a known unreferenced failure can
	// safely collect it. An actually committed ambiguous ref would be retained by
	// the same authoritative SQL recheck, never inferred absent from this error.
	testutil.FailErr(t, "collect unreferenced failed attachment", lifetime.Collect(context.Background()))
	testutil.FailErr(t, "close empty lifetime", lifetime.Close(t.Context()))
	if quota.disk != 0 || quota.state != 0 || quota.memory != 0 {
		t.Fatalf("failed attachment leaked accounting: %+v", quota)
	}
}
