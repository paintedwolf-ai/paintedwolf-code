package repoinfo_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBriefRevokesEmptyClaimWhenIndexGenerationAdvances(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	provider := repoinfo.NewProvider(catalog.Trees, func(context.Context, string) (repoinfo.CatalogRoot, bool, error) {
		return repoinfo.CatalogRoot{ProjectID: "p", RootID: "r"}, true, nil
	}, "")
	t.Cleanup(func() { testutil.FailErr(t, "close provider", provider.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	initial, err := repoinfo.AwaitBrief(ctx, provider, root)
	testutil.FailErr(t, "measure empty repository", err)
	if !repoinfo.Current(initial) || initial.FileCount != 0 {
		t.Fatalf("initial brief: %+v", initial)
	}
	testutil.FailErr(t, "create source file", os.WriteFile(filepath.Join(root, "main.py"), []byte("print('hello')\n"), 0600))
	catalog.InvalidateRoot(root, "main.py")
	testutil.WaitFor(t, 5*time.Second, func() bool {
		status, err := catalog.Trees.IndexStatus(ctx, "p", sourcecatalog.Root{ID: "r", Path: root})
		return err == nil && status.Complete && !status.Refreshing && status.Revision != initial.Generation
	})
	brief, err := provider.Brief(ctx, root)
	testutil.FailErr(t, "read brief after index advance", err)
	if brief.Generation == initial.Generation && repoinfo.Current(brief) {
		t.Fatalf("older brief still presented as current: %+v", brief)
	}
	board := repoinfo.WireBrief(brief)
	if packboard.RepoKnownEmpty(board) {
		t.Fatalf("board still declares an empty workspace: %+v", board)
	}
	if initial.Partial || initial.Refreshing {
		t.Fatal("refresh state mutated the previously returned brief")
	}
}
