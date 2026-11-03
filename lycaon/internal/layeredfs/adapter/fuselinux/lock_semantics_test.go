//go:build linux

package fuselinux_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
)

func TestLockHelperSubprocess(t *testing.T) {
	if os.Getenv("LYCAON_FUSELINUX_LOCK_HELPER") != "1" {
		return
	}
	fds := make(map[string]*os.File)
	defer func() {
		for _, f := range fds {
			_ = f.Close()
		}
	}()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "QUIT" {
			fmt.Println("BYE")
			return
		}
		fmt.Println(handleLockHelperCommand(fds, fields))
	}
}

func handleLockHelperCommand(fds map[string]*os.File, fields []string) string {
	switch fields[0] {
	case "OPEN":
		f, err := os.OpenFile(fields[2], os.O_RDWR, 0o644)
		if err != nil {
			return "ERR " + err.Error()
		}
		fds[fields[1]] = f
		return fmt.Sprintf("OK %d", os.Getpid())
	case "CLOSE":
		f := fds[fields[1]]
		delete(fds, fields[1])
		if err := f.Close(); err != nil {
			return "ERR " + err.Error()
		}
		return "OK"
	case "SETLK", "GETLK":
		return handlePOSIXLockCmd(fds[fields[1]], fields)
	case "FLOCK":
		return handleFlockCmd(fds[fields[1]], fields[2])
	default:
		return "ERR unknown command"
	}
}

func handlePOSIXLockCmd(f *os.File, fields []string) string {
	start, _ := strconv.ParseInt(fields[3], 10, 64)
	length, _ := strconv.ParseInt(fields[4], 10, 64)
	var typ int16 = syscall.F_UNLCK
	switch fields[2] {
	case "WRLCK":
		typ = syscall.F_WRLCK
	case "RDLCK":
		typ = syscall.F_RDLCK
	}
	fl := syscall.Flock_t{
		Type:   typ,
		Whence: int16(io.SeekStart),
		Start:  start,
		Len:    length,
	}
	if fields[0] == "SETLK" {
		err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &fl)
		if err == nil {
			return "OK"
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES) {
			return "CONFLICT"
		}
		return "ERR " + err.Error()
	}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &fl); err != nil {
		return "ERR " + err.Error()
	}
	if fl.Type == syscall.F_UNLCK {
		return "UNLCK"
	}
	tName := "RDLCK"
	if fl.Type == syscall.F_WRLCK {
		tName = "WRLCK"
	}
	return fmt.Sprintf("HELD %s %d %d %d", tName, fl.Start, fl.Len, fl.Pid)
}

func handleFlockCmd(f *os.File, mode string) string {
	var how int
	switch mode {
	case "EX":
		how = syscall.LOCK_EX | syscall.LOCK_NB
	case "SH":
		how = syscall.LOCK_SH | syscall.LOCK_NB
	case "UN":
		how = syscall.LOCK_UN
	}
	err := syscall.Flock(int(f.Fd()), how)
	if err == nil {
		return "OK"
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return "CONFLICT"
	}
	return "ERR " + err.Error()
}

type lockWorkerProc struct {
	t      *testing.T
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
}

