package progressclosure_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/internal/workerrecords"
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
	_, err = handler(t.Context(), map[string]any{"content": before.Content, "reconciliation_token": view.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	var missingRevision *toolrejection.ToolReject
	if !errors.As(err, &missingRevision) || missingRevision.Code != "PROGRESS_REVISION_REQUIRED" || closure.Reconciliation("session") == nil {
		t.Fatalf("acknowledgement bypassed revision requirement: %v", err)
	}
	out := &tools.ToolInvocationOut{}
	body, err := handler(t.Context(), map[string]any{"content": before.Content, "expected_revision": before.Revision, "item_ids": before.ItemIDs, "reconciliation_token": view.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Effects: tools.InvocationEffects{Out: out}})
	testutil.FailErr(t, "acknowledge represented finish", err)
	if out.Facts.WorkflowAdvanced == nil || !*out.Facts.WorkflowAdvanced {
		t.Fatal("finish acknowledgement did not publish typed advancement")
	}
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
	if closure.Acknowledge(t.Context(), "session", old.Token) {
		t.Fatal("stale token acknowledged a new finish")
	}
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read checklist", err)
	handler := reporting.ProgressHandler(store, func(_ctx context.Context, session string) string { return session }, closure)
	_, err = handler(t.Context(), map[string]any{"content": "- [x] Deliver", "expected_revision": before.Revision, "item_ids": before.ItemIDs, "reconciliation_token": old.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	var rejection *toolrejection.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != "PROGRESS_RECONCILIATION_CHANGED" {
		t.Fatalf("stale token result: %v", err)
	}
	if store.Get(t.Context(), "session") != before.Content || len(closure.Reconciliation("session").Jobs) != 2 {
		t.Fatal("stale acknowledgement changed reconciliation state")
	}
}

func TestReconciliationServiceRejectsTokensWithoutPendingFinishes(t *testing.T) {
	closure := progressclosure.New()
	if closure.Acknowledge(t.Context(), "session", "invented") {
		t.Fatal("empty reconciliation state accepted an arbitrary token")
	}
}

func TestDocumentTransitionAndWorkerAcknowledgementRemainDistinct(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Deliver\n- [ ] Verify"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "session", "worker-one")
	view := closure.Reconciliation("session")
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read current progress", err)
	handler := reporting.ProgressHandler(store, func(_ context.Context, id string) string { return id }, closure)
	body, err := handler(t.Context(), map[string]any{"content": "- [x] Deliver\n- [ ] Verify", "expected_revision": before.Revision, "item_ids": before.ItemIDs, "reconciliation_token": view.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "record terminal deliverable and finish", err)
	var result struct {
		Status        string                   `json:"status"`
		ClosedItemIDs []string                 `json:"closed_item_ids"`
		Ack           string                   `json:"reconciliation_status"`
		Snapshot      progress.RequestSnapshot `json:"snapshot"`
	}
	testutil.FailErr(t, "decode distinct outcomes", json.Unmarshal([]byte(body), &result))
	if result.Status != "updated" || len(result.ClosedItemIDs) != 1 || result.ClosedItemIDs[0] != before.ItemIDs[0] || result.Snapshot.Summary.Pending != 1 || result.Snapshot.Summary.AllRequiredItemsTerminal || result.Ack != "acknowledged" || closure.Reconciliation("session") != nil {
		t.Fatalf("transition acknowledgement: %s", body)
	}
}

type interleavingProgressStore struct {
	progress.Store
	before func()
}

func (s interleavingProgressStore) Update(ctx context.Context, id string, change func(progress.Snapshot) (progress.Document, error)) (progress.Snapshot, error) {
	s.before()
	return s.Store.Update(ctx, id, change)
}

func TestNewWorkerFinishDuringDocumentCommitRemainsUnacknowledged(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed pending work", store.Set("session", "- [ ] Deliver\n- [ ] Verify"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "session", "worker-one")
	observed := closure.Reconciliation("session")
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "observe progress", err)
	writer := interleavingProgressStore{Store: store, before: func() { closure.Arm(t.Context(), "session", "worker-two") }}
	publications := 0
	release := progress.RegisterWriteObserver(func(_ context.Context, event progress.WriteEvent) {
		if event.SessionID == "session" {
			publications++
		}
	})
	defer release()
	handler := reporting.ProgressHandler(writer, func(_ context.Context, id string) string { return id }, closure)
	body, err := handler(t.Context(), map[string]any{"content": "- [x] Deliver\n- [ ] Verify", "expected_revision": before.Revision, "item_ids": before.ItemIDs, "reconciliation_token": observed.Token}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "publish document with concurrent finish", err)
	var result struct {
		Changed  bool              `json:"document_changed"`
		Status   string            `json:"reconciliation_status"`
		Snapshot progress.Snapshot `json:"snapshot"`
	}
	testutil.FailErr(t, "decode independent outcomes", json.Unmarshal([]byte(body), &result))
	if !result.Changed || result.Status != "changed" || publications != 1 || store.Get(t.Context(), "session") != "- [x] Deliver\n- [ ] Verify" {
		t.Fatalf("document outcome was lost: %s events=%d", body, publications)
	}
	pending := closure.Reconciliation("session")
	if pending == nil || len(pending.Jobs) != 2 || pending.Token == observed.Token || result.Snapshot.Reconciliation == nil || result.Snapshot.Reconciliation.Token != pending.Token {
		t.Fatalf("new finish was consumed by old token: %s", body)
	}
}

func TestFinishAfterCommitSurvivesAnOlderAcknowledgement(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed deliverable", store.Set("session", "- [ ] Deliver"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "session", "worker-one")
	observed := closure.Reconciliation("session")
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "observe plan", err)
	release := progress.RegisterWriteObserver(func(ctx context.Context, event progress.WriteEvent) {
		if event.SessionID == "session" {
			closure.Arm(ctx, "session", "worker-two")
		}
	})
	defer release()
	update, err := progress.PrepareAuthorUpdate("- [x] Deliver")
	testutil.FailErr(t, "prepare terminal document", err)
	result, err := update.ExpectRevision(before.Revision).ExpectItemIDs(before.ItemIDs...).ExpectReconciliation(observed.Token).Apply(t.Context(), store, "session", closure)
	testutil.FailErr(t, "commit document before new finish", err)
	if !result.Updated || result.ReconciliationStatus != "changed" || result.Snapshot.Reconciliation == nil || len(result.Snapshot.Reconciliation.Jobs) != 2 || !progress.AllTerminal(result.Snapshot.Content) {
		t.Fatalf("postcommit finish was consumed: %+v", result)
	}
}

func TestReconciliationTokenBindsTheExactBaselineRevision(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed pending document", store.Set("session", "- [ ] Deliver"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "session", "worker-one")
	first := closure.Reconciliation("session")
	if !closure.Acknowledge(t.Context(), "session", first.Token) {
		t.Fatal("current token rejected")
	}
	testutil.FailErr(t, "publish identical bytes in a new revision", store.Set("session", "- [ ] Deliver"))
	closure.Arm(t.Context(), "session", "worker-one")
	second := closure.Reconciliation("session")
	if first.Token == second.Token || closure.Acknowledge(t.Context(), "session", first.Token) {
		t.Fatal("old revision token acknowledged a newer baseline")
	}
	if !closure.Acknowledge(t.Context(), "session", second.Token) {
		t.Fatal("new baseline token rejected")
	}
}

type recordedAccounts struct {
	jobs [][]string
	err  error
}

func (r *recordedAccounts) AccountJobs(_ context.Context, jobs []string) error {
	r.jobs = append(r.jobs, jobs)
	return r.err
}

// Acknowledgement accounts for exactly the listed finishes; a failed record
// keeps the set armed so the next update can acknowledge it.
func TestAcknowledgementAccountsForTheListedResults(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed pending document", store.Set("session", "- [ ] Deliver"))
	accounts := &recordedAccounts{err: errors.New("database busy")}
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.SetAccounts(accounts)
	closure.Arm(t.Context(), "session", "worker-b")
	closure.Arm(t.Context(), "session", "worker-a")
	rec := closure.Reconciliation("session")
	if closure.Acknowledge(t.Context(), "session", rec.Token) || closure.Reconciliation("session") == nil {
		t.Fatal("failed accounting cleared the set")
	}
	accounts.err = nil
	if !closure.Acknowledge(t.Context(), "session", rec.Token) || closure.Reconciliation("session") != nil {
		t.Fatal("acknowledgement kept the set")
	}
	if len(accounts.jobs) != 2 || !slices.Equal(accounts.jobs[1], []string{"worker-a", "worker-b"}) {
		t.Fatalf("accounted %v", accounts.jobs)
	}
}

// An operation that accounts for one finish releases only that finish; the
// latch clears with the last one.
func TestReleaseDropsAccountedFinishes(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed pending document", store.Set("session", "- [ ] Deliver"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "session", "worker-a")
	closure.Arm(t.Context(), "session", "worker-b")
	closure.Release("session", []string{"worker-a"})
	rec := closure.Reconciliation("session")
	if rec == nil || !slices.Equal(rec.Jobs, []string{"worker-b"}) {
		t.Fatalf("after one release = %+v", rec)
	}
	closure.Release("session", []string{"worker-b"})
	if closure.Reconciliation("session") != nil {
		t.Fatal("latch kept after its last finish was accounted for")
	}
}

type finishNames map[string]string

func (n finishNames) NamesByJob(_ context.Context, jobID string) (workerrecords.JobNames, bool, error) {
	name, ok := n[jobID]
	return workerrecords.JobNames{Worker: name}, ok, nil
}

// The reconciliation the coordinator reads names each finish by its worker;
// the jobs behind the token stay host identity, and a release keeps the
// remaining finishes named.
func TestReconciliationNamesFinishesByWorker(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Implement"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.SetNames(finishNames{"job-fixer": "fixer", "job-docs": "docs"})
	closure.Arm(t.Context(), "session", "job-fixer")
	closure.Arm(t.Context(), "session", "job-docs")
	raw, err := json.Marshal(closure.Reconciliation("session"))
	testutil.FailErr(t, "marshal", err)
	var view map[string]any
	testutil.FailErr(t, "decode", json.Unmarshal(raw, &view))
	if bytes.Contains(raw, []byte("job-")) || len(view["settled_work"].([]any)) != 2 {
		t.Fatalf("reconciliation = %s", raw)
	}
	closure.Release("session", []string{"job-fixer"})
	if got := closure.Reconciliation("session").Settled; len(got) != 1 || !strings.Contains(got[0], "docs") {
		t.Fatalf("after release = %v", got)
	}
}
