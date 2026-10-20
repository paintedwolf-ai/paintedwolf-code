package invocation_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

type outputQuota struct{ disk, state, memory int }

func (q *outputQuota) ReserveSpill(context.Context, int64) (func(), error) {
	q.disk++
	return func() { q.disk-- }, nil
}
func (q *outputQuota) ReserveProgramState(context.Context, int64) (func(), error) {
	q.state++
	return func() { q.state-- }, nil
}
func (q *outputQuota) Reserve(context.Context, int64) (func(), error) {
	q.memory++
	return func() { q.memory-- }, nil
}

func TestProgramRootSourceRetainsActualClaimAndRestoresWithoutSyntheticChild(t *testing.T) {
	f := newProgramReceiptFixture(t)
	_, err := f.database.ExecContext(t.Context(), `DELETE FROM invocation_programs WHERE id=?`, f.program)
	testutil.FailErr(t, "prepare fresh actual parent", err)
	store := f.recorder.ProgramBlobStore(blobstore.Store{Root: t.TempDir()})
	quota := &outputQuota{}
	spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(bytes.NewBufferString("${op} arg"))), tooloutput.StreamSpillOptions{Store: store, ClaimID: f.program, MaximumBytes: 64, PreviewBytes: 3, Admission: quota, WorkingMemory: quota, RetainedState: quota})
	testutil.FailErr(t, "retain screened exact submitted source", err)
	defer spill.Close()
	claim := spill.RetentionClaimID()
	// A failed program write leaves the actual claim and all body leases owned
	// by the wrapper. Failure is not permission to discard ambiguous attachment.
	_, err = f.database.ExecContext(t.Context(), `CREATE TRIGGER fixture_program_fault BEFORE INSERT ON invocation_programs BEGIN SELECT RAISE(ABORT,'fixture ledger fault'); END`)
	testutil.FailErr(t, "install program ledger fault", err)
	if body, err := f.recorder.BeginProgramWithSource(t.Context(), f.sourceStart, spill, false); err == nil || body != nil {
		t.Fatal("failed attachment transferred body")
	}
	reader, err := spill.Open(t.Context())
	testutil.FailErr(t, "failed attachment preserves wrapper", err)
	testutil.FailErr(t, "close retained reader", reader.Close())
	_, err = f.database.ExecContext(t.Context(), `DROP TRIGGER fixture_program_fault`)
	testutil.FailErr(t, "release actual ledger fault", err)
	body, err := f.recorder.BeginProgramWithSource(t.Context(), f.sourceStart, spill, false)
	testutil.FailErr(t, "atomically attach source and program", err)
	if body.Reference().RetentionClaimID != claim || quota.disk != 1 || quota.state != 1 || spill.Preview != nil {
		t.Fatalf("claim or quota moved incorrectly: %+v", quota)
	}
	if _, err = spill.Open(t.Context()); err == nil {
		t.Fatal("transferred wrapper reopened")
	}
	var children int
	testutil.FailErr(t, "inspect host executions", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=?`, f.program).Scan(&children))
	if children != 0 {
		t.Fatal("source retention fabricated a child")
	}
	// Simulate a new process's accounting. Restore uses the SAME durable claim;
	// it neither renews it nor manufactures producer completion from readable bytes.
	restoredQuota := &outputQuota{}
	releaseDisk, _ := restoredQuota.ReserveSpill(t.Context(), 1024)
	releaseState, _ := restoredQuota.ReserveProgramState(t.Context(), 4096)
	restored, err := tooloutput.RestoreRetainedStreamBody(t.Context(), store, body.Reference(), releaseDisk, releaseState, restoredQuota)
	testutil.FailErr(t, "reconstruct actual existing body claim", err)
	reader, err = restored.Open(t.Context())
	testutil.FailErr(t, "open restored source", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read exact restored source", err)
	testutil.FailErr(t, "close restored source reader", reader.Close())
	if string(got) != "${op} arg" || restored.Reference() != body.Reference() || restoredQuota.memory != 0 {
		t.Fatal("recovery changed bytes, claim, or scratch accounting")
	}
	_, err = f.database.ExecContext(t.Context(), `DELETE FROM invocation_programs WHERE id=?`, f.program)
	testutil.FailErr(t, "release caller-owned fixture receipt", err)
	testutil.FailErr(t, "collect restored body after durable ref release", restored.Close())
	testutil.FailErr(t, "settle old process accounting", body.Close())
	if quota.disk != 0 || quota.state != 0 || restoredQuota.disk != 0 || restoredQuota.state != 0 {
		t.Fatalf("body accounting leaked: old=%+v restored=%+v", quota, restoredQuota)
	}
}
