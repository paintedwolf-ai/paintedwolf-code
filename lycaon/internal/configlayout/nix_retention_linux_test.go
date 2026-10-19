package configlayout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestNixRetentionReleasesOwnRootAndReclaimsOnlyDeadRoots(t *testing.T) {
	t.Setenv("LYCAON_API_TOKEN", "engine-token-must-not-reach-nix")
	root := t.TempDir()
	tool := filepath.Join(root, "nix-store")
	testutil.FailErr(t, "write registrar", os.WriteFile(tool, []byte("#!/bin/sh\n[ -z \"${LYCAON_API_TOKEN+x}\" ] || exit 1\n[ \"$1\" = --add-root ] || exit 1\nln -s \"$5\" \"$2\"\n"), 0o700))
	layout := &NixLayout{Output: filepath.Join(root, "output"), NixStore: tool}
	directory := filepath.Join(root, "roots")
	releaseFirst, err := retainNix(context.Background(), layout, directory)
	testutil.FailErr(t, "retain first", err)
	defer releaseFirst.Close()
	dead := "session-" + strings.Repeat("0", 32)
	testutil.FailErr(t, "write dead lock", os.WriteFile(filepath.Join(directory, dead+".lock"), nil, 0o600))
	testutil.FailErr(t, "write dead root", os.Symlink("/old-generation", filepath.Join(directory, dead)))
	foreign := filepath.Join(directory, "another-app")
	testutil.FailErr(t, "write foreign root", os.Symlink("/foreign-generation", foreign))
	releaseSecond, err := retainNix(context.Background(), layout, directory)
	testutil.FailErr(t, "retain second", err)
	if _, err := os.Lstat(filepath.Join(directory, dead)); !os.IsNotExist(err) {
		t.Fatal("dead own root was not removed")
	}
	releaseSecond.Close()
	entries, err := os.ReadDir(directory)
	testutil.FailErr(t, "read live roots", err)
	if len(entries) != 4 {
		t.Fatalf("live root was pruned: %v", entries)
	}
	releaseFirst.Close()
	entries, err = os.ReadDir(directory)
	testutil.FailErr(t, "read released roots", err)
	if len(entries) != 2 || entries[0].Name() != "another-app" || entries[1].Name() != "registry.lock" {
		t.Fatalf("foreign state changed or own state retained: %v", entries)
	}
}

func TestNixRetentionRefusesUnavailableDaemonAndUnsafeDirectories(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "nix-store")
	testutil.FailErr(t, "write unavailable registrar", os.WriteFile(tool, []byte("#!/bin/sh\necho 'daemon unavailable' >&2\nexit 1\n"), 0o700))
	layout := &NixLayout{Output: "/generation", NixStore: tool}
	directory := filepath.Join(root, "roots")
	if _, err := retainNix(context.Background(), layout, directory); err == nil {
		t.Fatal("unavailable registration silently accepted")
	}
	entries, err := os.ReadDir(directory)
	testutil.FailErr(t, "read roots", err)
	if len(entries) != 1 || entries[0].Name() != "registry.lock" {
		t.Fatal("failed registration left a root/lock")
	}
	testutil.FailErr(t, "make directory public", os.Chmod(directory, 0o755))
	if _, err := retainNix(context.Background(), layout, directory); err == nil {
		t.Fatal("public directory accepted")
	}
}

func TestDesktopLockRetainsTheRootAfterItsHelperDies(t *testing.T) {
	for _, graceful := range []bool{false, true} {
		t.Run(map[bool]string{false: "abrupt", true: "graceful"}[graceful], func(t *testing.T) {
			testDesktopLockSurvivesHelperExit(t, graceful)
		})
	}
}

func testDesktopLockSurvivesHelperExit(t *testing.T, graceful bool) {
	root := t.TempDir()
	tool := filepath.Join(root, "nix-store")
	testutil.FailErr(t, "write registrar", os.WriteFile(tool, []byte("#!/bin/sh\nln -s \"$5\" \"$2\"\n"), 0o700))
	directory := filepath.Join(root, "roots")
	retention, err := retainNix(context.Background(), &NixLayout{Output: "/selected-generation", NixStore: tool}, directory)
	testutil.FailErr(t, "retain generation", err)
	defer retention.Close()
	desktop, err := os.OpenFile(retention.LockPath, os.O_RDWR, 0)
	testutil.FailErr(t, "open desktop lock", err)
	defer func() { _ = desktop.Close() }()
	testutil.FailErr(t, "hold desktop lock", unix.Flock(int(desktop.Fd()), unix.LOCK_SH|unix.LOCK_NB))
	if graceful {
		retention.Close()
	} else {
		testutil.FailErr(t, "simulate helper death", retention.lock.Close())
	}
	testutil.FailErr(t, "prune while desktop lives", pruneNixRoots(directory))
	link := strings.TrimSuffix(retention.LockPath, ".lock")
	if _, err := os.Readlink(link); err != nil {
		t.Fatalf("live desktop root pruned: %v", err)
	}
	testutil.FailErr(t, "desktop exits", desktop.Close())
	testutil.FailErr(t, "prune dead owner", pruneNixRoots(directory))
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("dead desktop root retained: %v", err)
	}
}
