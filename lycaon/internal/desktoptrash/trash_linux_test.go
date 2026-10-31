//go:build linux

package desktoptrash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
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
	testutil.FailErr(t, "replace shared directory with a symlink", os.RemoveAll(shared))
	testutil.FailErr(t, "create symlink fixture", os.Symlink(t.TempDir(), shared))
	if got := volumeTrashDir(top); got != filepath.Join(top, ".Trash-"+uid) {
		t.Fatalf("symlinked shared Trash selected %q", got)
	}
}

func TestLinuxVolumeTrashFallsBackWhenSharedTrashIsUnusable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("permission fixtures require an unprivileged user")
	}
	uid := fmt.Sprint(os.Getuid())
	top := t.TempDir()
	shared := filepath.Join(top, ".Trash")
	testutil.FailErr(t, "create read-only sticky shared directory", os.Mkdir(shared, 0755))
	testutil.FailErr(t, "mark shared directory read-only and sticky", os.Chmod(shared, 0555|os.ModeSticky))
	t.Cleanup(func() { _ = os.Chmod(shared, 0755) })
	if got := volumeTrashDir(top); got != filepath.Join(top, ".Trash-"+uid) {
		t.Fatalf("unwritable shared Trash selected %q", got)
	}
	if _, err := os.Lstat(filepath.Join(shared, uid)); !os.IsNotExist(err) {
		t.Fatalf("unwritable shared Trash gained a per-user directory: %v", err)
	}

	other := t.TempDir()
	shared = filepath.Join(other, ".Trash")
	testutil.FailErr(t, "create sticky shared directory", os.Mkdir(shared, 0777))
	testutil.FailErr(t, "mark shared directory sticky", os.Chmod(shared, 0777|os.ModeSticky))
	testutil.FailErr(t, "create exposed per-user directory", os.Mkdir(filepath.Join(shared, uid), 0700))
	testutil.FailErr(t, "expose per-user directory", os.Chmod(filepath.Join(shared, uid), 0755))
	if got := volumeTrashDir(other); got != filepath.Join(other, ".Trash-"+uid) {
		t.Fatalf("unsafe per-user directory selected %q", got)
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

func TestLinuxMountinfoParsing(t *testing.T) {
	fixture := strings.Join([]string{
		"short line",
		"10 1 0:1 / / rw no_separator_here",
		"badid 1 0:1 / / rw - ext4 /dev/sda1 rw",
		"12 1 0:1 / relative/mount rw - ext4 /dev/sda1 rw",
		"39 1 0:32 /root / rw,relatime shared:1 - btrfs /dev/vda3 rw,subvol=/root",
		"53 39 0:32 /home /home rw,relatime shared:2 master:1 - btrfs /dev/vda3 rw,subvol=/home",
		`88 53 0:32 / /run/media/u/My\040Disk\011Tab\012NL\134BS\999 rw - btrfs /dev/sdb1 rw`,
	}, "\n")
	for _, tc := range []struct {
		id   uint64
		want string
		ok   bool
	}{
		{39, "/", true}, {53, "/home", true}, {88, "/run/media/u/My Disk\tTab\nNL\\BS\\999", true}, {999, "", false},
	} {
		if got, ok := findMountPoint(strings.NewReader(fixture), tc.id); ok != tc.ok || got != tc.want {
			t.Fatalf("findMountPoint(%d) = (%q, %v), want (%q, %v)", tc.id, got, ok, tc.want, tc.ok)
		}
	}
	if !isWithinMount("/home", "/home/user/subvol") || isWithinMount("/home/user", "/home") {
		t.Fatal("unexpected isWithinMount boundary result")
	}
	if top, err := volumeTopWalk(t.TempDir()); err != nil || !filepath.IsAbs(top) {
		t.Fatalf("volumeTopWalk = %q, %v", top, err)
	}
}

func TestLinuxTrashSameMountSubvolumeRelocation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	prev := relocateGuarded
	relocateGuarded = func(_, to fseffect.Location, _ string) error {
		if dir := filepath.Dir(to.Rel); dir != "." {
			_, _ = fseffect.MkdirAll(fseffect.Location{Root: to.Root, Rel: dir}, 0o700)
		}
		return unix.EXDEV
	}
	t.Cleanup(func() { relocateGuarded = prev })
	subvol := filepath.Join(root, "subvol", "tree")
	testutil.FailErr(t, "create subvolume tree", os.MkdirAll(filepath.Join(subvol, "nested"), 0o700))
	testutil.FailErr(t, "seed regular file", os.WriteFile(filepath.Join(subvol, "nested", "item.txt"), []byte("subvol-data"), 0o600))
	testutil.FailErr(t, "seed symlink", os.Symlink("nested/item.txt", filepath.Join(subvol, "link")))
	receipt, err := Move(t.Context(), subvol)
	testutil.FailErr(t, "trash tree across subvolume boundary", err)
	if filepath.Dir(receipt.Path) != filepath.Join(root, "Trash", "files") {
		t.Fatalf("subvolume item did not use enclosing mount Trash: %q", receipt.Path)
	}
	dest := fseffect.Location{Root: filepath.Join(root, "subvol"), Rel: "tree"}
	testutil.FailErr(t, "occupy destination", os.WriteFile(subvol, []byte("occupied"), 0o600))
	if err := Restore(t.Context(), receipt, dest); !errors.Is(err, os.ErrExist) {
		t.Fatalf("occupied restore = %v, want ErrExist", err)
	}
	testutil.FailErr(t, "clear occupied destination", os.Remove(subvol))
	testutil.FailErr(t, "restore tree across subvolume boundary", Restore(t.Context(), receipt, dest))
	if body, err := os.ReadFile(filepath.Join(subvol, "nested", "item.txt")); err != nil || string(body) != "subvol-data" {
		t.Fatalf("restored file = %q, %v", body, err)
	}
	if link, err := os.Readlink(filepath.Join(subvol, "link")); err != nil || link != "nested/item.txt" {
		t.Fatalf("restored symlink = %q, %v", link, err)
	}
}

