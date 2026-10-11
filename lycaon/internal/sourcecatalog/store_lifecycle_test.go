package sourcecatalog

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTreeUnwatchedChangesReconcileAfterReuseWindow(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "first.go", "first")
	scope := TreeScope{Key: "unwatched"}
	first := readySummary(t, c, root, scope)
	writeTreeTestFile(t, root.Path, "unreported.go", "added without an event")
	store := summaryStoreFor(t, c, root, scope)
	store.mu.Lock()
	store.status.ValidatedAt = time.Now().Add(-repochange.CoverageRevalidationInterval - time.Minute)
	store.mu.Unlock()
	next := readySummary(t, c, root, scope)
	node, err := next.Node(t.Context(), "unreported.go")
	testutil.FailErr(t, "reconcile unobserved change", err)
	if node.Path != "unreported.go" || next.Status.Revision <= first.Status.Revision {
		t.Fatalf("unwatched generation did not refresh: node=%+v status=%+v", node, next.Status)
	}
}

func TestTreeEventOverflowFallsBackToBoundedReconciliation(t *testing.T) {
	s := &storeCore{}
	for i := range maxPendingTreePaths + 1000 {
		s.recordChanges([]string{fmt.Sprintf("file-%d", i)})
	}
	if !s.full || len(s.dirty) != 0 {
		t.Fatalf("overflow retained pending paths: full=%v paths=%d", s.full, len(s.dirty))
	}
}

func TestTreeReconcilesChangesArrivingDuringDiscovery(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "original")
	entered, resume := make(chan struct{}), make(chan struct{})
	var once, release sync.Once
	defer release.Do(func() { close(resume) })
	scope := TreeScope{Key: "interleaved", Filter: func(_ string, _ bool) bool {
		once.Do(func() { close(entered); <-resume })
		return true
	}}
	_, _, err := c.Trees.OpenSummary(t.Context(), "project", root, scope, 0)
	testutil.FailErr(t, "start discovery", err)
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("discovery did not enter read scope")
	}
	writeTreeTestFile(t, root.Path, "a.go", "updated material")
	c.InvalidateRoot(root.Path, "a.go")
	release.Do(func() { close(resume) })
	r := readySummary(t, c, root, scope)
	n, err := r.Node(t.Context(), ".")
	testutil.FailErr(t, "reconciled root", err)
	if n.Files != 1 || n.Bytes != int64(len("updated material")) || r.Status.Refreshing {
		t.Fatalf("discovery lost concurrent edit: node=%+v status=%+v", n, r.Status)
	}
}

func TestTreeColdReadDoesNotWaitForIndexer(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "package source")
	c.Trees.broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceMetadata: {Total: 1, PerLane: 1}})
	release, err := c.Trees.broker.Acquire(t.Context(), backgroundwork.Request{Key: "hold", Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
	testutil.FailErr(t, "hold indexing lane", err)
	defer release()
	r, status, err := c.Trees.OpenSummary(t.Context(), "project", root, TreeScope{Key: "all"}, 0)
	testutil.FailErr(t, "cold nonblocking read", err)
	if r != nil || status.State != StateWarming || !status.Refreshing {
		t.Fatalf("cold status=%+v reader=%v", status, r)
	}
	testutil.FailErr(t, "cancel queued indexing", c.Drain(t.Context()))
}

func TestTreeRestartServesPersistedGenerationDuringReconciliation(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "package source")
	first := readySummary(t, c, root, TreeScope{Key: "all"})
	// The first engine exits before the second opens its file.
	testutil.FailErr(t, "stop first engine", c.Drain(t.Context()))
	restarted := treeTestCatalog(t)
	restarted.Trees.treeDir = c.Trees.treeDir
	restarted.Trees.broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceMetadata: {Total: 1, PerLane: 1}})
	release, err := restarted.Trees.broker.Acquire(t.Context(), backgroundwork.Request{Key: "hold", Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
	testutil.FailErr(t, "hold startup reconciliation", err)
	defer release()
	r, status, err := restarted.Trees.OpenSummary(t.Context(), "project", root, TreeScope{Key: "all"}, 0)
	testutil.FailErr(t, "reopen persisted view", err)
	if r == nil {
		t.Fatalf("persisted view unavailable: %+v", status)
	}
	defer func() { _ = r.Close() }()
	n, err := r.Node(t.Context(), ".")
	testutil.FailErr(t, "persisted totals", err)
	if n.Files != 1 || status.Revision != first.Status.Revision || !status.Refreshing {
		t.Fatalf("persisted node=%+v status=%+v", n, status)
	}
	testutil.FailErr(t, "drain startup refresh", restarted.Drain(context.Background()))
}

func TestTreeScopedInvalidationRebasesParentEvents(t *testing.T) {
	c := treeTestCatalog(t)
	parent := t.TempDir()
	writeTreeTestFile(t, parent, "nested/old.go", "old")
	root := Root{ID: "nested", Path: parent + "/nested", Within: parent}
	_ = readySummary(t, c, root, TreeScope{Key: "all"})
	writeTreeTestFile(t, parent, "nested/new.go", "new")
	c.InvalidateRoot(parent, "nested/new.go")
	r := readySummary(t, c, root, TreeScope{Key: "all"})
	n, err := r.Node(t.Context(), ".")
	testutil.FailErr(t, "scoped totals", err)
	if n.Files != 2 {
		t.Fatalf("scoped node=%+v", n)
	}
}

func TestTreeReadTransactionIsReadOnly(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	r := readySummary(t, c, root, TreeScope{Key: "all"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := r.tx.ExecContext(ctx, "DELETE FROM nodes"); err == nil {
		t.Fatal("read generation allowed mutation")
	}
}

func TestSuspendProjectStoresRetiresInMemoryProjection(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "main.go", "package main")
	r := readySummary(t, c, root, TreeScope{Key: "all"})
	_ = r.Close()

	c.Trees.mu.Lock()
	storeCount := len(c.Trees.trees)
	c.Trees.mu.Unlock()
	if storeCount == 0 {
		t.Fatal("expected cached tree store")
	}

	c.Trees.SuspendProjectStores("project")

	c.Trees.mu.Lock()
	afterCount := len(c.Trees.trees)
	c.Trees.mu.Unlock()
	if afterCount != 0 {
		t.Fatalf("expected 0 stores after suspend, got %d", afterCount)
	}

	reopened := readySummary(t, c, root, TreeScope{Key: "all"})
	n, err := reopened.Node(t.Context(), ".")
	testutil.FailErr(t, "reopen from disk", err)
	if n.Files != 1 {
		t.Fatalf("expected 1 file after disk reload, got %d", n.Files)
	}
}
