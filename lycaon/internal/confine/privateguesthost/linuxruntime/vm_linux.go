package linuxruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const ConsoleName = "org.paintedwolf.private-service"
const ConsoleArgument = "pw_service_transport=virtio-port"

type bootManifest struct {
	Schema       int    `json:"schema"`
	Architecture string `json:"architecture"`
	Kernel       string `json:"kernel_sha256"`
	Initramfs    string `json:"initramfs_sha256"`
	QEMU         string `json:"qemu_path"`
}

func runVM(image string) error {
	bundle := filepath.Dir(filepath.Dir(image))
	manifest, err := readBootManifest(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return err
	}
	binary, err := openEmulator(bundle, manifest.QEMU)
	if err != nil {
		return err
	}
	defer func() { _ = binary.Close() }()
	kernel, err := sealedBootFile(filepath.Join(bundle, "kernel"), manifest.Kernel)
	if err != nil {
		return err
	}
	defer func() { _ = kernel.Close() }()
	initrd, err := sealedBootFile(filepath.Join(bundle, "initramfs.gz"), manifest.Initramfs)
	if err != nil {
		return err
	}
	defer func() { _ = initrd.Close() }()
	args := vmArguments(runtime.GOARCH, hasKVM())
	cmd := ControllerCommand(context.Background(), "/proc/self/fd/4", args...)
	cmd.Args[0] = manifest.QEMU
	cmd.Env = []string{"LANG=C"}
	cmd.ExtraFiles = []*os.File{os.Stdin, binary, kernel, initrd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	return superviseVM(cmd)
}

func vmArguments(arch string, kvm bool) []string {
	machine := "q35,usb=off"
	if arch == "arm64" {
		machine = "virt"
	}
	accelerator, cpu := "tcg,thread=single", "max"
	if kvm {
		accelerator, cpu = "kvm", "host"
	}
	return []string{"-no-user-config", "-nodefaults", "-no-reboot", "-display", "none", "-monitor", "none", "-serial", "none", "-parallel", "none", "-nic", "none",
		"-machine", machine, "-accel", accelerator, "-cpu", cpu, "-smp", "1", "-m", "1024M",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-kernel", "/proc/self/fd/5", "-initrd", "/proc/self/fd/6", "-append", "rdinit=/init panic=1 quiet " + ConsoleArgument,
		"-chardev", "socket,id=pwcontrol,fd=3", "-device", "virtio-serial-pci", "-device", "virtserialport,chardev=pwcontrol,name=" + ConsoleName}
}

func hasKVM() bool {
	fd, err := unix.Open("/dev/kvm", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	version, err := unix.IoctlRetInt(fd, 0xae00)
	return err == nil && version == 12
}

func openBootResource(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.Join(errors.New("invalid private boot resource"), err)
	}
	return file, nil
}

func readBootManifest(path string) (bootManifest, error) {
	var manifest bootManifest
	open := openBootResource
	if strings.HasPrefix(path, "/nix/store/") {
		open = func(path string) (*os.File, error) { return trustedFile(path, false) }
	}
	file, err := open(path)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return manifest, err
	}
	if len(raw) > 64<<10 {
		return manifest, errors.New("oversized private service manifest")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&manifest); err != nil {
		return manifest, err
	}
	if d.Decode(new(any)) != io.EOF || manifest.Schema != 1 || manifest.Architecture != runtime.GOARCH {
		return manifest, errors.New("invalid private service manifest")
	}
	return manifest, nil
}

func openEmulator(bundle, path string) (*os.File, error) {
	name := "qemu-system-x86_64"
	if runtime.GOARCH == "arm64" {
		name = "qemu-system-aarch64"
	}
	if filepath.Base(path) != name || (!filepath.IsAbs(path)) || filepath.Clean(path) != path {
		return nil, errors.New("invalid private emulator selection")
	}
	if path != filepath.Join("/usr/bin", name) {
		if !strings.HasPrefix(bundle, "/nix/store/") || !strings.HasPrefix(path, "/nix/store/") || !strings.HasSuffix(path, "/bin/"+name) {
			return nil, errors.New("private emulator is outside its package")
		}
	}
	return trustedFile(path, true)
}

// Distribution executables may be symlinks. Every traversed directory stays administrator-owned.
func trustedFile(path string, executable bool) (*os.File, error) {
	remaining := path
	for i := 0; i < 40; i++ {
		prefix := "/"
		parts := strings.Split(strings.TrimPrefix(remaining, "/"), "/")
		changed := false
		for n, part := range parts {
			prefix = filepath.Join(prefix, part)
			info, err := os.Lstat(prefix)
			if err != nil {
				return nil, err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 {
				return nil, errors.New("untrusted private emulator package")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(prefix)
				if err != nil {
					return nil, err
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(prefix), target)
				}
				remaining = filepath.Join(append([]string{target}, parts[n+1:]...)...)
				changed = true
				break
			}
			if !packageModeTrusted(prefix, info.Mode()) {
				return nil, errors.New("writable private emulator package")
			}
			if n == len(parts)-1 {
				if !info.Mode().IsRegular() || (executable && info.Mode().Perm()&0111 == 0) {
					return nil, errors.New("invalid private emulator file")
				}
				fd, err := unix.Openat2(unix.AT_FDCWD, prefix, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS})
				if err != nil {
					return nil, err
				}
				file := os.NewFile(uintptr(fd), prefix)
				opened, err := file.Stat()
				if err != nil || !os.SameFile(info, opened) || opened.Mode() != info.Mode() {
					_ = file.Close()
					return nil, errors.Join(errors.New("private emulator package changed during capture"), err)
				}
				return file, nil
			}
		}
		if !changed {
			break
		}
	}
	return nil, errors.New("invalid private emulator links")
}

func packageModeTrusted(path string, mode os.FileMode) bool {
	if mode.Perm()&0022 == 0 {
		return true
	}
	// The sticky store protects root-owned entries while build users create outputs.
	return path == "/nix/store" && mode.IsDir() && mode&os.ModeSticky != 0 && mode.Perm()&0002 == 0
}

func sealedBootFile(path, digest string) (*os.File, error) {
	want, err := hex.DecodeString(digest)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("invalid private boot digest")
	}
	source, err := openBootResource(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	const limit = 512 << 20
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, errors.New("invalid private boot file")
	}
	fd, err := unix.MemfdCreate("private-boot", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, limit+1))
	if err != nil {
		return nil, err
	}
	if n != info.Size() || !slices.Equal(hash.Sum(nil), want) {
		return nil, fmt.Errorf("private service %s digest mismatch", filepath.Base(path))
	}
	if _, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok = true
	return file, nil
}

// A lost host also retires a VM that never reached its guest supervisor.
func superviseVM(cmd *exec.Cmd) error {
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = read.Close() }()
	if err := cmd.Start(); err != nil {
		_ = write.Close()
		return err
	}
	done := make(chan struct{})
	var status error
	go func() { status = cmd.Wait(); _ = write.Close(); close(done) }()
	poll := []unix.PollFd{{Fd: 0, Events: unix.POLLRDHUP}, {Fd: int32(read.Fd()), Events: unix.POLLIN}}
	for {
		if _, err := unix.Poll(poll, -1); errors.Is(err, unix.EINTR) {
			continue
		}
		break
	}
	select {
	case <-done:
		return status
	case <-time.After(5 * time.Second):
	}
	_ = cmd.Process.Kill()
	<-done
	return status
}
