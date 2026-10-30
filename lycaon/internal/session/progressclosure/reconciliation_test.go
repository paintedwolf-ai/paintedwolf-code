package progressclosure_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
)

func TestWorkerAcknowledgementDoesNotRequireClosingUnfinishedRows(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed checklist", store.Set("session", "- [x] Survey\n- [ ] Implement\n- [ ] Verify"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "session", "worker-one")
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read checklist", err)
	view := closure.Reconciliation("session")
	handler := reporting.ProgressHandler(store, func(_ctx context.Context, session string) string { return session }, closure)
	body, err := handler(t.Context(), map[string]any{"content": before.Content, "expected_revision": before.Revision, "reconciliation_token": view.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "acknowledge represented finish", err)
	var result struct {
		Status, ReconciliationStatus string
		Snapshot                     progress.Snapshot
	}
	var fields map[string]json.RawMessage
	testutil.FailErr(t, "decode acknowledgement", json.Unmarshal([]byte(body), &fields))
	testutil.FailErr(t, "decode status", json.Unmarshal(fields["status"], &result.Status))
	testutil.FailErr(t, "decode reconciliation", json.Unmarshal(fields["reconciliation_status"], &result.ReconciliationStatus))
	testutil.FailErr(t, "decode snapshot", json.Unmarshal(fields["snapshot"], &result.Snapshot))
	if result.Status != "unchanged" || result.ReconciliationStatus != "acknowledged" || result.Snapshot.Revision != before.Revision {
		t.Fatalf("acknowledgement outcome: %s", body)
	}
	if closure.Reconciliation("session") != nil || store.Get(t.Context(), "session") != before.Content {
		t.Fatal("acknowledgement changed unfinished deliverables")
	}
}

func TestStaleWorkerAcknowledgementRetainsNewFinishes(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Deliver"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "session", "worker-one")
	old := closure.Reconciliation("session")
	closure.Arm(t.Context(), "session", "worker-two")
	if closure.Acknowledge("session", old.Token) {
		t.Fatal("stale token acknowledged a new finish")
	}
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read checklist", err)
	handler := reporting.ProgressHandler(store, func(_ctx context.Context, session string) string { return session }, closure)
	_, err = handler(t.Context(), map[string]any{"content": "- [x] Deliver", "expected_revision": before.Revision, "reconciliation_token": old.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	var rejection *toolrejection.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != "PROGRESS_RECONCILIATION_CHANGED" {
		t.Fatalf("stale token result: %v", err)
	}
	if store.Get(t.Context(), "session") != before.Content || len(closure.Reconciliation("session").Jobs) != 2 {
		t.Fatal("stale acknowledgement changed owner state")
	}
}

func TestReconciliationOwnerRejectsTokensWithoutPendingFinishes(t *testing.T) {
	owner := progressclosure.New()
	if owner.Acknowledge("session", "invented") {
		t.Fatal("empty owner state accepted an arbitrary token")
	}
}

func TestDocumentTransitionAndWorkerAcknowledgementRemainDistinct(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Deliver\n- [ ] Verify"))
	owner := progressclosure.New()
	owner.SetProgress(store)
	owner.Arm(t.Context(), "session", "worker-one")
	view := owner.Reconciliation("session")
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read current progress", err)
	release := progress.RegisterWriteObserver(func(ctx context.Context, event progress.WriteEvent) {
		if event.SessionID == "session" {
			owner.AfterWrite(ctx, event.SessionID)
		}
	})
	defer release()
	handler := reporting.ProgressHandler(store, func(_ context.Context, id string) string { return id }, owner)
	body, err := handler(t.Context(), map[string]any{"content": "- [x] Deliver\n- [ ] Verify", "expected_revision": before.Revision, "reconciliation_token": view.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "record terminal deliverable and finish", err)
	var result struct {
		Status string `json:"status"`
		Closed bool   `json:"deliverable_closed"`
		Ack    string `json:"reconciliation_status"`
	}
	testutil.FailErr(t, "decode distinct outcomes", json.Unmarshal([]byte(body), &result))
	if result.Status != "updated" || !result.Closed || result.Ack != "acknowledged" || owner.Reconciliation("session") != nil {
		t.Fatalf("transition acknowledgement: %s", body)
	}
}
