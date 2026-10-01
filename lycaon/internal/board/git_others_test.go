package board_test

import (
	"fmt"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

func initCommittedGitRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		gittest.Run(t, dir, args...)
	}
	gittest.Init(t, dir)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	run("add", "README.md")
	run("commit", "-m", "init")
	return dir, run
}

func TestLoadGitOneRepositoryLeavesOthersEmpty(t *testing.T) {
	dir, _ := initCommittedGitRepo(t)
	b := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	roots := []projectroot.RootRef{{ID: "r1", Path: dir, Label: "app", IsPrimary: true}}
	snap, err := b.Build(t.Context(), "proj", dir, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build", err)
	if snap.Git == nil || !snap.Git.Available {
		t.Fatalf("git: %#v", snap.Git)
	}
	if snap.Git.RepoID == "" || snap.Git.Label == "" {
		t.Fatalf("expected identity: %#v", snap.Git)
	}
	if len(snap.Git.Others) != 0 || snap.Git.OthersTruncated != 0 {
		t.Fatalf("one-repo others: %#v", snap.Git)
	}
	if text := packboard.FormatOtherReposLine(snap.Git); text != "" {
		t.Fatalf("other-repos line should be empty, got %q", text)
	}
}

func TestLoadGitTwoReposOtherDirty(t *testing.T) {
	a, aRun := initCommittedGitRepo(t)
	bDir, _ := initCommittedGitRepo(t)
	aRun("checkout", "-b", "branch-a")
	testutil.FailErr(t, "dirty b", os.WriteFile(filepath.Join(bDir, "README.md"), []byte("dirty\n"), 0o644))

	builder := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	roots := []projectroot.RootRef{
		{ID: "ra", Path: a, Label: "alpha", IsPrimary: true},
		{ID: "rb", Path: bDir, Label: "beta"},
	}
	snap, err := builder.Build(t.Context(), "proj", a, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build", err)
	if snap.Git == nil || snap.Git.Branch != "branch-a" {
		t.Fatalf("active git: %#v", snap.Git)
	}
	if len(snap.Git.Others) != 1 {
		t.Fatalf("others: %#v", snap.Git.Others)
	}
	other := snap.Git.Others[0]
	if other.Label != "beta" || !other.Dirty {
		t.Fatalf("other entry: %#v", other)
	}
	line := packboard.FormatOtherReposLine(snap.Git)
	if line == "" || !strings.Contains(line, "Other repos:") || !strings.Contains(line, "beta") || !strings.Contains(line, "dirty") {
		t.Fatalf("board line = %q", line)
	}
}

func TestLoadGitTwoReposOtherCleanOmitsLine(t *testing.T) {
	a, _ := initCommittedGitRepo(t)
	bDir, _ := initCommittedGitRepo(t)
	builder := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	roots := []projectroot.RootRef{
		{ID: "ra", Path: a, Label: "alpha", IsPrimary: true},
		{ID: "rb", Path: bDir, Label: "beta"},
	}
	snap, err := builder.Build(t.Context(), "proj", a, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build", err)
	if len(snap.Git.Others) != 1 || snap.Git.Others[0].Dirty {
		t.Fatalf("others should be one clean entry: %#v", snap.Git.Others)
	}
	if line := packboard.FormatOtherReposLine(snap.Git); line != "" {
		t.Fatalf("clean others must omit board line, got %q", line)
	}
}

func TestLoadGitSixReposCapsOthers(t *testing.T) {
	active, _ := initCommittedGitRepo(t)
	roots := []projectroot.RootRef{{ID: "r0", Path: active, Label: "active", IsPrimary: true}}
	for i := 1; i <= 5; i++ {
		dir, _ := initCommittedGitRepo(t)
		testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644))
		roots = append(roots, projectroot.RootRef{
			ID:    fmt.Sprintf("id-%d", i),
			Path:  dir,
			Label: fmt.Sprintf("repo%d", i),
		})
	}

	builder := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	snap, err := builder.Build(t.Context(), "proj", active, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build", err)
	if len(snap.Git.Others) != board.MaxBoardGitOthers {
		t.Fatalf("others len=%d want %d: %#v", len(snap.Git.Others), board.MaxBoardGitOthers, snap.Git.Others)
	}
	if snap.Git.OthersTruncated != 1 {
		t.Fatalf("truncated=%d want 1", snap.Git.OthersTruncated)
	}
	line := packboard.FormatOtherReposLine(snap.Git)
	if line == "" || !strings.Contains(line, "+1 more") {
		t.Fatalf("line = %q", line)
	}
}

func TestLoadGitMixedCleanAndDirtyOthers(t *testing.T) {
	active, _ := initCommittedGitRepo(t)
	clean, _ := initCommittedGitRepo(t)
	dirty, _ := initCommittedGitRepo(t)
	testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(dirty, "README.md"), []byte("d\n"), 0o644))
	builder := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	roots := []projectroot.RootRef{
		{ID: "ra", Path: active, Label: "active", IsPrimary: true},
		{ID: "rc", Path: clean, Label: "clean"},
		{ID: "rd", Path: dirty, Label: "dirty"},
	}
	snap, err := builder.Build(t.Context(), "proj", active, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build", err)
	if len(snap.Git.Others) != 2 {
		t.Fatalf("others: %#v", snap.Git.Others)
	}
	line := packboard.FormatOtherReposLine(snap.Git)
	if !strings.Contains(line, "dirty") || strings.Contains(line, "clean") {
		t.Fatalf("line should name only dirty others: %q", line)
	}
}

func TestPackHashChangesWhenOtherRepoGoesDirty(t *testing.T) {
	a, _ := initCommittedGitRepo(t)
	bDir, _ := initCommittedGitRepo(t)
	builder := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	roots := []projectroot.RootRef{
		{ID: "ra", Path: a, Label: "alpha", IsPrimary: true},
		{ID: "rb", Path: bDir, Label: "beta"},
	}
	cleanSnap, err := builder.Build(t.Context(), "proj", a, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build clean", err)
	h1 := cleanSnap.PackContentHash

	testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(bDir, "README.md"), []byte("dirty\n"), 0o644))
	dirtySnap, err := builder.Build(t.Context(), "proj", a, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build dirty", err)
	if dirtySnap.PackContentHash == "" || dirtySnap.PackContentHash == h1 {
		t.Fatalf("hash should change when other repo dirties: %q vs %q", h1, dirtySnap.PackContentHash)
	}
}

func TestLoadGitActiveRootSelectsRepository(t *testing.T) {
	a, aRun := initCommittedGitRepo(t)
	bDir, bRun := initCommittedGitRepo(t)
	aRun("checkout", "-b", "branch-a")
	bRun("checkout", "-b", "branch-b")
	builder := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	roots := []projectroot.RootRef{
		{ID: "ra", Path: a, Label: "alpha", IsPrimary: true},
		{ID: "rb", Path: bDir, Label: "beta"},
	}
	fromPrimary, err := builder.Build(t.Context(), "proj", a, "sess", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "primary", err)
	fromSecondary, err := builder.Build(t.Context(), "proj", bDir, "sess", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "secondary", err)
	if fromPrimary.Git.RepoID == "" || fromPrimary.Git.RepoID == fromSecondary.Git.RepoID {
		t.Fatalf("repo ids should differ: primary=%q secondary=%q", fromPrimary.Git.RepoID, fromSecondary.Git.RepoID)
	}
	if fromPrimary.Git.Branch != "branch-a" || fromSecondary.Git.Branch != "branch-b" {
		t.Fatalf("branches: primary=%q secondary=%q", fromPrimary.Git.Branch, fromSecondary.Git.Branch)
	}
	// pack_board and the SSE pulse both call Build with the session workspace path —
	// same path yields the same active repository id.
	again, err := builder.Build(t.Context(), "proj", bDir, "sess", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "again", err)
	if again.Git.RepoID != fromSecondary.Git.RepoID {
		t.Fatalf("parity broken: %q vs %q", again.Git.RepoID, fromSecondary.Git.RepoID)
	}
}

func TestPackHashStableForOneRepoWithEmptyOthers(t *testing.T) {
	dir, _ := initCommittedGitRepo(t)
	builder := &board.SnapshotBuilder{Git: git.NewManager(), Repo: repotest.NewProvider(t)}
	roots := []projectroot.RootRef{{ID: "r1", Path: dir, Label: "app", IsPrimary: true}}
	snap, err := builder.Build(t.Context(), "proj", dir, "", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "build", err)
	base := *snap
	base.Git = &api.BoardGitSlice{
		Available:     snap.Git.Available,
		Branch:        snap.Git.Branch,
		HeadShort:     snap.Git.HeadShort,
		Dirty:         snap.Git.Dirty,
		StagedCount:   snap.Git.StagedCount,
		UnstagedCount: snap.Git.UnstagedCount,
		Others:        nil,
	}
	hWith := board.PackContentHash(*snap, snap.Repo.GeneratedAt)
	hWithout := board.PackContentHash(base, snap.Repo.GeneratedAt)
	if hWith != hWithout {
		t.Fatalf("one-repo empty others must not change pack hash: %q vs %q", hWith, hWithout)
	}
}
