package linuxruntime

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func fixtureFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

func TestAcquisitionRequiresImageOrResourceScope(t *testing.T) {
	origin := t.TempDir()
	image := filepath.Join(origin, "missing", "rootfs", "init")
	if err := Acquire(image); err == nil {
		t.Fatal("activated missing packaged guest")
	}
	if _, err := processGroup("missing-process"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing process group = %v", err)
	}
	fixtureFile(t, origin, "cgroup.subtree_control", "memory pids")
	if err := activate(image, origin); err == nil {
		t.Fatal("ordinary directory accepted as kernel resource domain")
	}
	leaves, err := filepath.Glob(filepath.Join(origin, "pw-session-*"))
	if err != nil || len(leaves) != 1 {
		t.Fatalf("resource setup left %v, %v", leaves, err)
	}
	for name, bound := range limits() {
		data, err := os.ReadFile(filepath.Join(leaves[0], name))
		if err != nil || string(data) != strconv.FormatInt(bound, 10) {
			t.Errorf("%s bound = %q, %v", name, data, err)
		}
	}
	if err := startLeaf(image, filepath.Join(origin, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent resource scope = %v", err)
	}
}

func TestResourceManagerRequiresSameUIDAndNestedDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager")
	manager := "/usr/bin/systemd-run"
	got, err := managerForOrigin(manager, path, hierarchy)
	if err != nil || got != "" {
		t.Fatalf("absent manager selected %q, %v", got, err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on private manager socket: %v", err)
	}
	defer listener.Close()
	origin, err := processGroup("self")
	if err != nil {
		// Some cgroup namespaces report paths relative to an inaccessible host root.
		if _, err := managerForOrigin(manager, path, hierarchy); err == nil {
			t.Fatal("accepted manager with unverifiable cgroup")
		}
		return
	}
	for _, tc := range []struct{ origin, want string }{{origin, manager}, {origin + "/child", ""}} {
		got, err := managerForOrigin(manager, path, tc.origin)
		if got != tc.want || err != nil {
			t.Errorf("manager for %s = %q, %v", tc.origin, got, err)
		}
	}
}

func TestControllerMustBelongToBoundedDomain(t *testing.T) {
	group := t.TempDir()
	pid := strconv.Itoa(os.Getpid())
	reset := func() {
		fixtureFile(t, group, "cgroup.procs", "1\n"+pid+"\n")
		fixtureFile(t, group, "memory.max", "536870912\n")
		fixtureFile(t, group, "pids.max", "256\n")
	}
	reset()
	if err := verifyMembershipAndLimits(group, filepath.Dir(group)); err != nil {
		t.Fatalf("valid bounded scope: %v", err)
	}
	for _, tc := range []struct{ name, value, parent string }{
		{"cgroup.procs", "", group},
		{"cgroup.procs", pid, group + "-sibling"},
		{"pids.max", "0", group},
		{"pids.max", "257", group},
		{"memory.max", "max", group},
		{"memory.max", "536870913", group},
	} {
		reset()
		fixtureFile(t, group, tc.name, tc.value)
		if err := verifyMembershipAndLimits(group, tc.parent); err == nil {
			t.Errorf("accepted %s=%q under %s", tc.name, tc.value, tc.parent)
		}
	}
	if err := os.Remove(filepath.Join(group, "memory.max")); err != nil {
		t.Fatalf("remove memory bound: %v", err)
	}
	if err := verifyMembershipAndLimits(group, group); err == nil {
		t.Fatal("accepted missing memory bound")
	}
	if err := verifyMembershipAndLimits(filepath.Join(group, "missing"), group); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent members file = %v", err)
	}
	t.Setenv("PW_SERVICE_RESOURCE_PARENT", filepath.Join(hierarchy, "absent-parent"))
	if err := Verify(); err == nil {
		t.Fatal("accepted live process outside the declared parent")
	}
}

func TestTrustedManagerUsesCapturedExecutable(t *testing.T) {
	if err := managerScope("/unused", "/bin/true", hierarchy); err != nil {
		t.Fatalf("start captured manager: %v", err)
	}
	var exit *exec.ExitError
	if err := managerScope("/unused", "/bin/false", hierarchy); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("manager exit status = %v", err)
	}
	for _, path := range []string{"/usr/bin", filepath.Join(t.TempDir(), "manager")} {
		if err := managerScope("/unused", path, hierarchy); err == nil {
			t.Errorf("accepted manager %s", path)
		}
	}
}

func TestGuestActivationUsesVerifiedBootFiles(t *testing.T) {
	bundle := t.TempDir()
	image := filepath.Join(bundle, "rootfs", "init")
	if err := runVM(image); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing manifest = %v", err)
	}
	fixtureFile(t, bundle, "manifest.json", fmt.Sprintf(`{"schema":1,"architecture":%q,"qemu_path":"/usr/bin/false"}`, runtime.GOARCH))
	if err := runVM(image); err == nil {
		t.Fatal("booted with invalid emulator")
	}
	driver := fixtureFile(t, bundle, "controller", "#!/bin/sh\n[ \"$LANG\" = C ] && [ \"$(cat /proc/self/fd/5)\" = kernel ] && [ \"$(cat /proc/self/fd/6)\" = initramfs ]\n")
	if err := os.Chmod(driver, 0700); err != nil {
		t.Fatalf("make fixture controller executable: %v", err)
	}
	binary, err := os.Open(driver)
	if err != nil {
		t.Fatalf("open fixture controller: %v", err)
	}
	defer binary.Close()
	m := bootManifest{Kernel: fmt.Sprintf("%x", sha256.Sum256([]byte("kernel"))), Initramfs: fmt.Sprintf("%x", sha256.Sum256([]byte("initramfs"))), QEMU: driver}
	if err := launchVM(bundle, m, binary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent kernel = %v", err)
	}
	fixtureFile(t, bundle, "kernel", "kernel")
	if err := launchVM(bundle, m, binary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent initramfs = %v", err)
	}
	fixtureFile(t, bundle, "initramfs.gz", "initramfs")
	done := make(chan error, 1)
	go func() {
		// Retire the thread after installing the irreversible no-new-privileges flag.
		runtime.LockOSThread()
		done <- launchVM(bundle, m, binary)
	}()
	if err := <-done; err != nil {
		t.Fatalf("launch with sealed boot files: %v", err)
	}
}

func TestRejectMalformedBootResources(t *testing.T) {
	dir := t.TempDir()
	manifest := fixtureFile(t, dir, "manifest.json", strings.Repeat(" ", (64<<10)+1))
	if _, err := readBootManifest(manifest); err == nil {
		t.Fatal("accepted oversized manifest")
	}
	if _, err := openBootResource(dir); err == nil {
		t.Fatal("accepted directory as boot file")
	}
	link := filepath.Join(dir, "linked-manifest")
	if err := os.Symlink(manifest, link); err != nil {
		t.Fatalf("link manifest: %v", err)
	}
	if _, err := readBootManifest(link); err == nil {
		t.Fatal("accepted linked boot manifest")
	}
	empty := fixtureFile(t, dir, "kernel", "")
	if _, err := sealedBootFile(empty, fmt.Sprintf("%x", sha256.Sum256(nil))); err == nil {
		t.Fatal("accepted empty kernel")
	}
}
