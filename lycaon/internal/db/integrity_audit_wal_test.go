package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The background audit reads one table at a time, so writes during it are
// checkpointed between tables instead of piling up behind one snapshot.
func TestAuditLetsTheWALCheckpointBetweenTables(t *testing.T) {
	store, _ := openTestDBWithPath(t)
	audited, complete := 0, 0
	store.auditedTable = func(table string) {
		audited++
		growWAL(t, store, "audit-"+table, 20)
		result, err := CheckpointWAL(t.Context(), store)
		testutil.FailErr(t, "checkpoint during audit", err)
		if result.Complete() {
			complete++
		}
	}
	testutil.FailErr(t, "audit store", AuditIntegrity(t.Context(), store))
	if audited < 10 {
		t.Fatalf("audit visited %d tables, want every table", audited)
	}
	if complete != audited {
		t.Fatalf("%d of %d checkpoints during the audit were pinned by it", audited-complete, audited)
	}
}

func TestAuditDeadlineLeavesPendingWithoutQuarantine(t *testing.T) {
	_, store := uncleanAuditStore(t)
	err := auditTables(t.Context(), store.reader, nil, -time.Second)
	if !errors.Is(err, errAuditDeferred) || errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("expired audit = %v", err)
	}
	state, err := integrityAuditState(t.Context(), store.reader)
	testutil.FailErr(t, "read pending state", err)
	if state != integrityAuditPendingValue || store.Failure() != nil {
		t.Fatalf("deadline changed health: state=%q failure=%v", state, store.Failure())
	}
	growWAL(t, store, "after-deferred-audit", 20)
	result, err := CheckpointWAL(t.Context(), store)
	testutil.FailErr(t, "checkpoint after deadline", err)
	if !result.Complete() {
		t.Fatal("deferred audit pinned the WAL")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := auditTables(ctx, store.reader, nil, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled audit = %v", err)
	}
	testutil.FailErr(t, "complete audit after deadline", AuditIntegrity(t.Context(), store))
	state, err = integrityAuditState(t.Context(), store.reader)
	testutil.FailErr(t, "read completed state", err)
	if state != "" {
		t.Fatalf("completed audit state = %q", state)
	}
}
