package workerplacement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnknownRegistryFormatIsRefusedUnchanged(t *testing.T) {
	state := t.TempDir()
	path := registryPath(state)
	raw := []byte(`{"format_version":99,"folders":[{"id":"x"}]}`)
	testutil.FailErr(t, "write", os.WriteFile(path, raw, 0o600))
	if _, err := Load(state); !errors.Is(err, ErrUnknownRegistryFormat) {
		t.Fatalf("load err = %v", err)
	}
	if _, err := Purge(context.Background(), state); !errors.Is(err, ErrUnknownRegistryFormat) {
		t.Fatalf("purge err = %v", err)
	}
	if err := RecordBranch(context.Background(), state, "x", "k", "j"); !errors.Is(err, ErrUnknownRegistryFormat) {
		t.Fatalf("record err = %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read", err)
	if string(after) != string(raw) {
		t.Fatalf("registry changed: %s", after)
	}
}

func placedFolder(t *testing.T, state string) Folder {
	t.Helper()
	path := filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(os.Getuid(), state))
	folder, created, err := reserve(context.Background(), state, path, filepath.Dir(path), KindVolumeRoot)
	testutil.FailErr(t, "reserve", err)
	if !created {
		t.Fatal("reserve did not create the folder")
	}
	return folder
}

func TestRecordAndResolveAcrossLogicalAndPhysical(t *testing.T) {
	state := t.TempDir()
	folder := placedFolder(t, state)
	testutil.FailErr(t, "record", RecordBranch(context.Background(), state, folder.ID, "abc", "job"))

	root, placed, err := PhysicalBranchRoot(state, "abc", "job")
	testutil.FailErr(t, "physical", err)
	if !placed || root != filepath.Join(folder.BranchesRoot(), "abc", "job") {
		t.Fatalf("root = %s, placed = %v", root, placed)
	}
	logical, ok, err := LogicalFromPhysical(state, root)
	testutil.FailErr(t, "logical", err)
	if !ok || logical != "worker-branches/abc/job" {
		t.Fatalf("logical = %s, %v", logical, ok)
	}
	if _, ok, err := LogicalFromPhysical(state, filepath.Join(t.TempDir(), "worker-branches", "abc", "job")); err != nil || ok {
		t.Fatalf("unregistered path resolved: %v, %v", ok, err)
	}
	unrecorded, err := PhysicalFromLogical(state, "worker-branches/abc/other")
	testutil.FailErr(t, "unrecorded", err)
	if unrecorded != filepath.Join(state, "worker-branches", "abc", "other") {
		t.Fatalf("unrecorded = %s", unrecorded)
	}
}

func TestLastForgottenBranchRemovesTheFolder(t *testing.T) {
	state := t.TempDir()
	folder := placedFolder(t, state)
	ctx := context.Background()
	testutil.FailErr(t, "record a", RecordBranch(ctx, state, folder.ID, "k", "a"))
	testutil.FailErr(t, "record b", RecordBranch(ctx, state, folder.ID, "k", "b"))
	testutil.FailErr(t, "forget a", ForgetBranch(ctx, state, "k", "a"))
	if !Available(folder) {
		t.Fatal("folder removed while a branch remains")
	}
	testutil.FailErr(t, "forget b", ForgetBranch(ctx, state, "k", "b"))
	if _, err := os.Lstat(folder.Path); !os.IsNotExist(err) {
		t.Fatalf("empty folder remains: %v", err)
	}
	if _, err := os.Lstat(registryPath(state)); !os.IsNotExist(err) {
		t.Fatalf("empty registry remains: %v", err)
	}
}

func TestPurgeLeavesAFolderItCannotReachRecorded(t *testing.T) {
	state := t.TempDir()
	reachable := placedFolder(t, state)
	absent := placedFolder(t, state)
	testutil.FailErr(t, "hide", os.Rename(absent.Path, absent.Path+"-away"))
	ctx := context.Background()
	report, err := Purge(ctx, state)
	testutil.FailErr(t, "purge", err)
	if len(report.Removed) != 1 || report.Removed[0] != reachable.Path {
		t.Fatalf("removed = %v", report.Removed)
	}
	if len(report.Unavailable) != 1 || report.Unavailable[0] != absent.Path {
		t.Fatalf("unavailable = %v", report.Unavailable)
	}
	registry, err := Load(state)
	testutil.FailErr(t, "load", err)
	if len(registry.Folders) != 1 || registry.Folders[0].ID != absent.ID {
		t.Fatalf("registry = %+v", registry)
	}
}

func TestPurgeRefusesAFolderWhoseMarkerDoesNotMatch(t *testing.T) {
	state := t.TempDir()
	folder := placedFolder(t, state)
	testutil.FailErr(t, "foreign marker", writeMarker(folder.Path, "someone-else", state))
	report, err := Purge(context.Background(), state)
	testutil.FailErr(t, "purge", err)
	if len(report.Removed) != 0 || len(report.Unavailable) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Lstat(folder.Path); err != nil {
		t.Fatalf("folder removed: %v", err)
	}
}

func TestFolderFoundAtANewMountPointKeepsItsIdentity(t *testing.T) {
	state := t.TempDir()
	folder := placedFolder(t, state)
	ctx := context.Background()
	testutil.FailErr(t, "record", RecordBranch(ctx, state, folder.ID, "k", "j"))
	moved := filepath.Join(t.TempDir(), filepath.Base(folder.Path))
	testutil.FailErr(t, "move", os.Rename(folder.Path, moved))
	again, created, err := reserve(ctx, state, moved, filepath.Dir(moved), KindVolumeRoot)
	testutil.FailErr(t, "reserve", err)
	if created || again.ID != folder.ID {
		t.Fatalf("again = %+v, created = %v", again, created)
	}
	root, placed, err := PhysicalBranchRoot(state, "k", "j")
	testutil.FailErr(t, "physical", err)
	if !placed || !strings.HasPrefix(root, moved) {
		t.Fatalf("root = %s, placed = %v", root, placed)
	}
}

func TestAnotherInstallationsFolderIsNotAdopted(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	folder := placedFolder(t, first)
	if _, _, err := reserve(context.Background(), second, folder.Path, filepath.Dir(folder.Path), KindVolumeRoot); err == nil {
		t.Fatal("reserved another installation's folder")
	}
}

func TestAssertOutsideRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	testutil.FailErr(t, "mkdir", os.MkdirAll(root, 0o700))
	for _, path := range []string{root, filepath.Join(root, "a", "b")} {
		if err := AssertOutsideRoots(path, []string{root}); !errors.Is(err, ErrInsideAttachedRoot) {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, path := range []string{base, root + "-other", filepath.Join(base, "other")} {
		testutil.FailErr(t, path, AssertOutsideRoots(path, []string{root}))
	}
}

func TestNoLocationOutsideRootsIsTyped(t *testing.T) {
	state := t.TempDir()
	_, err := Decide(context.Background(), Request{
		StateRoot:     state,
		StateBranches: filepath.Join(state, "worker-branches"),
		AttachedRoots: []string{string(filepath.Separator)},
		SourceRoots:   []string{string(filepath.Separator)},
		Probe:         func(context.Context, string) (bool, error) { return false, nil },
	})
	if !errors.Is(err, ErrNoLocationOutsideRoots) {
		t.Fatalf("err = %v", err)
	}
}