func TestLinuxTrashBindMountBoundary(t *testing.T) {
	if os.Getenv("PW_LINUX_TRASH_MOUNTNS_CHILD") == "1" {
		root := os.Getenv("PW_LINUX_TRASH_MOUNTNS_ROOT")
		home, src, mnt := filepath.Join(root, "home"), filepath.Join(root, "src"), filepath.Join(root, "bind mount")
		testutil.FailErr(t, "create dirs", errors.Join(os.MkdirAll(filepath.Join(src, "nested"), 0o700), os.MkdirAll(home, 0o700), os.MkdirAll(mnt, 0o700)))
		testutil.FailErr(t, "bind mount", syscall.Mount(src, mnt, "", syscall.MS_BIND, ""))
		t.Setenv("XDG_DATA_HOME", home)
		if sameDev, err := fspath.SameFilesystem(mnt, home); err != nil || !sameDev {
			t.Fatalf("expected same st_dev for bind mount: same=%v err=%v", sameDev, err)
		}
		if sameMnt, err := sameMount(mnt, home); err != nil || sameMnt {
			t.Fatalf("expected distinct mount IDs for bind mount: same=%v err=%v", sameMnt, err)
		}
		path := filepath.Join(mnt, "nested", "item.txt")
		testutil.FailErr(t, "seed bind mount file", os.WriteFile(path, []byte("bound"), 0o600))
		receipt, err := Move(t.Context(), path)
		testutil.FailErr(t, "trash file on bind mount", err)
		if want := filepath.Join(mnt, fmt.Sprintf(".Trash-%d", os.Getuid()), "files"); filepath.Dir(receipt.Path) != want {
			t.Fatalf("bind mount trash path = %q, want under %q", receipt.Path, want)
		}
		testutil.FailErr(t, "restore file on bind mount", Restore(t.Context(), receipt, fseffect.Location{Root: mnt, Rel: filepath.Join("nested", "item.txt")}))
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxTrashBindMountBoundary$")
	cmd.Env = append(os.Environ(), "PW_LINUX_TRASH_MOUNTNS_CHILD=1", "PW_LINUX_TRASH_MOUNTNS_ROOT="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS, AmbientCaps: []uintptr{unix.CAP_SYS_ADMIN},
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mountns bind-mount test failed: %v\n%s", err, out)
	}
}
