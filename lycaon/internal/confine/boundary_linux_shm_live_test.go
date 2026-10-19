//go:build linux

package confine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

const shmProbeEnv = "PW_CONFINE_SHM_PROBE"
const shmPathEnv = "PW_CONFINE_SHM_PATH"
const shmTestFlag = "-test.run=^TestLinuxSharedMemoryProbeProcess$"
const sharedMemoryCapacity = 64 * 1024 * 1024

func TestLinuxSharedMemoryProbeProcess(t *testing.T) {
	mode := os.Getenv(shmProbeEnv)
	if mode == "" {
		return
	}
	if err := runSharedMemoryProbe(mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runSharedMemoryProbe(mode string) error {
	path := os.Getenv(shmPathEnv)
	if mode == "absent" {
		_, err := os.Stat(path)
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("foreign shared memory visible: %v", err)
		}
		return nil
	}
	if mode == "read" {
		data, err := os.ReadFile(path)
		if err == nil && !bytes.Equal(data, bytes.Repeat([]byte{73}, 4*1024*1024)) {
			return errors.New("shared memory content changed")
		}
		return err
	}
	if err := checkSharedMemoryMount(mode != "native-file" && mode != "native-mmap"); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path); _ = f.Close() }()
	if mode == "capacity" {
		return checkSharedMemoryLimit(f)
	}
	if mode == "mmap" || mode == "native-mmap" {
		if err := f.Truncate(4 * 1024 * 1024); err != nil {
			return err
		}
		data, err := unix.Mmap(int(f.Fd()), 0, 4*1024*1024, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return err
		}
		for i := range data {
			data[i] = 73
		}
		if err := unix.Munmap(data); err != nil {
			return err
		}
	} else if _, err := f.Write(bytes.Repeat([]byte{73}, 4*1024*1024)); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(self, shmTestFlag)
	child.Env = append(os.Environ(), shmProbeEnv+"=read")
	if output, err := child.CombinedOutput(); err != nil {
		return fmt.Errorf("descendant read: %w: %s", err, output)
	}
	if mode == "hold" {
		fmt.Println("ready")
		_, err = io.Copy(io.Discard, os.Stdin)
	}
	return err
}

func checkSharedMemoryMount(private bool) error {
	var fs unix.Statfs_t
	if err := unix.Statfs("/dev/shm", &fs); err != nil {
		return err
	}
	if private && fs.Blocks*uint64(fs.Bsize) != sharedMemoryCapacity {
		return fmt.Errorf("shared memory capacity = %d", fs.Blocks*uint64(fs.Bsize))
	}
	st, err := os.Stat("/dev/shm")
	if err != nil {
		return err
	}
	if st.Mode().Perm() != 0o777 || st.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("shared memory mode = %s", st.Mode())
	}
	return nil
}

func checkSharedMemoryLimit(f *os.File) error {
	chunk := make([]byte, 1024*1024)
	written := 0
	for written <= sharedMemoryCapacity {
		n, err := f.Write(chunk)
		written += n
		if errors.Is(err, unix.ENOSPC) {
			break
		}
		if err != nil {
			return err
		}
	}
	if written != sharedMemoryCapacity {
		return fmt.Errorf("write capacity = %d, want %d", written, sharedMemoryCapacity)
	}
	if err := f.Truncate(sharedMemoryCapacity + int64(os.Getpagesize())); err != nil {
		return err
	}
	data, err := unix.Mmap(int(f.Fd()), 0, sharedMemoryCapacity+os.Getpagesize(), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Munmap(data) }()
	debug.SetPanicOnFault(true)
	faulted := false
	func() {
		defer func() { faulted = recover() != nil }()
		// Go converts this resource-exhaustion SIGBUS to a recoverable fault.
		data[sharedMemoryCapacity] = 1
	}()
	if !faulted {
		return errors.New("mmap touched a page past the full tmpfs capacity")
	}
	return nil
}

func shmEnv(mode, path string) []string {
	return append(os.Environ(), shmProbeEnv+"="+mode, shmPathEnv+"="+path)
}

func TestLinuxPrivateSharedMemoryCapacity(t *testing.T) {
	self := requireLinuxSandbox(t)
	c := denyConfinement(t.TempDir())
	for _, mode := range []string{"native-file", "native-mmap"} {
		native := exec.Command(self, shmTestFlag)
		native.Env = shmEnv(mode, fmt.Sprintf("/dev/shm/pwc-shm-native-%d", os.Getpid()))
		if output, err := native.CombinedOutput(); err != nil {
			t.Fatalf("native shared memory %s: %v: %s", mode, err, output)
		}
	}
	for _, mode := range []string{"file", "mmap", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			status, out := linuxConfinedRun(t, self, c, shmEnv(mode, "/dev/shm/pwc-shm-fixture"), self, shmTestFlag)
			if !status.Exited() || status.ExitStatus() != 0 {
				t.Fatalf("private shared memory %s: %v: %s", mode, status, out)
			}
		})
	}
}

func TestLinuxSharedMemoryIsPrivateBetweenLaunches(t *testing.T) {
	self := requireLinuxSandbox(t)
	c := denyConfinement(t.TempDir())
	path := fmt.Sprintf("/dev/shm/pwc-shm-%d", os.Getpid())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{shmTestFlag}, c)
	testutil.FailErr(t, "build shared memory owner", err)
	defer cleanup()
	owner.Env = shmEnv("hold", path)
	input, err := owner.StdinPipe()
	testutil.FailErr(t, "owner stdin", err)
	defer func() { _ = input.Close() }()
	stdout, err := owner.StdoutPipe()
	testutil.FailErr(t, "owner stdout", err)
	owner.Stderr = os.Stderr
	testutil.FailErr(t, "start shared memory owner", owner.Start())
	defer func() { cancel(); _ = owner.Wait() }()
	ready := make([]byte, len("ready\n"))
	_, err = io.ReadFull(stdout, ready)
	testutil.FailErr(t, "owner readiness", err)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private mount leaked to host: %v", err)
	}
	status, out := linuxConfinedRun(t, self, c, shmEnv("absent", path), self, shmTestFlag)
	if !status.Exited() || status.ExitStatus() != 0 {
		t.Fatalf("sibling shared memory isolation: %v: %s", status, out)
	}
	testutil.FailErr(t, "finish shared memory owner", input.Close())
	testutil.FailErr(t, "wait for shared memory owner", owner.Wait())
}
