package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestApplyHunkResolutionsRejectsInvalidRangesWithoutMutation(t *testing.T) {
	original := []byte("one\ntwo\n")
	for _, hunks := range [][]api.WorkerPromoteHunkResolution{
		{{StartLine: 0, EndLine: 1, Content: "bad"}},
		{{StartLine: 5, EndLine: 5, Content: "bad"}},
		{{StartLine: 2, EndLine: 5, Content: "bad"}},
		{{StartLine: 3, EndLine: 3, Content: "append is not a replacement"}},
		{
			{StartLine: 1, EndLine: 2, Content: "first"},
			{StartLine: 2, EndLine: 2, Content: "overlap"},
		},
	} {
		if _, err := applyHunkResolutionsToBytes(original, "a.txt", hunks); err == nil {
			t.Fatalf("hunks %+v unexpectedly accepted", hunks)
		}
	}
	if string(original) != "one\ntwo\n" {
		t.Fatalf("input mutated: %q", original)
	}
}

func TestApplyHunkResolutionsUsesOriginalCoordinates(t *testing.T) {
	got, err := applyHunkResolutionsToBytes([]byte("one\ntwo\nthree\nfour\n"), "a.txt", []api.WorkerPromoteHunkResolution{
		{StartLine: 4, EndLine: 4, Content: "FOUR\nFIVE"},
		{StartLine: 1, EndLine: 2, Content: "ONE"},
	})
	testutil.FailErr(t, "apply hunks", err)
	if string(got) != "ONE\nthree\nFOUR\nFIVE\n" {
		t.Fatalf("resolved content = %q", got)
	}
}

func TestPromoteTransactionRecoversCommittedTargets(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-recover", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	primary := filepath.Join(root, "script.sh")
	testutil.FailErr(t, "seed primary", os.WriteFile(primary, []byte("old\n"), 0o755))

	plans := []promoteMutation{{
		path: "script.sh", original: []byte("old\n"), originalExists: true, originalMode: 0o755,
		target: []byte("new\n"), targetExists: true,
	}}
	tx, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin transaction", err)
	promote := PromoteRootsForTask(task, nil)
	testutil.FailErr(t, "apply transaction", tx.apply(t.Context(), promote, task))

	found, err := recoverPromoteTransaction(dataDir, task.ID, promote, task)
	testutil.FailErr(t, "recover transaction", err)
	if !found {
		t.Fatal("transaction journal not found")
	}
	raw, err := os.ReadFile(primary)
	testutil.FailErr(t, "read recovered primary", err)
	if string(raw) != "old\n" {
		t.Fatalf("recovered primary = %q", raw)
	}
	info, err := os.Stat(primary)
	testutil.FailErr(t, "stat recovered primary", err)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("recovered mode = %o", info.Mode().Perm())
	}
}

func TestPromoteTransactionRefusesToOverwriteDivergentPrimary(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-diverged", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	primary := filepath.Join(root, "a.txt")
	testutil.FailErr(t, "seed primary", os.WriteFile(primary, []byte("old\n"), 0o644))
	plans := []promoteMutation{{
		path: "a.txt", original: []byte("old\n"), originalExists: true, originalMode: 0o644,
		target: []byte("new\n"), targetExists: true,
	}}
	tx, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin transaction", err)
	promote := PromoteRootsForTask(task, nil)
	testutil.FailErr(t, "apply transaction", tx.apply(t.Context(), promote, task))
	testutil.FailErr(t, "external edit", os.WriteFile(primary, []byte("external\n"), 0o644))

	_, err = recoverPromoteTransaction(dataDir, task.ID, promote, task)
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("recover error = %v, want divergence", err)
	}
	raw, readErr := os.ReadFile(primary)
	testutil.FailErr(t, "read divergent primary", readErr)
	if string(raw) != "external\n" {
		t.Fatalf("divergent primary overwritten: %q", raw)
	}
}

func TestPromoteTransactionValidatesWholeReadSetBeforeFirstWrite(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-stale-read", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	for _, path := range []string{"a.txt", "b.txt"} {
		testutil.FailErr(t, "seed "+path, os.WriteFile(filepath.Join(root, path), []byte("old\n"), 0o644))
	}
	plans := []promoteMutation{
		{path: "a.txt", original: []byte("old\n"), originalExists: true, originalMode: 0o644, target: []byte("new-a\n"), targetExists: true},
		{path: "b.txt", original: []byte("old\n"), originalExists: true, originalMode: 0o644, target: []byte("new-b\n"), targetExists: true},
	}
	tx, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin transaction", err)
	testutil.FailErr(t, "concurrent edit", os.WriteFile(filepath.Join(root, "b.txt"), []byte("external\n"), 0o644))

	err = tx.apply(t.Context(), PromoteRootsForTask(task, nil), task)
	var unstarted *promoteTransactionUnstartedError
	if !errors.As(err, &unstarted) {
		t.Fatalf("apply error = %v, want unstarted conflict", err)
	}
	a, readErr := os.ReadFile(filepath.Join(root, "a.txt"))
	testutil.FailErr(t, "read untouched first path", readErr)
	if string(a) != "old\n" {
		t.Fatalf("first path mutated before read-set validation completed: %q", a)
	}
}
