package delegations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSandboxReconciliationPreservesDetachedDurableWorker(t *testing.T) {
	database := testdbfixture.Open(t, "reconcile.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	runtime := New(database, nil, worker.WorkersConfig{})
	runtime.BranchRoot, runtime.SeedRoot = t.TempDir(), t.TempDir()
	detached, current := t.TempDir(), t.TempDir()
	id, err := runtime.Queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{ParentSessionID: "parent", AgentType: "implementer", Prompt: "Inspect source", Brief: "Inspect source", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue retained worker", err)
	retained := enginepaths.JobBranchDir(runtime.BranchRoot, detached, id)
	orphan := enginepaths.JobBranchDir(runtime.BranchRoot, detached, "orphan")
	seed := filepath.Join(runtime.SeedRoot, enginepaths.ProjectKey(detached), "generations", "g1")
	for _, dir := range []string{retained, orphan, seed} {
		testutil.FailErr(t, "create sandbox", os.MkdirAll(dir, 0755))
		testutil.FailErr(t, "write sandbox evidence", os.WriteFile(filepath.Join(dir, "evidence.txt"), []byte("worker evidence"), 0600))
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.ReconcileWorkerSandboxes(canceled, []string{detached}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reconciliation returned %v", err)
	}
	for _, dir := range []string{retained, orphan, seed} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("failed reconciliation removed %s: %v", dir, err)
		}
	}
	removed, err := runtime.ReconcileWorkerSandboxes(t.Context(), []string{current})
	testutil.FailErr(t, "reconcile detached root", err)
	if removed == 0 {
		t.Fatal("detached orphan sandboxes were retained")
	}
	content, err := os.ReadFile(filepath.Join(retained, "evidence.txt"))
	testutil.FailErr(t, "read retained worker evidence", err)
	if string(content) != "worker evidence" {
		t.Fatalf("retained evidence changed: %q", content)
	}
	for _, dir := range []string{orphan, seed} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("detached orphan remains at %s: %v", dir, err)
		}
	}
	if task, ok := runtime.Queue.Lookup(t.Context(), id); !ok || task.ID != id {
		t.Fatal("sandbox cleanup discarded durable worker identity")
	}
	removed, err = runtime.ReconcileWorkerSandboxes(t.Context(), []string{current})
	testutil.FailErr(t, "repeat reconciliation", err)
	if removed != 0 {
		t.Fatalf("repeat reconciliation removed %d paths", removed)
	}

	// Durable references are required before deleting any snapshotted sandbox.
	unresolved := enginepaths.JobBranchDir(runtime.BranchRoot, current, "unresolved")
	testutil.FailErr(t, "create unresolved sandbox", os.MkdirAll(unresolved, 0755))
	testutil.FailErr(t, "close unavailable durable queue", database.Close())
	if _, err := runtime.ReconcileWorkerSandboxes(t.Context(), []string{current}); err == nil {
		t.Fatal("unavailable durable queue authorized sandbox cleanup")
	}
	for _, dir := range []string{retained, unresolved} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("failed durable lookup removed %s: %v", dir, err)
		}
	}
}
