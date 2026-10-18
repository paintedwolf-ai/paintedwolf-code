package configlayout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
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
	defer releaseFirst()
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
	releaseSecond()
	entries, err := os.ReadDir(directory)
	testutil.FailErr(t, "read live roots", err)
	if len(entries) != 4 {
		t.Fatalf("live root was pruned: %v", entries)
	}
	releaseFirst()
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
