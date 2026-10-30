//go:build linux && (amd64 || arm64)

package launchguard

import (
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGuardianWaitidChildFixture(t *testing.T) {
	mode := os.Getenv("PW_GUARDIAN_WAITID_FIXTURE")
	if mode == "" {
		return
	}
	gate := os.NewFile(3, "owned-waitid-gate")
	var ready [1]byte
	if n, err := gate.Read(ready[:]); err != nil || n != 1 {
		os.Exit(2)
	}
	if mode == "signal" {
		_ = unix.Kill(os.Getpid(), unix.SIGKILL)
		os.Exit(2)
	}
	os.Exit(3)
}

func TestGuardianWaitidObservationNeverConsumesOriginalWaitStatus(t *testing.T) {
	for _, mode := range []string{"exit", "signal"} {
		t.Run(mode, func(t *testing.T) {
			read, write, err := os.Pipe()
			testutil.FailErr(t, "create owned child exit gate", err)
			defer read.Close()
			defer write.Close()
			command := exec.Command(os.Args[0], "-test.run=^TestGuardianWaitidChildFixture$")
			command.Env = append(os.Environ(), "PW_GUARDIAN_WAITID_FIXTURE="+mode)
			command.ExtraFiles = []*os.File{read}
			testutil.FailErr(t, "start only owned waitid child", command.Start())
			reaped := false
			defer func() {
				if !reaped {
					_ = command.Process.Kill()
					_, _ = command.Process.Wait()
				} else {
					_ = command.Process.Release()
				}
			}()
			pin, err := unix.PidfdOpen(command.Process.Pid, 0)
			testutil.FailErr(t, "retain original child pidfd", err)
			defer unix.Close(pin)
			uid, pid := uint32(os.Getuid()), command.Process.Pid
			if observeGuardianExit(pin, pid, uid) != nil {
				t.Fatal("live child gained fabricated exit status")
			}
			_, err = write.Write([]byte{1})
			testutil.FailErr(t, "release only owned child to actual exit", err)
			polls := []unix.PollFd{{Fd: int32(pin), Events: unix.POLLIN}}
			_, err = unix.Poll(polls, 5000)
			testutil.FailErr(t, "await original child pidfd death", err)
			if polls[0].Revents&unix.POLLIN == 0 {
				t.Fatal("owned waitid child did not exit")
			}
			expected := &guardianExitFact{Code: 1, Status: 3, WaitStatus: 3 << 8}
			if mode == "signal" {
				expected = &guardianExitFact{Code: 2, Status: 9, WaitStatus: 9}
			}
			first, second := observeGuardianExit(pin, pid, uid), observeGuardianExit(pin, pid, uid)
			if !reflect.DeepEqual(first, expected) || !reflect.DeepEqual(second, expected) {
				t.Fatalf("nonconsuming child facts differ: first=%+v second=%+v want=%+v", first, second, expected)
			}
			if observeGuardianExit(pin, pid+1, uid) != nil || observeGuardianExit(pin, pid, uid^1) != nil {
				t.Fatal("observed exit accepted a foreign role PID or UID")
			}
			var status unix.WaitStatus
			actual, err := unix.Wait4(pid, &status, 0, nil)
			testutil.FailErr(t, "original Wait4 remains sole reap/status authority", err)
			reaped = true
			if actual != pid || uint32(status) != expected.WaitStatus {
				t.Fatalf("diagnostic consumed or changed original wait: pid=%d status=%d", actual, status)
			}
			if observeGuardianExit(pin, pid, uid) != nil {
				t.Fatal("reaped child retained fabricated status")
			}
		})
	}
}

func TestGuardianWaitidRejectsUnsupportedChildStatusEncoding(t *testing.T) {
	for _, value := range [][2]int32{{0, 0}, {4, 9}, {5, 19}, {6, 0}, {1, -1}, {1, 256}, {2, 0}, {2, 65}, {3, -1}} {
		if _, ok := guardianExitStatus(value[0], value[1]); ok {
			t.Fatalf("fabricated raw status for unsupported child code/status %v", value)
		}
	}
	if raw, ok := guardianExitStatus(3, int32(unix.SIGSEGV)); !ok || raw != uint32(unix.SIGSEGV)|0x80 {
		t.Fatal("Linux core-dump status encoding lost its exact bit")
	}
	if observeGuardianExit(-1, os.Getpid(), uint32(os.Getuid())) != nil {
		t.Fatal("invalid pidfd fabricated status")
	}
}

func TestGuardianWaitidRetainsActualIntakeFailurePhase(t *testing.T) {
	for _, code := range []int{80, 81, 82, 83, 84, 85, 86, 100, 101, 102, 103, 104, 105, 110, 111, 112, 113, 114, 115, 120, 121, 122, 123, 124, 125} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestIntakeMonitorStatusFixture$")
			command.Env = append(os.Environ(), "PW_INTAKE_MONITOR_STATUS_FIXTURE="+strconv.Itoa(code))
			testutil.FailErr(t, "start owned intake phase child", command.Start())
			waited := false
			defer func() {
				if !waited {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			pin, err := unix.PidfdOpen(command.Process.Pid, 0)
			testutil.FailErr(t, "pin exact intake phase child", err)
			defer unix.Close(pin)
			polls := []unix.PollFd{{Fd: int32(pin), Events: unix.POLLIN}}
			_, err = unix.Poll(polls, 5000)
			testutil.FailErr(t, "observe owned intake child exit", err)
			if polls[0].Revents&unix.POLLIN == 0 {
				t.Fatal("owned intake child did not exit")
			}
			expected := &guardianExitFact{Code: 1, Status: int32(code), WaitStatus: uint32(code << 8)}
			for range 2 {
				if actual := observeGuardianExit(pin, command.Process.Pid, uint32(os.Getuid())); !reflect.DeepEqual(actual, expected) {
					t.Fatalf("intake failure phase lost in actual nonconsuming waitid: got=%+v want=%+v", actual, expected)
				}
			}
			err = command.Wait()
			waited = true
			if err == nil || command.ProcessState.ExitCode() != code {
				t.Fatalf("diagnostic changed original child exit: state=%v error=%v", command.ProcessState, err)
			}
		})
	}
}
