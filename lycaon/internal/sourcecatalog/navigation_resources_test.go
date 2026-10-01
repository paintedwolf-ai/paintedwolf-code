package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPinnedNavigationsDoNotBlockStructuralOrProjectionWrites(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := Root{ID: "root", Path: t.TempDir()}
	catalog := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	testutil.FailErr(t, "create source", os.WriteFile(filepath.Join(root.Path, "a.txt"), []byte("source"), 0600))
	_, err := catalog.ObserveDirectory(t.Context(), "project", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe root", err)
	var snapshots []*Navigation
	for range 3 {
		navigation, err := catalog.OpenNavigation(t.Context(), "project", root)
		testutil.FailErr(t, "open pinned navigation", err)
		t.Cleanup(func() { testutil.FailErr(t, "release snapshot", navigation.Close()) })
		snapshots = append(snapshots, navigation)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	testutil.FailErr(t, "create unobserved directory", os.Mkdir(filepath.Join(root.Path, "new"), 0700))
	testutil.FailErr(t, "create unobserved source", os.WriteFile(filepath.Join(root.Path, "new", "b.txt"), []byte("new source"), 0600))
	observed, err := catalog.ObserveDirectory(ctx, "project", root, "new", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "publish directory while navigations are pinned", err)
	if !observed.Complete {
		t.Fatal("directory publication did not finish")
	}
	for _, snapshot := range snapshots {
		_, err := snapshot.Entry(ctx, "new/b.txt")
		if !errors.Is(err, pagedview.ErrMissing) {
			t.Fatalf("pinned snapshot changed after publication: %v", err)
		}
	}
	projection, err := catalog.NewProjectionRows(ctx, "project", root)
	testutil.FailErr(t, "create projection while navigations are pinned", err)
	defer projection.Close()
	testutil.FailErr(t, "write projection alongside pinned navigations", projection.Write(ctx, []ProjectionRecord{{Rank: 0, Address: "a.txt", Parent: ".", End: 1, Body: []byte(`{"name":"a.txt"}`)}}, nil))
	testutil.FailErr(t, "release materialized rows", projection.Release(ctx))
}
