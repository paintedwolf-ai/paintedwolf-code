//go:build linux

package confine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

const ipcProbeEnv = "PW_CONFINE_IPC_PROBE"
const ipcIDsEnv = "PW_CONFINE_IPC_IDS"
const ipcTestFlag = "-test.run=^TestLinuxIPCProbeProcess$"
const ipcMarker = 73

// Linux semctl commands from include/uapi/linux/sem.h, absent from x/sys/unix.
const semGetValue = 12
const semSetValue = 16

type ipcObjects struct {
	SharedMemory int
	Semaphore    int
	MessageQueue int
}

type ipcMessage struct {
	Type  int64
	Value int64
}

func ipcCall(call, id, arg, flags uintptr, data uintptr) (uintptr, error) {
	r, _, errno := unix.Syscall6(call, id, arg, flags, data, 0, 0)
	if errno != 0 {
		return r, errno
	}
	return r, nil
}

func newIPCObjects() (*ipcObjects, error) {
	o := &ipcObjects{SharedMemory: -1, Semaphore: -1, MessageQueue: -1}
	var err error
	o.SharedMemory, err = unix.SysvShmGet(unix.IPC_PRIVATE, 4096, unix.IPC_CREAT|0o600)
	if err == nil {
		var id uintptr
		id, err = ipcCall(unix.SYS_SEMGET, unix.IPC_PRIVATE, 1, unix.IPC_CREAT|0o600, 0)
		o.Semaphore = int(id)
	}
	if err == nil {
		var id uintptr
		id, err = ipcCall(unix.SYS_MSGGET, unix.IPC_PRIVATE, unix.IPC_CREAT|0o600, 0, 0)
		o.MessageQueue = int(id)
	}
	if err == nil {
		var memory []byte
		memory, err = unix.SysvShmAttach(o.SharedMemory, 0, 0)
		if err == nil {
			memory[0] = ipcMarker
			err = unix.SysvShmDetach(memory)
		}
	}
	if err == nil {
		_, err = ipcCall(unix.SYS_SEMCTL, uintptr(o.Semaphore), 0, semSetValue, ipcMarker)
	}
	if err == nil {
		err = o.sendMessage()
	}
	if err != nil {
		_ = o.remove()
		return nil, err
	}
	return o, nil
}

