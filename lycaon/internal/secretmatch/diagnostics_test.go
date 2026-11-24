package secretmatch

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestPhaseObserverAndEvidenceTraceRemainValueFree(t *testing.T) {
	noopUnsub := RegisterPhaseObserver(nil)
	noopUnsub()

	var mu sync.Mutex
	var got []ScreenPhaseRecord
	unsub := RegisterPhaseObserver(func(rec ScreenPhaseRecord) {
		mu.Lock()
		got = append(got, rec)
		mu.Unlock()
	})
	defer unsub()

	baseCtx := context.Background()
	if WithScreenCallID(baseCtx, "") != baseCtx {
		t.Fatal("empty call ID wrapped context")
	}
	if ScreenCallIDFrom(baseCtx) != "" {
		t.Fatal("unexpected call ID on bare context")
	}
	ctx := WithScreenCallID(baseCtx, "call-opaque-77")
	if ScreenCallIDFrom(ctx) != "call-opaque-77" {
		t.Fatalf("call ID = %q, want call-opaque-77", ScreenCallIDFrom(ctx))
	}

	var nilTrace *EvidenceTrace
	nilTrace.NoteRootWait(10, "b1", 1, 2, 3)
	if snap := nilTrace.Snapshot(); snap.RootsChecked != 0 {
		t.Fatalf("nil trace snapshot = %+v", snap)
	}

	ctx, trace := WithEvidenceTrace(ctx)
	ctx2, trace2 := WithEvidenceTrace(ctx)
	if ctx2 != ctx || trace2 != trace {
		t.Fatal("nested WithEvidenceTrace replaced existing trace")
	}

	trace.NoteRootWait(0, "", 0, 0, 0)
	trace.NoteRootWait(12, "build-1", 4, 29, 2)
	snap := trace.Snapshot()
	if snap.RootsChecked != 2 || snap.BuildsJoined != 1 || snap.ReadinessWaitMS != 12 ||
		snap.DirectoriesListed != 4 || snap.PathsClassified != 29 || snap.CredentialFilesRead != 2 {
		t.Fatalf("trace snapshot = %+v", snap)
	}

	EmitPhaseRecord(ctx, ScreenPhaseRecord{
		CallID:              ScreenCallIDFrom(ctx),
		ProjectID:           "proj-1",
		Phase:               "evidence_reconciled",
		DurationMS:          snap.ReadinessWaitMS,
		RootsChecked:        snap.RootsChecked,
		BuildsJoined:        snap.BuildsJoined,
		BuildIDs:            snap.BuildIDs,
		DirectoriesListed:   snap.DirectoriesListed,
		PathsClassified:     snap.PathsClassified,
		CredentialFilesRead: snap.CredentialFilesRead,
		ManagedValues:       3,
	})

	unsub()
	unsub()
	EmitPhaseRecord(ctx, ScreenPhaseRecord{CallID: "after-unsub", Phase: "ignored"})

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].CallID != "call-opaque-77" || got[0].BuildsJoined != 1 || got[0].ManagedValues != 3 {
		t.Fatalf("observed records = %+v", got)
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if strings.Contains(string(raw), "ghp_") || strings.Contains(string(raw), "sk-") {
		t.Fatalf("phase record leaked credential text: %s", string(raw))
	}
}
