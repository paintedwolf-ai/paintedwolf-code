package invocation_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	wire "github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"strings"
)

type outputQuota struct {
	disk, state, memory int
	diskBytes           int64
}

func (q *outputQuota) ReserveSpill(_ context.Context, n int64) (func(), error) {
	q.disk++
	q.diskBytes += n
	return func() {
		q.disk--
		q.diskBytes -= n
	}, nil
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
	programContext, cancelProgram := context.WithCancel(t.Context())
	defer cancelProgram()
	store := f.recorder.ProgramBlobStore(programContext, blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
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
	// A mistaken/ambiguous cleanup attempt must preserve the very marker
	// required by boot reconstruction while SQL still names this attachment.
	cancelProgram()
	if err = body.Close(); err == nil {
		t.Fatal("durable source allowed premature close")
	}
	claimed, err := store.HasRetentionClaim(claim, body.Reference().Blob.ID)
	testutil.FailErr(t, "keep exact marker after durable close refusal", err)
	if !claimed || quota.disk != 1 || quota.state != 1 {
		t.Fatal("durable close stranded recovery claim or accounting")
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

func TestIncompleteDeliveryWindowSurvivesReopenWithoutChangingExecution(t *testing.T) {
	f := newProgramReceiptFixture(t)
	before, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
	testutil.FailErr(t, "read execution before delivery", err)
	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(t.Context(), blobstore.Store{Root: t.TempDir()})
	lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
	testutil.FailErr(t, "open output lifetime", err)
	testutil.FailErr(t, "restore output lifetime", lifetime.Restore(t.Context()))
	defer func() {
		_, err := f.database.ExecContext(t.Context(), `DELETE FROM invocation_program_outputs WHERE program_id=?`, f.program)
		testutil.FailErr(t, "release receipt references", err)
		testutil.FailErr(t, "close output lifetime", lifetime.Close(t.Context()))
	}()
	input, output := io.Pipe()
	failure := &toolcontract.ProgramMemoryLimitError{Account: "session_spill_bytes", Maximum: 1048576, Observed: 1048577}
	go func() { _, _ = output.Write([]byte("prefix\n")); _ = output.CloseWithError(failure) }()
	spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(input), tooloutput.StreamSpillOptions{Store: store, ClaimID: f.program + ":stdout", MaximumBytes: 64, Admission: quota, WorkingMemory: quota, RetainedState: quota})
	testutil.FailErr(t, "retain incomplete output", err)
	defer spill.Close()
	ref := "output:program:" + f.program + ":stdout"
	_, err = lifetime.Attach(t.Context(), f.program, invocation.RetainedOutput{Stream: "stdout", Reference: ref, Complete: false}, spill)
	testutil.FailErr(t, "attach incomplete output", err)
	window := wire.CommandProgramOutputWindow{Stream: "stdout", Ref: ref, OutputPath: "@output/" + f.program + ".stdout", TotalBytes: 7, TotalLines: 1, Complete: false, InlineBudgetBytes: 12 << 10, Limit: &wire.CommandProgramOutputLimit{Code: "COMMAND_LIMIT_EXCEEDED", LimitID: failure.Account, Maximum: failure.Maximum, Observed: failure.Observed}}
	testutil.FailErr(t, "attach partial delivery facts", lifetime.AttachDeliveryWindow(t.Context(), f.program, window))
	reopened := invocation.NewSQLRecorder(f.database)
	after, err := reopened.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
	testutil.FailErr(t, "read partial delivery after reopen", err)
	if len(after.Program.Outputs) != 1 || !reflect.DeepEqual(after.Program.Outputs[0], window) || after.Program.State != before.Program.State || after.Program.StopCode != before.Program.StopCode || !reflect.DeepEqual(after.Program.StatusCode, before.Program.StatusCode) {
		t.Fatalf("delivery changed execution: before=%+v after=%+v", before.Program, after.Program)
	}
}

// Output bytes are required; optional source attribution may exceed receipt
// bounds without changing either the retained body or the delivery decision.
func TestProgramOutputOmitsUnadmittedMappingsAndKeepsCompleteBody(t *testing.T) {
	for _, mode := range []string{"encoding", "memory", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgramReceiptFixture(t)
			quota := &mappingQuota{maximum: 1 << 30}
			store := f.recorder.ProgramBlobStore(t.Context(), blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
			lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
			testutil.FailErr(t, "create lifetime", err)
			testutil.FailErr(t, "restore lifetime", lifetime.Restore(t.Context()))
			body := strings.Repeat("x", 400)
			spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(strings.NewReader(body))), tooloutput.StreamSpillOptions{Store: store, ClaimID: f.program + ":stdout", MaximumBytes: 1024, Admission: quota, WorkingMemory: quota, RetainedState: quota})
			testutil.FailErr(t, "retain complete body", err)
			defer spill.Close()
			mappings := make([]toolcontract.StreamSourceMapping, len(body))
			for i := range mappings {
				source := fmt.Sprintf("source:%d", i)
				if mode == "encoding" && i == 0 {
					source += strings.Repeat("s", 90<<20)
				}
				mappings[i] = toolcontract.StreamSourceMapping{Source: toolcontract.SourcePin{SourceID: source, Revision: "r1", SHA256: strings.Repeat("a", 64), StartByte: 0, EndByte: 1}, OutputStartByte: int64(i), OutputEndByte: int64(i + 1), SourceStartByte: 0, SourceEndByte: 1}
			}
			if mode == "memory" {
				quota.maximum = 4096
			}
			if mode == "invalid" {
				mappings[len(mappings)-1].SourceEndByte = 2
			}
			ref := "output:program:" + f.program + ":stdout"
			admitted, err := lifetime.Attach(t.Context(), f.program, invocation.RetainedOutput{Stream: "stdout", Reference: ref, Complete: true, Mappings: mappings}, spill)
			if mode == "invalid" {
				if err == nil {
					t.Fatal("invalid exact mapping accepted")
				}
			} else {
				testutil.FailErr(t, "attach body without optional maps", err)
				if len(admitted) != 0 {
					t.Fatal("unadmitted mapping returned for immediate source credit")
				}
				reopened := invocation.NewSQLRecorder(f.database)
				quota.maximum = 1
				_, err = reopened.ReadProgramOutputMappings(t.Context(), f.session, f.program, ref, quota)
				var limit *toolcontract.ProgramMemoryLimitError
				if !errors.As(err, &limit) || quota.memory != 0 {
					t.Fatalf("decode preceded admission or leaked: %v %+v", err, quota)
				}
				quota.maximum = 1 << 30
				read, err := reopened.ReadProgramOutputMappings(t.Context(), f.session, f.program, ref, quota)
				testutil.FailErr(t, "read actual persisted mapping decision", err)
				if read.Blob.Size != int64(len(body)) || len(read.Mappings) != 0 {
					t.Fatalf("changed body or persisted rejected credit: %+v %d", read.Blob, len(read.Mappings))
				}
				if quota.memory != 1 {
					t.Fatal("mapping decode lease ended before consumer")
				}
				read.Close()
				read.Close()
				quota.maximum = 1 << 30
				reader, err := lifetime.Open(t.Context(), f.session, f.program, ref)
				testutil.FailErr(t, "reopen complete retained body", err)
				actual, err := io.ReadAll(reader.Reader)
				testutil.FailErr(t, "read retained body", err)
				testutil.FailErr(t, "close retained body", reader.Reader.Close())
				if string(actual) != body {
					t.Fatal("optional attribution changed bytes")
				}
			}
			_, err = f.database.ExecContext(t.Context(), `DELETE FROM invocation_program_outputs WHERE program_id=?`, f.program)
			testutil.FailErr(t, "release receipt references", err)
			testutil.FailErr(t, "close lifetime", lifetime.Close(t.Context()))
			if quota.disk != 0 || quota.state != 0 || quota.memory != 0 {
				t.Fatalf("leaked admission: %+v", quota)
			}
		})
	}
}

type mappingQuota struct {
	outputQuota
	maximum int64
}

func (q *mappingQuota) Reserve(ctx context.Context, amount int64) (func(), error) {
	if amount > q.maximum {
		return nil, &toolcontract.ProgramMemoryLimitError{Account: "working_memory", Maximum: q.maximum, Observed: amount}
	}
	return q.outputQuota.Reserve(ctx, amount)
}
