//go:build integration

package board

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"testing"

	"time"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSnapshotBuilderRepoFromProvider(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	builder := &SnapshotBuilder{Repo: repotest.NewProvider(t)}
	p := builder.Repo
	p.Warm(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := repoinfo.AwaitBrief(ctx, p, dir); err != nil {
		testutil.FailErr(t, "AwaitBrief", err)
	}
	snap, err := builder.Build(context.Background(), testdbseed.DefaultProjectID, dir, "sess-1", "", nil)
	testutil.FailErr(t, "builder.Build failed", err)
	if snap.Repo.FileCount == 0 {
		t.Fatal("Repo.FileCount = 0, want >0")
	}
	if len(snap.Repo.Languages) == 0 {
		t.Fatal("Repo.Languages empty")
	}
	if len(snap.Repo.Layout.Files) == 0 {
		t.Fatalf("Repo.Layout.Files empty for tiny repo: %+v", snap.Repo.Layout)
	}
}

func TestSnapshotBuilderGitSlice(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	gittest.InitCommit(t, dir, "init")

	builder := &SnapshotBuilder{Repo: repotest.NewProvider(t), Git: git.NewManager()}
	snap, err := builder.Build(context.Background(), testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "builder.Build failed", err)
	if snap.Git == nil || snap.Git.Branch == "" {
		t.Fatalf("git slice missing: %+v", snap.Git)
	}
	if !snap.Git.Available {
		t.Fatal("git.Available = false, want true")
	}
}

func TestSnapshotBuilderGitNotRepository(t *testing.T) {
	dir := t.TempDir()
	builder := &SnapshotBuilder{Repo: repotest.NewProvider(t), Git: git.NewManager()}
	if _, err := repoinfo.AwaitBrief(t.Context(), builder.Repo, dir); err != nil {
		testutil.FailErr(t, "AwaitBrief", err)
	}
	snap, err := builder.Build(context.Background(), testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "builder.Build failed", err)
	if snap.Git == nil {
		t.Fatal("git slice nil, want unavailable marker")
	}
	if snap.Git.Available {
		t.Fatalf("git.Available = true, want false: %+v", snap.Git)
	}
	// Exact emptiness suppresses the survey hint.
	got := packboard.FormatGitLine(snap.Git, snap.Repo)
	if got != packboard.GitNoneEmptyRepoOrientationLine {
		t.Fatalf("git line = %q, want %q", got, packboard.GitNoneEmptyRepoOrientationLine)
	}
}

func TestSnapshotBuilderPackContentHashNonEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	builder := &SnapshotBuilder{
		Repo:        repotest.NewProvider(t),
		Delegations: delegation.NewMemoryStore(),
		Workers:     worker.NewInMemoryQueue(4),
	}
	snap, err := builder.Build(context.Background(), testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "builder.Build failed", err)
	if snap.PackContentHash == "" {
		t.Fatal("PackContentHash empty")
	}
}
