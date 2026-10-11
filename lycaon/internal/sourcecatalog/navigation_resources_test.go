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
	_, err := catalog.Directories.ObserveDirectory(t.Context(), "project", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe root", err)
	var snapshots []*Navigation
	for range 3 {
		navigation, err := catalog.Directories.OpenNavigation(t.Context(), "project", root)
		testutil.FailErr(t, "open pinned navigation", err)
		t.Cleanup(func() { testutil.FailErr(t, "release snapshot", navigation.Close()) })
		snapshots = append(snapshots, navigation)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	testutil.FailErr(t, "create unobserved directory", os.Mkdir(filepath.Join(root.Path, "new"), 0700))
	testutil.FailErr(t, "create unobserved source", os.WriteFile(filepath.Join(root.Path, "new", "b.txt"), []byte("new source"), 0600))
	observed, err := catalog.Directories.ObserveDirectory(ctx, "project", root, "new", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
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
	projection, err := catalog.Directories.NewProjectionRows(ctx, "project", root)
	testutil.FailErr(t, "create projection while navigations are pinned", err)
	defer projection.Close()
	testutil.FailErr(t, "write projection alongside pinned navigations", projection.Write(ctx, []ProjectionRecord{{Rank: 0, Address: "a.txt", Parent: ".", End: 1, Body: []byte(`{"name":"a.txt"}`)}}, nil))
	testutil.FailErr(t, "release materialized rows", projection.Release(ctx))
}

func TestNavigationDrainSealsAdmissionAndJoinsReaders(t *testing.T) {
	var resources navigationResources
	root, release, err := resources.Acquire(t.Context(), t.TempDir())
	testutil.FailErr(t, "acquire confined root", err)
	idle := resources.Drain()
	if idle == nil {
		t.Fatal("drain did not retain the active reader")
	}
	if _, _, err := resources.Acquire(t.Context(), t.TempDir()); err == nil {
		t.Fatal("drained navigation admitted a new reader")
	}
	if _, err := root.Stat("."); err != nil {
		testutil.FailErr(t, "reader survived drain", err)
	}
	release()
	release()
	select {
	case <-idle:
	case <-t.Context().Done():
		t.Fatal("released reader did not settle drain")
	}
	if resources.Active() {
		t.Fatal("released reader stayed active")
	}
	if _, err := root.Stat("."); err == nil {
		t.Fatal("drained root descriptor remained open")
	}
}
