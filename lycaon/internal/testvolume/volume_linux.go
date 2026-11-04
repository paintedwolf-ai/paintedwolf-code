//go:build linux

package testvolume

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	lexec "github.com/lycaon/lycaon/internal/exec"
)

const (
	namespaceChildEnv = "PW_TESTVOLUME_NAMESPACE_CHILD"
	unsharePath       = "/usr/bin/unshare"
)

// New returns a real second volume. Mounting needs a private namespace, so a test
// on Linux re-runs itself inside one. The second result is true in the parent once
// that run has passed, and the caller returns.
func New(t *testing.T, kind Kind) (*Volume, bool) {
	t.Helper()
	if kind == Clonable {
		return reflinkVolume(t), false
	}
	if os.Getenv(namespaceChildEnv) == "" {
		runInNamespace(t)
		return nil, true
	}
	return tmpfsVolume(t), false
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
	if _, err := os.Stat(unsharePath); err != nil {
		t.Skipf("unshare unavailable: %v", err)
	}
	name, _, _ := strings.Cut(t.Name(), "/")
	args := []string{"--user", "--map-root-user", "--mount", os.Args[0], "-test.run", "^" + name + "$", "-test.v", "-test.count=1"}
	// A test binary writes counters only when asked for a profile. go test never
	// merges its own directory, so a coverage run names another.
	if dir := os.Getenv("PW_GO_CHILD_COVERDIR"); dir != "" {
		args = append(args, "-test.gocoverdir="+dir, "-test.coverprofile="+filepath.Join(t.TempDir(), "namespace.out"))
	}
	env := append(os.Environ(), namespaceChildEnv+"=1")
	output, code, err := lexec.Run(t.Context(), unsharePath, args, lexec.ExecOpts{
		Launch:  lexec.HostLaunch("test volume namespace"),
		Env:     env,
		Timeout: 5 * time.Minute,
	})
	text := string(output)
	if err != nil {
		t.Fatalf("private namespace run: %v\n%s", err, text)
	}
	// With no test output, unshare itself was refused: the host forbids namespaces.
	if code != 0 && !strings.Contains(text, "=== RUN") {
		t.Skipf("private mount namespaces are unavailable: %s", text)
	}
	if code != 0 {
		t.Fatalf("private namespace run failed:\n%s", text)
	}
	t.Logf("private namespace run:\n%s", text)
	if strings.Contains(text, "--- SKIP: "+name) {
		t.Skip("private namespace run skipped")
	}
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
