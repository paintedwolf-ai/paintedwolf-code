//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGuardianSetupNoticeDropsAuthorityAndBoundsBlockedOutput(t *testing.T) {
	fixture := installLaunchFixture(t)
	testutil.FailErr(t, "exercise fixed caller-only setup diagnostic", sudoFixture(t, "setup-notice", fixture.dir, fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid())))
}

func checkBootstrapNotice(directory, uidText, gidText string) error {
	uid, e1 := strconv.ParseUint(uidText, 10, 32)
	gid, e2 := strconv.ParseUint(gidText, 10, 32)
	if e1 != nil || e2 != nil || uid == 0 {
		return errors.New("invalid notice fixture caller")
	}
	policyPath, lockDirectory = filepath.Join(directory, "launch.json"), filepath.Join(directory, "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return err
	}
	defer generation.Close()
	caller := Identity{UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}}
	for _, blocked := range []bool{false, true} {
		if err := exerciseBootstrapNotice(generation.MountTransition, caller, blocked); err != nil {
			return err
		}
	}
	return nil
}

func exerciseBootstrapNotice(image *os.File, caller Identity, blocked bool) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	writerFD := int(writer.Fd())
	if blocked {
		if err := unix.SetNonblock(writerFD, true); err != nil {
			return err
		}
		var fill [4096]byte
		for {
			if _, err := unix.Write(writerFD, fill[:]); errors.Is(err, unix.EAGAIN) {
				break
			} else if err != nil {
				return err
			}
		}
		if err := unix.SetNonblock(writerFD, false); err != nil {
			return err
		}
	}
	previous := os.Stderr
	os.Stderr = writer
	pid, pin, err := startBootstrapNotice(image, caller)
	os.Stderr = previous
	if err != nil {
		return err
	}
	defer pin.Close()
	reaped := false
	defer func() {
		if !reaped {
			_ = unix.PidfdSendSignal(int(pin.Fd()), unix.SIGKILL, nil, 0)
			var status unix.WaitStatus
			for {
				_, err := unix.Wait4(pid, &status, 0, nil)
				if !errors.Is(err, unix.EINTR) {
					break
				}
			}
		}
	}()
	if blocked {
		deadline := time.Now().Add(time.Second)
		for {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
			if err == nil && guardianThreadProfile(raw, caller) == nil {
				break
			}
			if !time.Now().Before(deadline) {
				return errors.New("blocked diagnostic did not drop to the full caller profile")
			}
			time.Sleep(time.Millisecond)
		}
		files, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			return err
		}
		if len(files) != 1 || files[0].Name() != "2" {
			return errors.New("caller diagnostic retained an authority descriptor")
		}
		expireBootstrapNotice(pin, time.Now())
	}
	var status unix.WaitStatus
	for {
		_, err = unix.Wait4(pid, &status, 0, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	reaped = true
	flags, err := unix.FcntlInt(uintptr(writerFD), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK != 0 {
		return errors.New("diagnostic altered shared stream flags")
	}
	_ = writer.Close()
	if blocked {
		if !status.Signaled() || status.Signal() != unix.SIGKILL {
			return errors.New("blocked diagnostic was not stopped through its owned pidfd")
		}
	} else {
		if !status.Exited() || status.ExitStatus() != 1 {
			return errors.New("fixed diagnostic did not complete")
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if string(raw) != "Painted Wolf Code: protected setup stopped. Waiting for its processes to exit.\n" {
			return errors.New("diagnostic output differs from the fixed caller-only message")
		}
	}
	return nil
}
