package tooloutput

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

type referenceQuota struct{ disk, state int }

func (q *referenceQuota) ReserveSpill(context.Context, int64) (func(), error) {
	q.disk++
	return func() { q.disk-- }, nil
}
func (q *referenceQuota) ReserveProgramState(context.Context, int64) (func(), error) {
	q.state++
	return func() { q.state-- }, nil
}
func referenceSpill(t *testing.T, store blobstore.Store, q *referenceQuota) *StreamSpill {
	t.Helper()
	out, err := RetainScreenedStream(t.Context(), ScreenedReader(io.NopCloser(bytes.NewBufferString("same fixture body"))), StreamSpillOptions{Store: store, ClaimID: "fixture-initiating-operation", MaximumBytes: 100, PreviewBytes: 4, Admission: q, RetainedState: q, WorkingMemory: fixtureStreamMemory{}})
	testutil.FailErr(t, "retain referenced stream", err)
	t.Cleanup(func() { _ = out.Close() })
	return out
}
func TestDuplicateInitiatingClaimsKeepDistinctBodyReferences(t *testing.T) {
	q := &referenceQuota{}
	store := blobstore.Store{Root: t.TempDir()}
	a, b := referenceSpill(t, store, q), referenceSpill(t, store, q)
	if a.Blob.ID != b.Blob.ID || a.RetentionClaimID() == b.RetentionClaimID() || a.InitiatingClaimID() != b.InitiatingClaimID() {
		t.Fatal("reference identity/provenance not separated")
	}
	if err := a.Close(); err == nil {
		t.Fatal("other retained reference must block collection")
	}
	if reader, err := a.Open(t.Context()); err == nil {
		_ = reader.Close()
		t.Fatal("closing unclaimed reference reopened")
	}
	reader, err := b.Open(t.Context())
	testutil.FailErr(t, "open surviving reference", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read surviving body", err)
	testutil.FailErr(t, "close surviving reader", reader.Close())
	if string(got) != "same fixture body" {
		t.Fatal("other reference lost bytes")
	}
	testutil.FailErr(t, "collect second reference", b.Close())
	testutil.FailErr(t, "settle first reference quota", a.Close())
	if q.disk != 0 || q.state != 0 {
		t.Fatalf("reference quota leaked %+v", q)
	}
}
func TestBodyTransferPreservesExactClaimAndLeasesAfterDurableAttachment(t *testing.T) {
	q := &referenceQuota{}
	out := referenceSpill(t, blobstore.Store{Root: t.TempDir()}, q)
	cause := errors.New("synthetic attachment failure")
	if token, err := out.TransferBody(t.Context(), func(context.Context, RetainedStreamReference) error { return cause }); token != nil || !errors.Is(err, cause) {
		t.Fatal("failed attachment transferred ownership")
	}
	if q.disk != 1 || q.state != 2 {
		t.Fatalf("failed attachment released leases %+v", q)
	}
	reader, err := out.Open(t.Context())
	testutil.FailErr(t, "failed attachment retains open", err)
	if token, err := out.TransferBody(t.Context(), func(context.Context, RetainedStreamReference) error {
		t.Fatal("persist called with active reader")
		return nil
	}); token != nil || err == nil {
		t.Fatal("active reader permitted transfer")
	}
	testutil.FailErr(t, "close pretransfer reader", reader.Close())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var persisted RetainedStreamReference
	token, err := out.TransferBody(ctx, func(_ context.Context, ref RetainedStreamReference) error { persisted = ref; cancel(); return nil })
	testutil.FailErr(t, "complete committed transfer despite cancellation", err)
	defer token.Close()
	if persisted.RetentionClaimID != out.RetentionClaimID() || token.Reference() != persisted || q.disk != 1 || q.state != 1 || out.Preview != nil {
		t.Fatalf("transfer changed claim/leases %+v %+v", persisted, q)
	}
	persisted.RetentionClaimID = "changed local value"
	if token.Reference().RetentionClaimID == persisted.RetentionClaimID {
		t.Fatal("reference not captured by value")
	}
	if reader, err := out.Open(t.Context()); err == nil {
		_ = reader.Close()
		t.Fatal("transferred wrapper retained open ownership")
	}
	testutil.FailErr(t, "close transferred display", out.Close())
	reader, err = token.Open(t.Context())
	testutil.FailErr(t, "durable owner opens same body", err)
	testutil.FailErr(t, "close transferred reader", reader.Close())
	testutil.FailErr(t, "collect transferred body", token.Close())
	if q.disk != 0 || q.state != 0 {
		t.Fatalf("transferred leases leaked %+v", q)
	}
}

func TestDurableReferenceClosePreservesActualClaimUntilReleased(t *testing.T) {
	for _, callbackFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(callbackFailure), func(t *testing.T) {
			retained := true
			cause := errors.New("fixture durable reference unavailable")
			store := blobstore.Store{Root: t.TempDir(), Retained: func(string) (bool, error) {
				if callbackFailure {
					return false, cause
				}
				return retained, nil
			}}
			quota := &referenceQuota{}
			spill := referenceSpill(t, store, quota)
			body, err := spill.TransferBody(t.Context(), func(context.Context, RetainedStreamReference) error { return nil })
			testutil.FailErr(t, "attach original body claim", err)
			reference := body.Reference()
			err = body.Close()
			if err == nil || (callbackFailure && !errors.Is(err, cause)) {
				t.Fatalf("durable reference did not stop collection: %v", err)
			}
			claimed, err := store.HasRetentionClaim(reference.RetentionClaimID, reference.Blob.ID)
			testutil.FailErr(t, "inspect exact original marker", err)
			if !claimed || quota.disk != 1 || quota.state != 1 {
				t.Fatalf("refused collection changed claim or quota: claimed=%v quota=%+v", claimed, quota)
			}
			if reader, err := body.Open(t.Context()); err == nil {
				_ = reader.Close()
				t.Fatal("closing body reopened")
			}
			retained = false
			callbackFailure = false
			testutil.FailErr(t, "collect after durable owner releases reference", body.Close())
			claimed, err = store.HasRetentionClaim(reference.RetentionClaimID, reference.Blob.ID)
			testutil.FailErr(t, "inspect released marker", err)
			if claimed {
				t.Fatal("released durable body retained its old marker")
			}
			if quota.disk != 0 || quota.state != 0 {
				t.Fatalf("released reference leaked accounting %+v", quota)
			}
		})
	}
}
