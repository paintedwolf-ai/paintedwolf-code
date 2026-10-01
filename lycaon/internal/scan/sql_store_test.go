package scan

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestSQLStoreListByWorkflowRunID(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	ctx := context.Background()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run_abc", "session-abc", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run_other", "session-other", testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert match", store.Insert(ctx, api.CodeScan{
		ID:            "scan-run-a",
		CanonicalPath: "/tmp/p",
		ScannerID:     "semgrep",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		WorkflowRunID: "run_abc",
	}, nil, ""))
	testutil.FailErr(t, "insert other", store.Insert(ctx, api.CodeScan{
		ID:            "scan-run-b",
		CanonicalPath: "/tmp/p",
		ScannerID:     "semgrep",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		WorkflowRunID: "run_other",
	}, nil, ""))

	got, err := store.ListByWorkflowRunID(ctx, "run_abc")
	testutil.FailErr(t, "ListByWorkflowRunID", err)
	if len(got) != 1 || got[0].ID != "scan-run-a" {
		t.Fatalf("got = %#v want [scan-run-a]", got)
	}
	empty, err := store.ListByWorkflowRunID(ctx, "")
	testutil.FailErr(t, "ListByWorkflowRunID empty", err)
	if len(empty) != 0 {
		t.Fatalf("empty run id = %#v want nil", empty)
	}
}

func TestSQLStoreBindsReusableScanToMultipleContexts(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	ctx := context.Background()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-1", "session-1", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-2", "session-2", testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert unbound", store.Insert(ctx, api.CodeScan{
		ID:            "scan-unbound",
		CanonicalPath: "/tmp/p",
		ScannerID:     "lycaon-sast",
		Status:        api.CodeScanStatusPending,
		Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
	}, nil, ""))

	bound, err := store.BindContexts(ctx, "scan-unbound", "", "run-1", "session-1")
	testutil.FailErr(t, "bind first contexts", err)
	if bound == nil || bound.WorkflowRunID != "run-1" || bound.SessionID != "session-1" {
		t.Fatalf("bound = %#v", bound)
	}

	again, err := store.BindContexts(ctx, "scan-unbound", "", "run-1", "session-1")
	testutil.FailErr(t, "bind same contexts", err)
	if again.ID != bound.ID || again.WorkflowRunID != "run-1" || again.SessionID != "session-1" {
		t.Fatalf("same-run bind = %#v", again)
	}

	second, err := store.BindContexts(ctx, "scan-unbound", "", "run-2", "session-2")
	testutil.FailErr(t, "bind second contexts", err)
	if second.ID != bound.ID || second.WorkflowRunID != "run-2" || second.SessionID != "session-2" {
		t.Fatalf("second-run bind = %#v", second)
	}

	for _, tc := range []struct{ runID, sessionID string }{
		{runID: "run-1", sessionID: "session-1"},
		{runID: "run-2", sessionID: "session-2"},
	} {
		byRun, listErr := store.ListByWorkflowRunID(ctx, tc.runID)
		testutil.FailErr(t, "list by workflow", listErr)
		if len(byRun) != 1 || byRun[0].ID != "scan-unbound" || byRun[0].WorkflowRunID != tc.runID {
			t.Fatalf("workflow %s scans = %#v", tc.runID, byRun)
		}
		bySession, listErr := store.ListBySessionID(ctx, tc.sessionID)
		testutil.FailErr(t, "list by session", listErr)
		if len(bySession) != 1 || bySession[0].ID != "scan-unbound" || bySession[0].SessionID != tc.sessionID {
			t.Fatalf("session %s scans = %#v", tc.sessionID, bySession)
		}
	}

	_, err = store.BindContexts(ctx, "missing", "", "run-1", "session-1")
	if err == nil {
		t.Fatal("expected error binding a missing scan")
	}
}

