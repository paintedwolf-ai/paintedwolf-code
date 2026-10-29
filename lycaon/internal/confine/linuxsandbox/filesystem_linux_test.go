//go:build linux

package linuxsandbox

import (
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestMetadataWriteRestorationsFollowPlanGrantsAndMasks(t *testing.T) {
	fs := Filesystem{WriteRoots: []string{"/project", "/project/masked/approved"}, WriteFiles: []string{"/project/masked/exact"}, ReadOnly: []string{"/project/masked"}, Hidden: []string{"/project/hidden"}, Restored: []string{"/project/hidden/readable"}}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/project", true}, {"/project/file", true}, {"/project/masked", false}, {"/project/masked/file", false},
		{"/project/masked/approved/file", true}, {"/project/masked/exact", true}, {"/project/masked/exact/child", false},
		{"/project/hidden/readable", false}, {"/project/hidden/readable/file", false}, {"/project-other", false}, {"/Project", false}, {"/outside", false},
	} {
		if got := metadataWritable(fs, tc.path); got != tc.want {
			t.Errorf("metadataWritable(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestFilesystemMountOrderingPreservesExactAllowBacks(t *testing.T) {
	if raw := os.Getenv("PW_METADATA_MOUNT_PLAN"); raw != "" {
		runtime.LockOSThread()
		var fs Filesystem
		if err := json.Unmarshal([]byte(raw), &fs); err != nil {
			t.Fatal("decode mount fixture:", err)
		}
		if err := makeRootPrivate(); err != nil {
			t.Fatal("isolate mount fixture:", err)
		}
		if err := applyFilesystem(fs); err != nil {
			t.Fatal("apply mount fixture:", err)
		}
		for _, path := range strings.Split(os.Getenv("PW_METADATA_ALLOW_BACKS"), "\n") {
			if err := unix.Chmod(path, 0o640); err != nil {
				t.Errorf("exact allow-back %s refused: %v", path, err)
			}
		}
		return
	}
	base := t.TempDir()
	fs := Filesystem{}
	var paths []string
	for _, mask := range []string{"read-only", "hidden"} {
		for _, kind := range []string{"file", "directory"} {
			path := filepath.Join(base, mask+"-"+kind)
			target := path
			if kind == "directory" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal("create mount fixture directory:", err)
				}
				target = filepath.Join(path, "file")
				fs.WriteRoots = append(fs.WriteRoots, path)
			} else {
				fs.WriteFiles = append(fs.WriteFiles, path)
			}
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal("seed mount fixture:", err)
			}
			if mask == "read-only" {
				fs.ReadOnly = append(fs.ReadOnly, path)
			} else {
				fs.Hidden = append(fs.Hidden, path)
			}
			fs.Restored = append(fs.Restored, path)
			paths = append(paths, target)
		}
	}
	args := []string{"-test.run=^TestFilesystemMountOrderingPreservesExactAllowBacks$"}
	// A coverage build emits counters at exit, so the sealed root view keeps
	// that directory writable and the child never creates a temporary one.
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		fs.WriteRoots = append(fs.WriteRoots, dir)
		args = append(args, "-test.gocoverdir="+dir)
	}
	raw, err := json.Marshal(fs)
	if err != nil {
		t.Fatal("encode mount fixture:", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal("locate mount fixture binary:", err)
	}
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(), "PW_METADATA_MOUNT_PLAN="+string(raw), "PW_METADATA_ALLOW_BACKS="+strings.Join(paths, "\n"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}, GidMappingsEnableSetgroups: false, AmbientCaps: []uintptr{unix.CAP_SYS_ADMIN}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mount fixture failed: %v\n%s", err, out)
	}
}
