//go:build linux

package testvolume

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const namespaceChildEnv = "PW_TESTVOLUME_NAMESPACE_CHILD"

// New returns a real second volume. Mounting needs a private namespace, so a test
// on Linux re-runs itself inside one and the parent reports the child's outcome.
func New(t *testing.T, kind Kind) *Volume {
	t.Helper()
	if kind == Clonable {
		return reflinkVolume(t)
	}
	if os.Getenv(namespaceChildEnv) == "" {
		runInNamespace(t)
	}
	return tmpfsVolume(t)
}

func reflinkVolume(t *testing.T) *Volume {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv(ReflinkVolumeEnv))
	if dir == "" {
		t.Skipf("%s is not set; no reflink volume can be made without elevation", ReflinkVolumeEnv)
	}
	root, err := os.MkdirTemp(dir, ".pw-placement-test-")
	if err != nil {
		t.Fatalf("create test directory on reflink volume: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove test directory on reflink volume: %v", err)
		}
	})
	unavailable := func(t *testing.T) { t.Fatal("the configured reflink volume cannot be ejected by a test") }
	return &Volume{Root: root, eject: unavailable, remount: unavailable}
}

func runInNamespace(t *testing.T) {
	t.Helper()
	name, _, _ := strings.Cut(t.Name(), "/")
	cmd := exec.Command(os.Args[0], "-test.run", "^"+name+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), namespaceChildEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		t.Fatalf("private namespace run failed: %v\n%s", err, output.String())
	case err != nil:
		t.Skipf("private mount namespaces are unavailable: %v", err)
	}
	t.Logf("private namespace run:\n%s", output.String())
	if strings.Contains(output.String(), "--- SKIP: "+name) {
		t.Skip("private namespace run skipped")
	}
	// The test already ran in the child. This is FailNow's exit without the failure.
	runtime.Goexit()
}

func tmpfsVolume(t *testing.T) *Volume {
	t.Helper()
	// Every mount here disappears with the process, so a killed test leaves nothing.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Skipf("cannot make mounts private: %v", err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	store := filepath.Join(base, "store")
	mount := filepath.Join(base, "mount")
	for _, dir := range []string{store, mount} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := unix.Mount("tmpfs", store, "tmpfs", 0, "size=32m"); err != nil {
		t.Skipf("cannot mount tmpfs: %v", err)
	}
	// A bind of the store can go away while its contents stay, like an ejected disk.
	bind := func(t *testing.T) {
		if err := unix.Mount(store, mount, "", unix.MS_BIND, ""); err != nil {
			t.Fatalf("mount test volume: %v", err)
		}
	}
	v := &Volume{Root: mount}
	v.eject = func(t *testing.T) {
		if err := unix.Unmount(mount, 0); err != nil {
			t.Fatalf("eject test volume: %v", err)
		}
	}
	v.remount = bind
	v.Remount(t)
	t.Cleanup(func() {
		_ = unix.Unmount(mount, unix.MNT_DETACH)
		_ = unix.Unmount(store, unix.MNT_DETACH)
	})
	return v
}

// MakeReadOnly remounts the volume read-only.
func (v *Volume) MakeReadOnly(t *testing.T) {
	t.Helper()
	if err := unix.Mount("", v.Root, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		t.Fatalf("remount test volume read-only: %v", err)
	}
}
