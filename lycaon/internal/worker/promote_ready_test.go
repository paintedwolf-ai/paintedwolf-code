package worker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatHunkSummaryLineIncludesLineRange(t *testing.T) {
	line := formatHunkSummaryLine(api.WorkerMergeHunk{
		StartLine: 12,
		EndLine:   18,
		Primary:   "primary text",
		Branch:    "branch text",
	})
	if !strings.HasPrefix(line, "L12-18:") {
		t.Fatalf("line = %q want L12-18 prefix", line)
	}
	if !strings.Contains(line, "primary:") || !strings.Contains(line, "branch:") {
		t.Fatalf("line = %q want both sides", line)
	}
}

func TestQualifyBranchRelUsesStableRootDirectory(t *testing.T) {
	roots := []projectroot.RootRef{
		{ID: "primary-id", Label: "app", Path: "/project/app", IsPrimary: true},
		{ID: "secondary-id", Label: "docs", Path: "/project/docs"},
	}
	branchDir, err := projectroot.BranchDirForID(roots[1].ID)
	testutil.FailErr(t, "secondary branch directory", err)

	var got string
	qualifyBranchRel(PromoteRoots{Roots: roots}, roots[0], branchDir+"/notes..draft.md", func(path string) {
		got = path
	})
	if got != "@docs/notes..draft.md" {
		t.Fatalf("qualified path = %q", got)
	}
}

func TestPromoteFileLockSerializesWritersAcrossServiceInstances(t *testing.T) {
	hostData := t.TempDir()
	first, err := acquirePromoteFileLock(t.Context(), hostData)
	testutil.FailErr(t, "acquire first promote lock", err)

	blockedCtx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if _, err := acquirePromoteFileLock(blockedCtx, hostData); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("competing promote lock = %v", err)
	}
	first.release()
	second, err := acquirePromoteFileLock(t.Context(), hostData)
	testutil.FailErr(t, "acquire promote lock after release", err)
	second.release()
}

func TestNotifyMergeWorktreeChanged(t *testing.T) {
	dir := t.TempDir()
	abs, err := filepath.Abs(dir)
	testutil.FailErr(t, "abs", err)

	var mu sync.Mutex
	var kinds []repochange.Kind
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != abs {
			return
		}
		mu.Lock()
		kinds = append(kinds, ev.Kind)
		mu.Unlock()
	})

	notifyMergeWorktreeChanged(t.Context(), &api.WorkerTask{WorkspacePath: dir}, []projectroot.RootRef{{Path: dir}})
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != 1 || kinds[0] != repochange.WorktreeChanged {
		t.Fatalf("kinds = %v want [WorktreeChanged]", kinds)
	}
}

func TestReadyResolutionArtifactDrop(t *testing.T) {
	ready := readyResolutionForConflict(artifactConflict("cache/foo.pyc"))
	if ready.Action != api.WorkerPromoteResolutionActionDrop {
		t.Fatalf("action = %q want drop", ready.Action)
	}
	if !ready.NeedsReview {
		t.Fatal("opaque worker bytes must not be discarded without a choice")
	}
	if automatic := readyResolutionsToPromote([]api.WorkerReadyResolution{ready}, true); len(automatic) != 0 {
		t.Fatalf("opaque path entered automatic promotion: %+v", automatic)
	}
	if ready.PathClass != "artifact" {
		t.Fatalf("path_class = %q want artifact", ready.PathClass)
	}
}

func TestApplyPromoteResolutionDropKeepsPrimary(t *testing.T) {
	dir := t.TempDir()
	rel := "cache/foo.pyc"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir cache", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write pyc", os.WriteFile(abs, []byte{0, 1}, 0o600))
	task := &api.WorkerTask{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, WorkspaceRoot: filepath.Join(dir, ".overlay")}
	err := applyTestPromoteResolution(task, nil, api.WorkerPromoteResolution{
		Path:   rel,
		Action: api.WorkerPromoteResolutionActionDrop,
	})
	testutil.FailErr(t, "drop", err)
	got, err := os.ReadFile(abs)
	testutil.FailErr(t, "read after drop", err)
	if !bytes.Equal(got, []byte{0, 1}) {
		t.Fatalf("primary after drop = %v", got)
	}
}

func TestProposeReconciledContentCleanThreeWay(t *testing.T) {
	c := PromoteConflict{
		Base:    "a\nb\n",
		Primary: "a\nb\n",
		Branch:  "a\nc\n",
	}
	proposed, reconciled := ProposeReconciledContent(c)
	if !reconciled {
		t.Fatal("expected reconciled")
	}
	if proposed != "a\nc\n" {
		t.Fatalf("proposed=%q", proposed)
	}
}

func TestProposeReconciledContentCombinesNonoverlappingEdits(t *testing.T) {
	c := PromoteConflict{
		Base:    "a\nb\nc\nd\ne\n",
		Primary: "a\nx\nc\nd\ne\n",
		Branch:  "a\nb\nc\ny\ne\n",
	}
	proposed, reconciled := ProposeReconciledContent(c)
	if !reconciled {
		t.Fatal("expected reconciled")
	}
	if !strings.Contains(proposed, "x") {
		t.Fatalf("primary sibling change missing: %q", proposed)
	}
	if !strings.Contains(proposed, "y") {
		t.Fatalf("branch intent missing: %q", proposed)
	}
}

func TestProposeReconciledContentRequiresChoiceForOverlappingEdits(t *testing.T) {
	c := PromoteConflict{Base: "value=base\n", Primary: "value=primary\n", Branch: "value=worker\n"}
	proposed, reconciled := ProposeReconciledContent(c)
	if reconciled || proposed != "" {
		t.Fatalf("overlap silently reconciled: %q (%v)", proposed, reconciled)
	}
}

func TestKeepBothEmptyContentDoesNotEmptyFile(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "proj")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	const rel = "shared.go"
	original := []byte("package main\nfunc funcA(){}\n")
	if err := os.WriteFile(filepath.Join(primary, rel), original, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	roots := []projectroot.RootRef{{ID: "p", Label: "a", Path: primary, IsPrimary: true}}
	task := &api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   primary,
		WorkspaceRoot:   filepath.Join(base, "branch"),
		WorkspaceRootID: "p",
	}

	err := applyTestPromoteResolution(task, roots, api.WorkerPromoteResolution{
		Path:   rel,
		Action: api.WorkerPromoteResolutionActionKeepBoth,
	})
	if err == nil {
		t.Fatal("keep_both with empty content must return an error, not silently apply")
	}
	data, readErr := os.ReadFile(filepath.Join(primary, rel))
	testutil.FailErr(t, "read primary file", readErr)
	if string(data) != string(original) {
		t.Fatalf("primary file must be unchanged, got %q", data)
	}
}

func applyTestPromoteResolution(task *api.WorkerTask, roots []projectroot.RootRef, res api.WorkerPromoteResolution) error {
	if task == nil {
		return nil
	}
	promote := PromoteRootsForTask(task, roots)
	plan, mutates, err := preparePromoteResolution(promote, task, res)
	if err != nil || !mutates {
		return err
	}
	return applyTargetPromoteMutation(promote, task, plan)
}
