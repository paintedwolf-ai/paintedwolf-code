//go:build linux

package desktoptrash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxTrashMetadataAndRecovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	name := "space and\nnewline"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("kept"), 0600); err != nil {
		testutil.FailErr(t, "seed native Trash file", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Move(canceled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled move = %v, want cancellation", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "kept" {
		t.Fatalf("canceled move changed source: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Trash")); !os.IsNotExist(err) {
		t.Fatalf("canceled move created Trash: %v", err)
	}
	receipt, err := Move(t.Context(), path)
	if err != nil {
		testutil.FailErr(t, "move file to native Trash", err)
	}
	metadata, err := os.ReadFile(receipt.Metadata)
	if err != nil {
		testutil.FailErr(t, "read native Trash metadata", err)
	}
	if !strings.Contains(string(metadata), "Path="+encodeTrashPath(path)+"\n") {
		t.Fatalf("incorrect metadata: %q", metadata)
	}
	destination := fseffect.Location{Root: root, Rel: name}
	if err := Restore(canceled, receipt, destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restoration = %v, want cancellation", err)
	}
	tampered := receipt
	tampered.Identity += "-different-entry"
	if err := Restore(t.Context(), tampered, destination); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed receipt identity = %v, want unavailable", err)
	}
	testutil.FailErr(t, "occupy restoration destination", os.WriteFile(path, []byte("new entry"), 0600))
	if err := Restore(t.Context(), receipt, destination); err == nil {
		t.Fatal("restoration overwrote an occupied destination")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "new entry" {
		t.Fatalf("refused restoration changed destination: %q, %v", body, err)
	}
	if body, err := os.ReadFile(receipt.Path); err != nil || string(body) != "kept" {
		t.Fatalf("refused restorations changed Trash entry: %q, %v", body, err)
	}
	if info, err := os.ReadFile(receipt.Metadata); err != nil || string(info) != string(metadata) {
		t.Fatalf("refused restorations changed recovery metadata: %q, %v", info, err)
	}
	testutil.FailErr(t, "release restoration destination", os.Remove(path))
	if err := Restore(t.Context(), receipt, destination); err != nil {
		testutil.FailErr(t, "restore native Trash receipt", err)
	}
	if _, err := os.Stat(receipt.Metadata); !os.IsNotExist(err) {
		t.Fatalf("native metadata remains: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "kept" {
		t.Fatalf("restored contents = %q, %v", body, err)
	}
	if err := Restore(t.Context(), receipt, destination); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("consumed receipt = %v, want unavailable", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "kept" {
		t.Fatalf("receipt replay changed restored contents: %q, %v", body, err)
	}
}

func TestLinuxTrashPathEncodingKeepsSeparators(t *testing.T) {
	got := encodeTrashPath("/home/user/My Docs/ünïcode #1%/plan?.txt")
	want := "/home/user/My%20Docs/%C3%BCn%C3%AFcode%20%231%25/plan%3F.txt"
	if got != want {
		t.Fatalf("encoded path = %q, want %q", got, want)
	}
	if got := encodeTrashPath("relative/dir name/file"); got != "relative/dir%20name/file" {
		t.Fatalf("relative path encoding = %q", got)
	}
}

func TestLinuxTrashRejectsUnsafeDirectories(t *testing.T) {
	for _, mode := range []string{"symlink", "public"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_DATA_HOME", root)
			trash := filepath.Join(root, "Trash")
			if mode == "symlink" {
				if err := os.Symlink(t.TempDir(), trash); err != nil {
					testutil.FailErr(t, "create unsafe symlink fixture", err)
				}
			} else {
				if err := os.Mkdir(trash, 0700); err != nil {
					testutil.FailErr(t, "create Trash fixture", err)
				}
				if err := os.Chmod(trash, 0755); err != nil {
					testutil.FailErr(t, "make Trash fixture public", err)
				}
			}
			path := filepath.Join(root, "original")
			if err := os.WriteFile(path, []byte("kept"), 0600); err != nil {
				testutil.FailErr(t, "seed original file", err)
			}
			if _, err := Move(t.Context(), path); err == nil {
				t.Fatal("unsafe Trash accepted")
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "kept" {
				t.Fatalf("source changed: %q, %v", body, err)
			}
		})
	}
}

func TestLinuxTrashReportsPermissionFailureWithoutMoving(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("permission fixtures require an unprivileged user")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	testutil.FailErr(t, "create read-only data home", os.Mkdir(locked, 0500))
	t.Cleanup(func() { _ = os.Chmod(locked, 0700) })
	t.Setenv("XDG_DATA_HOME", locked)
	path := filepath.Join(root, "original")
	testutil.FailErr(t, "seed original file", os.WriteFile(path, []byte("kept"), 0600))
	receipt, err := Move(t.Context(), path)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-only Trash home = %v, want a permission error", err)
	}
	if receipt.Path != "" {
		t.Fatalf("failed move returned a recovery location: %+v", receipt)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "kept" {
		t.Fatalf("failed move changed source: %q, %v", body, err)
	}
	if entries, err := os.ReadDir(locked); err != nil || len(entries) != 0 {
		t.Fatalf("failed move wrote into the read-only home: %v, %v", entries, err)
	}
}

func TestLinuxVolumeTrashDirectoryFollowsStickyAdministration(t *testing.T) {
	top := t.TempDir()
	uid := fmt.Sprint(os.Getuid())
	if got := volumeTrashDir(top); got != filepath.Join(top, ".Trash-"+uid) {
		t.Fatalf("missing shared Trash selected %q", got)
	}
	shared := filepath.Join(top, ".Trash")
	testutil.FailErr(t, "create plain shared directory", os.Mkdir(shared, 0777))
	if got := volumeTrashDir(top); got != filepath.Join(top, ".Trash-"+uid) {
		t.Fatalf("non-sticky shared Trash selected %q", got)
	}
	testutil.FailErr(t, "mark shared directory sticky", os.Chmod(shared, 0777|os.ModeSticky))
	if got := volumeTrashDir(top); got != filepath.Join(shared, uid) {
		t.Fatalf("sticky shared Trash selected %q", got)
	}
	testutil.FailErr(t, "replace shared directory with a symlink", os.Remove(shared))
	testutil.FailErr(t, "create symlink fixture", os.Symlink(t.TempDir(), shared))
	if got := volumeTrashDir(top); got != filepath.Join(top, ".Trash-"+uid) {
		t.Fatalf("symlinked shared Trash selected %q", got)
	}
}

// Entries on another filesystem than the data home stay on their own volume
// with a relative recorded path, and recovery moves them back without copying.
func TestLinuxTrashKeepsOtherVolumesInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	volume, err := os.MkdirTemp("/dev/shm", "paintedwolf-trash-") //nolint:usetesting // The entry must be on a separate filesystem.
	testutil.FailErr(t, "create separate-volume fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(volume) })
	if same, err := fspath.SameFilesystem(volume, home); err != nil || same {
		t.Fatalf("fixture is not on a separate filesystem: same=%t err=%v", same, err)
	}
	top, err := volumeTop(filepath.Join(volume, "entry"))
	testutil.FailErr(t, "resolve volume top", err)
	trash := volumeTrashDir(top)
	if _, err := os.Lstat(trash); os.IsNotExist(err) {
		t.Cleanup(func() { _ = os.RemoveAll(trash) })
	}
	path := filepath.Join(volume, "nested dir", "entry ü.txt")
	testutil.FailErr(t, "create nested directory", os.MkdirAll(filepath.Dir(path), 0700))
	testutil.FailErr(t, "seed volume entry", os.WriteFile(path, []byte("kept"), 0600))
	before, err := fspath.EntryIdentity(path)
	testutil.FailErr(t, "record entry identity", err)
	receipt, err := Move(t.Context(), path)
	testutil.FailErr(t, "trash entry on another volume", err)
	t.Cleanup(func() { _ = os.Remove(receipt.Path); _ = os.Remove(receipt.Metadata) })
	if filepath.Dir(receipt.Path) != filepath.Join(trash, "files") {
		t.Fatalf("entry left its volume: %q", receipt.Path)
	}
	if receipt.Identity != before {
		t.Fatalf("trash copied instead of moving: %q != %q", receipt.Identity, before)
	}
	metadata, err := os.ReadFile(receipt.Metadata)
	testutil.FailErr(t, "read volume metadata", err)
	relative, err := filepath.Rel(top, path)
	testutil.FailErr(t, "compute relative origin", err)
	if !strings.Contains(string(metadata), "Path="+encodeTrashPath(relative)+"\n") {
		t.Fatalf("volume metadata = %q, want relative path %q", metadata, relative)
	}
	if _, err := os.Stat(filepath.Join(home, "Trash")); !os.IsNotExist(err) {
		t.Fatalf("home Trash was created for another volume: %v", err)
	}
	testutil.FailErr(t, "restore volume entry", Restore(t.Context(), receipt, fseffect.Location{Root: volume, Rel: filepath.Join("nested dir", "entry ü.txt")}))
	after, err := fspath.EntryIdentity(path)
	testutil.FailErr(t, "inspect restored entry", err)
	if after != before {
		t.Fatal("restoration replaced the entry instead of moving it back")
	}
	if _, err := os.Stat(receipt.Metadata); !os.IsNotExist(err) {
		t.Fatalf("volume metadata remains after restoration: %v", err)
	}
}

func TestLinuxTrashPreservesMaximumLengthName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "relative-data-home")
	t.Setenv("HOME", root)
	name := strings.Repeat("x", 255)
	path := filepath.Join(root, name)
	testutil.FailErr(t, "create maximum length file", os.WriteFile(path, []byte("kept"), 0600))
	receipt, err := Move(t.Context(), path)
	testutil.FailErr(t, "trash maximum length file", err)
	if filepath.Dir(receipt.Path) != filepath.Join(root, ".local", "share", "Trash", "files") {
		t.Fatalf("relative XDG data home did not fall back to HOME: %q", receipt.Path)
	}
	testutil.FailErr(t, "restore original name", Restore(t.Context(), receipt, fseffect.Location{Root: root, Rel: name}))
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if string(body) != "kept" {
		t.Fatal("recovery lost contents")
	}
	t.Setenv("HOME", "")
	if _, err := Move(t.Context(), path); err == nil {
		t.Fatal("Trash accepted relative XDG data home without a home directory")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "kept" {
		t.Fatalf("unavailable Trash changed source: %q, %v", body, err)
	}
}
