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
	"path/filepath"
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
		switch {
		case err == nil:
			return errors.New("foreign shared memory visible")
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("inspect foreign shared memory: %w", err)
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
	if mode != "temp-capacity" {
		if err := checkSharedMemoryMount(mode != "native-file" && mode != "native-mmap"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path); _ = f.Close() }()
	if mode == "capacity" || mode == "temp-capacity" {
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

func TestLinuxPrivateTempSurvivesOnlyItsSession(t *testing.T) {
	self := requireLinuxSandbox(t)
	host, err := os.CreateTemp("/tmp", "pw-shared-sentinel-")
	testutil.FailErr(t, "plant shared sentinel", err)
	t.Cleanup(func() { _ = os.Remove(host.Name()) })
	_, err = host.WriteString("host")
	testutil.FailErr(t, "write shared sentinel", err)
	testutil.FailErr(t, "close shared sentinel", host.Close())
	name := filepath.Base(host.Name())
	session := denyConfinement(t.TempDir())
	session.SessionScratchRoot = t.TempDir()
	run := func(c confine.Confinement, command string, args ...string) {
		t.Helper()
		status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", append([]string{"-ec", command, "sh"}, args...)...)
		if status.ExitStatus() != 0 {
			t.Fatalf("temp command exited %d: %s", status.ExitStatus(), out)
		}
	}
	run(session, `test ! -e "/tmp/$1"; printf session > "/tmp/$1"; printf private > "/var/tmp/$1.alt"`, name)
	run(session, `test "$(cat "/var/tmp/$1")" = session; test "$(cat "/tmp/$1.alt")" = private`, name)
	for _, probe := range []struct{ path, want string }{
		{host.Name(), "host"},
		{filepath.Join(session.SessionScratchRoot, name), "session"},
		{filepath.Join(session.SessionScratchRoot, name+".alt"), "private"},
	} {
		got, err := os.ReadFile(probe.path)
		testutil.FailErr(t, "read temp observation", err)
		if string(got) != probe.want {
			t.Fatalf("read %q from %s, want %q", got, probe.path, probe.want)
		}
	}
	session.SessionScratchRoot = t.TempDir()
	run(session, `test ! -e "/tmp/$1"; test ! -e "/var/tmp/$1.alt"`, name)
	session.SessionScratchRoot = ""
	run(session, `printf local > "/tmp/$1"; test "$(cat "/var/tmp/$1")" = local`, name)
	run(session, `test ! -e "/tmp/$1"`, name)
}

func TestLinuxLaunchLocalTempIsBounded(t *testing.T) {
	self := requireLinuxSandbox(t)
	session := denyConfinement(t.TempDir())
	status, out := linuxConfinedRun(t, self, session, shmEnv("temp-capacity", "/tmp/fill"), self, shmTestFlag)
	if status.ExitStatus() != 0 {
		t.Fatalf("private temp capacity: exit %d: %s", status.ExitStatus(), out)
	}
}

func TestLinuxPrivateTempKeepsExplicitHostSubtrees(t *testing.T) {
	self := requireLinuxSandbox(t)
	host, err := os.MkdirTemp("/tmp", "pw-attached-temp-")
	testutil.FailErr(t, "create host temp attachment", err)
	t.Cleanup(func() { _ = os.RemoveAll(host) })
	attached := filepath.Join(host, "project")
	readOnly := filepath.Join(host, "reviewed")
	testutil.FailErr(t, "create attached directory", os.Mkdir(attached, 0o700))
	testutil.FailErr(t, "seed read-only path", os.WriteFile(readOnly, []byte("original"), 0o600))
	session := denyConfinement(attached)
	session.ReadRoots = []string{readOnly}
	session.SessionScratchRoot = t.TempDir()
	status, out := linuxConfinedRun(t, self, session, nil, "/bin/sh", "-ec",
		`printf updated > "$1/file"; test "$(cat "$2")" = original; if (printf change > "$2"); then exit 1; fi`, "sh", attached, readOnly)
	if status.ExitStatus() != 0 {
		t.Fatalf("retained paths: exit %d: %s", status.ExitStatus(), out)
	}
	got, err := os.ReadFile(filepath.Join(attached, "file"))
	testutil.FailErr(t, "read retained attachment", err)
	if string(got) != "updated" {
		t.Fatalf("attached file = %q", got)
	}
}

func TestLinuxObservedCustomTempRefusalNamesSharedLocation(t *testing.T) {
	self := requireLiveBoundary(t)
	project, custom := t.TempDir(), outsideTemporaryWriteRoots(t)
	t.Setenv("TMPDIR", custom)
	target := filepath.Join(custom, "output")
	refusals, out := refusalsOf(t, self, liveBoundaryRoots(project), "/bin/sh", "-c", `printf x > "$1"`, "sh", target)
	refused, ok := findRefusal(refusals, "file-write-create", target)
	if !ok || refused.Layer != confine.FloorSharedTemp || refused.Recovery != confine.RecoverWriteRoot || refused.Grant != custom {
		t.Fatalf("shared temp write: refusals=%+v, output=%q", refusals, out)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused file exists: %v", err)
	}
}
