package linuxruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestBootImageIsPinnedAndSealed(t *testing.T) {
	bytes := []byte("test kernel")
	path := filepath.Join(t.TempDir(), "kernel")
	if err := os.WriteFile(path, bytes, 0600); err != nil {
		t.Fatalf("write kernel fixture: %v", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(bytes))
	file, err := sealedBootFile(path, digest)
	if err != nil {
		t.Fatalf("seal kernel fixture: %v", err)
	}
	defer file.Close()
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatalf("replace kernel fixture: %v", err)
	}
	got, err := io.ReadAll(file)
	if err != nil || !slices.Equal(got, bytes) {
		t.Fatalf("pinned image = %q, %v", got, err)
	}
	if _, err := file.WriteString("change"); !errors.Is(err, unix.EPERM) {
		t.Fatalf("sealed image write = %v", err)
	}
	if _, err := sealedBootFile(path, digest); err == nil {
		t.Fatal("changed image passed digest check")
	}
	if _, err := sealedBootFile(path, "invalid"); err == nil {
		t.Fatal("invalid digest accepted")
	}
}

func TestEmulatorAuthorityAndDevices(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, kvm := range []bool{false, true} {
			args := vmArguments(arch, kvm)
			for _, pair := range [][2]string{{"-m", "1024M"}, {"-smp", "1"}, {"-nic", "none"}, {"-monitor", "none"}, {"-display", "none"}, {"-chardev", "socket,id=pwcontrol,fd=3"}} {
				i := slices.Index(args, pair[0])
				if i < 0 || i+1 >= len(args) || args[i+1] != pair[1] {
					t.Fatalf("%s KVM=%v lacks %v", arch, kvm, pair)
				}
			}
			for _, flag := range []string{"-drive", "-fsdev", "-netdev", "-virtfs", "-qmp"} {
				if slices.Contains(args, flag) {
					t.Fatalf("unexpected host resource %s", flag)
				}
			}
		}
	}
	for _, path := range []string{"qemu-system-x86_64", "/tmp/qemu-system-x86_64", "/usr/bin/env", "/nix/store/user/bin/qemu-system-x86_64"} {
		if f, err := openEmulator(t.TempDir(), path); err == nil {
			f.Close()
			t.Fatalf("accepted emulator %s", path)
		}
	}
}
func TestManifestRequiresSupportedArchitecture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	for _, content := range []string{"{}", "{}{}", fmt.Sprintf(`{"schema":1,"architecture":%q,"unknown":true}`, runtime.GOARCH)} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		if _, err := readBootManifest(path); err == nil {
			t.Fatalf("accepted %s", content)
		}
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"schema":1,"architecture":%q}`, runtime.GOARCH)), 0600); err != nil {
		t.Fatalf("write supported manifest: %v", err)
	}
	if _, err := readBootManifest(path); err != nil {
		t.Fatalf("read supported manifest: %v", err)
	}
}
func TestEmulatorExitIsSettled(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := superviseVM(exec.CommandContext(ctx, "/bin/true")); err != nil {
		t.Fatalf("settle emulator process: %v", err)
	}
	if err := superviseVM(exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing"))); err == nil {
		t.Fatal("missing emulator started")
	}
}

func TestPackageDirectoryModes(t *testing.T) {
	for _, check := range []struct {
		path string
		mode os.FileMode
		want bool
	}{
		{"/nix/store", os.ModeDir | os.ModeSticky | 0775, true},
		{"/nix/store", os.ModeDir | 0775, false},
		{"/nix/store", os.ModeDir | os.ModeSticky | 0777, false},
		{"/nix/store/object", os.ModeDir | os.ModeSticky | 0775, false},
		{"/nix/store", os.ModeSticky | 0775, false},
		{"/nix/store/object", os.ModeDir | 0555, true},
		{"/usr/bin", os.ModeDir | 0755, true},
	} {
		if got := packageModeTrusted(check.path, check.mode); got != check.want {
			t.Errorf("trust %s %s = %v", check.path, check.mode, got)
		}
	}
}
