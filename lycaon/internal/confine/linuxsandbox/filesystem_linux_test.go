//go:build linux

package linuxsandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
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
		verifySharedTempRefusals(t, fs)
		for _, path := range strings.Split(os.Getenv("PW_METADATA_ALLOW_BACKS"), "\n") {
			if err := unix.Chmod(path, 0o640); err != nil {
				t.Errorf("exact allow-back %s refused: %v", path, err)
			}
		}
		return
	}
	base := t.TempDir()
	shared := filepath.Join(base, "shared")
	testutil.FailErr(t, "create shared directory", os.Mkdir(shared, 0700))
	testutil.FailErr(t, "seed shared file", os.WriteFile(filepath.Join(shared, "existing"), []byte("original"), 0600))
	fs := Filesystem{SharedTemp: []string{shared}}
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

func verifySharedTempRefusals(t *testing.T, fs Filesystem) {
	t.Helper()
	root := fs.SharedTemp[0]
	dir := openDirFD(t, root)
	floors := newCreationFloors(fs)
	existing := filepath.Join(root, "existing")
	entry := filepath.Join(root, "create")
	for _, tc := range []struct {
		call              pathCall
		target, operation string
	}{
		{pathCall{Op: callOpen, Path: "create", Flags: unix.O_CREAT | unix.O_WRONLY, Mode: 0600}, entry, refusalCreate},
		{pathCall{Op: callUnlink, Path: "existing"}, existing, refusalUnlink},
		{pathCall{Op: callUnlink, Path: existing}, existing, refusalUnlink},
		{pathCall{Op: callMkdir, Path: entry, Mode: 0700}, entry, refusalCreate},
		{pathCall{Op: callMknod, Path: entry, Mode: unix.S_IFIFO | 0600}, entry, refusalCreate},
		{pathCall{Op: callSymlink, Path: entry, Content: existing}, entry, refusalCreate},
		{pathCall{Op: callOpen, Path: existing, Flags: unix.O_WRONLY}, existing, refusalCreate},
		{pathCall{Op: callTruncate, Path: existing}, existing, refusalCreate},
		{pathCall{Op: callMetadata, Path: "existing", Content: "file-write-mode"}, existing, "file-write-mode"},
		{pathCall{Op: callMetadata, Content: "file-write-owner"}, root, "file-write-owner"},
	} {
		result, opened := floors.perform(tc.call, []int{dir})
		if opened >= 0 {
			_ = unix.Close(opened)
		}
		if result.Errno != int(unix.EROFS) || result.Refusal == nil || result.Refusal.Target != tc.target || result.Refusal.Operation != tc.operation {
			t.Errorf("%s %q: result %+v, refusal %+v", tc.call.Op, tc.call.Path, result, result.Refusal)
		}
	}
	for _, call := range []pathCall{{Op: callMetadata, Path: "missing"}, {Op: callMetadata, Path: fs.WriteFiles[0]}} {
		if result, _ := floors.perform(call, []int{dir}); result.Errno != 0 || result.Refusal != nil {
			t.Errorf("metadata at %s raised a shared temp refusal: %+v", call.Path, result)
		}
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "original" {
		t.Fatalf("shared file after refusal: %q, %v", data, err)
	}
}
