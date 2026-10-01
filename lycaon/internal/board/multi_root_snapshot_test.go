package board_test

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSnapshotBuilder_MultiRootOrientation(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	secondary := filepath.Join(t.TempDir(), "secondary")
	for _, dir := range []string{primary, secondary} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
			testutil.FailErr(t, "write main.go", err)
		}
	}
	p := repotest.NewProvider(t)
	t.Cleanup(func() { _ = p.Close() })
	for _, dir := range []string{primary, secondary} {
		if _, err := repoinfo.AwaitBrief(t.Context(), p, dir); err != nil {
			testutil.FailErr(t, "await brief", err)
		}
	}
	b := &board.SnapshotBuilder{Repo: p}
	roots := []projectroot.RootRef{
		{ID: "p", Path: primary, IsPrimary: true, Label: "lycaon"},
		{ID: "s", Path: secondary, IsPrimary: false, Label: "den"},
	}
	snap, err := b.Build(context.Background(), testdbseed.DefaultProjectID, primary, "sess-1", api.BoardDetailLevelCompact, roots)
	testutil.FailErr(t, "Build", err)
	if len(snap.OrientationRoots) != 2 {
		t.Fatalf("orientation roots = %d want 2", len(snap.OrientationRoots))
	}
	if snap.OrientationRoots[0].Label != "lycaon" || !snap.OrientationRoots[0].IsPrimary {
		t.Fatalf("primary section = %+v", snap.OrientationRoots[0])
	}
}