func TestSQLStoreTracksTerminalWorkflowDelivery(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(sqlDB)
	ctx := t.Context()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-terminal", "session-terminal", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-adopted", "session-adopted", testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert terminal scan", store.Insert(ctx, api.CodeScan{
		ID: "scan-terminal", CanonicalPath: "/tmp/p", ScannerID: "lycaon-sast",
		Status: api.CodeScanStatusComplete, Categories: []api.ScanCategory{api.ScanCategorySAST},
	}, nil, ""))
	_, err := store.BindContexts(ctx, "scan-terminal", "", "run-terminal", "session-terminal")
	testutil.FailErr(t, "bind first workflow", err)
	_, err = store.BindContexts(ctx, "scan-terminal", "", "run-adopted", "session-adopted")
	testutil.FailErr(t, "bind adopted workflow", err)

	pending, err := store.PendingTerminalWorkflowBindings(ctx, "", 10)
	testutil.FailErr(t, "list pending terminal bindings", err)
	if len(pending) != 2 || pending[0].ScanID != "scan-terminal" || pending[1].ScanID != "scan-terminal" {
		t.Fatalf("pending terminal bindings = %#v", pending)
	}
	testutil.FailErr(t, "mark terminal notified", store.MarkWorkflowTerminalNotified(ctx, "scan-terminal", "run-terminal"))
	pending, err = store.PendingTerminalWorkflowBindings(ctx, "", 10)
	testutil.FailErr(t, "list partially acknowledged terminal bindings", err)
	if len(pending) != 1 || pending[0].WorkflowRunID != "run-adopted" {
		t.Fatalf("pending after first acknowledgement = %#v", pending)
	}
	testutil.FailErr(t, "mark adopted terminal notified", store.MarkWorkflowTerminalNotified(ctx, "scan-terminal", "run-adopted"))
	pending, err = store.PendingTerminalWorkflowBindings(ctx, "", 10)
	testutil.FailErr(t, "list acknowledged terminal bindings", err)
	if len(pending) != 0 {
		t.Fatalf("pending after all acknowledgements = %#v", pending)
	}
}

func TestSQLStoreCodeScanCRUD(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	rec := api.CodeScan{
		CanonicalPath: "/tmp/p",
		ScannerID:     "eslint",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategoryLint},
		Result:        json.RawMessage(`{"ok":true}`),
	}
	if err := store.Insert(context.Background(), rec, nil, ""); err != nil {
		testutil.FailErr(t, "store.Insert failed", err)
	}
	list, err := store.ListByCanonicalPath(context.Background(), "/tmp/p")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %#v err=%v", list, err)
	}
	got, err := store.Get(context.Background(), list[0].ID)
	testutil.FailErr(t, "store.Get failed", err)
	if got.ScannerID != "eslint" {
		t.Fatalf("scanner = %q", got.ScannerID)
	}
}

func TestSQLStoreListOpen(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	ctx := t.Context()
	for _, scan := range []api.CodeScan{
		{ID: "pending", CanonicalPath: "/tmp/p", Status: api.CodeScanStatusPending},
		{ID: "running", CanonicalPath: "/tmp/p", Status: api.CodeScanStatusRunning},
		{ID: "complete", CanonicalPath: "/tmp/p", Status: api.CodeScanStatusComplete},
	} {
		testutil.FailErr(t, "insert scan", store.Insert(ctx, scan, nil, ""))
	}
	open, err := store.ListOpen(ctx)
	testutil.FailErr(t, "list open scans", err)
	if len(open) != 2 || open[0].ID != "pending" || open[1].ID != "running" {
		t.Fatalf("open scans = %#v", open)
	}
}

func TestSQLStoreLatestForDelegationTieBreaksByInsertOrder(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	ctx := context.Background()
	categories := []api.ScanCategory{api.ScanCategorySecurity}
	stamp := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	older := api.CodeScan{
		ID:            "scan-older",
		CanonicalPath: "/tmp/p",
		Categories:    categories,
		Status:        api.CodeScanStatusPending,
		CreatedAt:     stamp,
		DelegationID:  "dep-1",
		HeadSHA:       "old",
		Trigger:       api.ScanTriggerManual,
	}
	newer := api.CodeScan{
		ID:            "scan-newer",
		CanonicalPath: "/tmp/p",
		Categories:    categories,
		Status:        api.CodeScanStatusPending,
		CreatedAt:     stamp,
		DelegationID:  "dep-1",
		HeadSHA:       "new",
		Trigger:       api.ScanTriggerManual,
	}
	testutil.FailErr(t, "Insert older", store.Insert(ctx, older, nil, ""))
	testutil.FailErr(t, "Insert newer", store.Insert(ctx, newer, nil, ""))

	got, err := store.LatestForDelegation(ctx, "dep-1", categories)
	testutil.FailErr(t, "LatestForDelegation", err)
	if got.ID != newer.ID {
		t.Fatalf("latest = %s want %s (same created_at must tie-break by insert order)", got.ID, newer.ID)
	}
}