func (o *ipcObjects) sendMessage() error {
	m := ipcMessage{Type: 1, Value: ipcMarker}
	_, _, errno := unix.Syscall6(unix.SYS_MSGSND, uintptr(o.MessageQueue), uintptr(unsafe.Pointer(&m)), 8, unix.IPC_NOWAIT, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func (o *ipcObjects) remove() error {
	var result error
	if o.SharedMemory >= 0 {
		_, err := unix.SysvShmCtl(o.SharedMemory, unix.IPC_RMID, nil)
		result = errors.Join(result, err)
	}
	if o.Semaphore >= 0 {
		_, err := ipcCall(unix.SYS_SEMCTL, uintptr(o.Semaphore), 0, unix.IPC_RMID, 0)
		result = errors.Join(result, err)
	}
	if o.MessageQueue >= 0 {
		_, err := ipcCall(unix.SYS_MSGCTL, uintptr(o.MessageQueue), unix.IPC_RMID, 0, 0)
		result = errors.Join(result, err)
	}
	return result
}

func (o *ipcObjects) check(private bool) error {
	memory, shmErr := unix.SysvShmAttach(o.SharedMemory, 0, unix.SHM_RDONLY)
	if shmErr == nil {
		defer func() { _ = unix.SysvShmDetach(memory) }()
		if private && memory[0] != ipcMarker {
			return fmt.Errorf("shared memory marker = %d", memory[0])
		}
	}
	value, semErr := ipcCall(unix.SYS_SEMCTL, uintptr(o.Semaphore), 0, semGetValue, 0)
	if private && semErr == nil && value != ipcMarker {
		return fmt.Errorf("semaphore value = %d", value)
	}
	m := ipcMessage{}
	_, _, msgErr := unix.Syscall6(unix.SYS_MSGRCV, uintptr(o.MessageQueue), uintptr(unsafe.Pointer(&m)), 8, 0, unix.IPC_NOWAIT, 0)
	if private {
		if msgErr != 0 {
			return fmt.Errorf("receive message: %w", msgErr)
		}
		if m.Value != ipcMarker {
			return fmt.Errorf("message value = %d", m.Value)
		}
		return errors.Join(shmErr, semErr, o.sendMessage())
	}
	if shmErr == nil || semErr == nil || msgErr == 0 {
		return fmt.Errorf("foreign IPC reachable: shared memory=%v semaphore=%v message queue=%v", shmErr, semErr, msgErr)
	}
	return nil
}

func ipcEnv(mode string, objects *ipcObjects) []string {
	raw, _ := json.Marshal(objects)
	return append(os.Environ(), ipcProbeEnv+"="+mode, ipcIDsEnv+"="+string(raw))
}

func TestLinuxIPCProbeProcess(t *testing.T) {
	mode := os.Getenv(ipcProbeEnv)
	if mode == "" {
		return
	}
	if err := runIPCProbe(mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runIPCProbe(mode string) error {
	if mode != "owner" {
		var o ipcObjects
		if err := json.Unmarshal([]byte(os.Getenv(ipcIDsEnv)), &o); err != nil {
			return err
		}
		return o.check(mode == "check")
	}
	o, err := newIPCObjects()
	if err != nil {
		return err
	}
	defer func() { _ = o.remove() }()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(self, ipcTestFlag)
	child.Env = ipcEnv("check", o)
	if output, err := child.CombinedOutput(); err != nil {
		return fmt.Errorf("same-action descendant: %w: %s", err, output)
	}
	if err := json.NewEncoder(os.Stdout).Encode(o); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, os.Stdin)
	return err
}

func TestLinuxIPCIsolatesHostAndSiblingActions(t *testing.T) {
	self := requireLinuxSandbox(t)
	host, err := newIPCObjects()
	testutil.FailErr(t, "create host IPC fixtures", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove host IPC fixtures", host.remove()) })
	native := exec.Command(self, ipcTestFlag)
	native.Env = ipcEnv("check", host)
	output, err := native.CombinedOutput()
	if err != nil {
		t.Fatalf("native IPC fixture failed: %v: %s", err, output)
	}
	c := denyConfinement(t.TempDir())
	status, out := linuxConfinedRun(t, self, c, ipcEnv("deny", host), self, ipcTestFlag)
	if !status.Exited() || status.ExitStatus() != 0 {
		t.Fatalf("host IPC isolation: %v: %s", status, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{ipcTestFlag}, c)
	testutil.FailErr(t, "build sibling owner command", err)
	defer cleanup()
	owner.Env = ipcEnv("owner", nil)
	input, err := owner.StdinPipe()
	testutil.FailErr(t, "owner stdin", err)
	defer func() { _ = input.Close() }()
	stdout, err := owner.StdoutPipe()
	testutil.FailErr(t, "owner stdout", err)
	owner.Stderr = os.Stderr
	testutil.FailErr(t, "start sibling owner", owner.Start())
	defer func() { cancel(); _ = owner.Wait() }()
	var sibling ipcObjects
	testutil.FailErr(t, "read sibling IPC identifiers", json.NewDecoder(stdout).Decode(&sibling))
	status, out = linuxConfinedRun(t, self, c, ipcEnv("deny", &sibling), self, ipcTestFlag)
	if !status.Exited() || status.ExitStatus() != 0 {
		t.Fatalf("sibling IPC isolation: %v: %s", status, out)
	}
	testutil.FailErr(t, "finish sibling owner", input.Close())
	testutil.FailErr(t, "wait for sibling owner", owner.Wait())
	testutil.FailErr(t, "host IPC remains intact", host.check(true))
}