func startLockWorkerProc(t *testing.T, name string) *lockWorkerProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestLockHelperSubprocess$")
	cmd.Env = append(os.Environ(), "LYCAON_FUSELINUX_LOCK_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	w := &lockWorkerProc{t: t, name: name, cmd: cmd, stdin: stdin, stdout: bufio.NewScanner(stdoutPipe)}
	t.Cleanup(func() {
		_, _ = fmt.Fprintln(stdin, "QUIT")
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	return w
}

func (w *lockWorkerProc) exec(command string, wantPrefix string) string {
	w.t.Helper()
	if _, err := fmt.Fprintln(w.stdin, command); err != nil {
		w.t.Fatalf("[%s] send %q: %v", w.name, command, err)
	}
	if !w.stdout.Scan() {
		w.t.Fatalf("[%s] EOF waiting for reply to %q (err=%v)", w.name, command, w.stdout.Err())
	}
	got := strings.TrimSpace(w.stdout.Text())
	if !strings.HasPrefix(got, wantPrefix) {
		w.t.Fatalf("[%s] %q -> got %q, want prefix %q", w.name, command, got, wantPrefix)
	}
	return got
}

func TestMountedPOSIXByteRangeLocksAndFlockSemanticsTwoProcesses(t *testing.T) {
	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime")
	for _, d := range []string{trunkDir, backingDir, runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "locks.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("seed locks.bin: %v", err)
	}

	const viewID = "view-posix-and-flock-locks"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	procA := startLockWorkerProc(t, "procA")
	procB := startLockWorkerProc(t, "procB")
	lockFile := filepath.Join(prep.PrimaryWorkPath, "locks.bin")

	openA := procA.exec("OPEN fd1 "+lockFile, "OK ")
	pidA := strings.TrimPrefix(openA, "OK ")
	openB := procB.exec("OPEN fd1 "+lockFile, "OK ")
	pidB := strings.TrimPrefix(openB, "OK ")

	// 1. Disjoint byte-range write locks across two processes succeed without conflict.
	procA.exec("SETLK fd1 WRLCK 0 100", "OK")
	procB.exec("SETLK fd1 WRLCK 100 100", "OK")
	procA.exec("GETLK fd1 WRLCK 100 100", "HELD WRLCK 100 100 "+pidB)
	procB.exec("GETLK fd1 WRLCK 0 100", "HELD WRLCK 0 100 "+pidA)
	procB.exec("GETLK fd1 WRLCK 200 100", "UNLCK")

	// 2. Acquiring another disjoint range by procA preserves procA's previous [0, 99] lock.
	procA.exec("SETLK fd1 WRLCK 200 100", "OK")
	procB.exec("SETLK fd1 WRLCK 0 50", "CONFLICT")
	procB.exec("GETLK fd1 WRLCK 0 50", "HELD WRLCK 0 100 "+pidA)
	procB.exec("SETLK fd1 WRLCK 200 50", "CONFLICT")
	procB.exec("GETLK fd1 WRLCK 200 50", "HELD WRLCK 200 100 "+pidA)

	// 3. Overlapping range fails; coalescing adjacent [0,99]+[100,199]+[200,299] merges into [0, 299] (len 300).
	procB.exec("SETLK fd1 WRLCK 50 100", "CONFLICT")
	procB.exec("SETLK fd1 UNLCK 100 100", "OK")
	procA.exec("SETLK fd1 WRLCK 100 100", "OK")
	procB.exec("GETLK fd1 WRLCK 50 200", "HELD WRLCK 0 300 "+pidA)

	// 4. Partial unlock of [100, 199] splits [0, 299] into [0, 99] and [200, 299].
	procA.exec("SETLK fd1 UNLCK 100 100", "OK")
	procB.exec("GETLK fd1 WRLCK 100 100", "UNLCK")
	procB.exec("SETLK fd1 WRLCK 100 100", "OK")
	procB.exec("SETLK fd1 WRLCK 0 100", "CONFLICT")
	procB.exec("GETLK fd1 WRLCK 0 100", "HELD WRLCK 0 100 "+pidA)
	procB.exec("SETLK fd1 WRLCK 200 100", "CONFLICT")
	procB.exec("GETLK fd1 WRLCK 200 100", "HELD WRLCK 200 100 "+pidA)
	procB.exec("SETLK fd1 UNLCK 100 100", "OK")

	// 5. Independent flock vs POSIX fcntl domains and independent close(fd2) lifetimes.
	procB.exec("FLOCK fd1 EX", "OK")
	procA.exec("FLOCK fd1 EX", "CONFLICT")
	procB.exec("FLOCK fd1 UN", "OK")
	procA.exec("FLOCK fd1 EX", "OK")

	// Opening and closing a second descriptor fd2 in procA releases procA's POSIX locks (on FUSE_FLUSH)
	// while preserving procA's open-file-description flock on fd1!
	procA.exec("OPEN fd2 "+lockFile, "OK ")
	procA.exec("CLOSE fd2", "OK")
	procB.exec("SETLK fd1 WRLCK 0 300", "OK")
	procB.exec("SETLK fd1 UNLCK 0 300", "OK")
	procB.exec("FLOCK fd1 EX", "CONFLICT")

	// Closing fd1 in procA releases the flock on FUSE_RELEASE.
	procA.exec("CLOSE fd1", "OK")
	procB.exec("FLOCK fd1 EX", "OK")
	procB.exec("FLOCK fd1 UN", "OK")
	procB.exec("CLOSE fd1", "OK")
}
