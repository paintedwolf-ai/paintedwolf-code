//go:build darwin || linux

package testvolume

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVolumeKeepsItsContentsAcrossEjectAndRemount(t *testing.T) {
	volume, ran := New(t, Movable)
	if ran {
		return
	}
	dir := volume.Dir(t, "project")
	path := filepath.Join(dir, "kept.txt")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte("kept"), 0o600))
	volume.Eject(t)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("file visible while ejected: %v", err)
	}
	volume.Remount(t)
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read after remount", err)
	if string(raw) != "kept" {
		t.Fatalf("contents = %q", raw)
	}
	volume.MakeReadOnly(t)
	if err := os.WriteFile(filepath.Join(volume.Root, "new.txt"), nil, 0o600); err == nil {
		t.Fatal("wrote to a read-only volume")
	}
}

func TestNoCloneVolumeIsASeparateFilesystem(t *testing.T) {
	volume, ran := New(t, NoClone)
	if ran {
		return
	}
	mounted, err := os.Stat(volume.Root)
	testutil.FailErr(t, "stat volume", err)
	parent, err := os.Stat(filepath.Dir(volume.Root))
	testutil.FailErr(t, "stat parent", err)
	if os.SameFile(mounted, parent) {
		t.Fatal("volume is its parent")
	}
}

func TestSharedScopeNamesAreUniqueAndFilesystemSafe(t *testing.T) {
	first := sharedScopeName(t)
	second := sharedScopeName(t)
	if first == second {
		t.Fatalf("two scopes share a name: %s", first)
	}
	for _, name := range []string{first, second} {
		if !strings.HasPrefix(name, "t") {
			t.Fatalf("scope name lacks its counter: %s", name)
		}
		if strings.ContainsAny(name, "/: ") {
			t.Fatalf("scope name keeps unsafe characters: %s", name)
		}
	}
}
