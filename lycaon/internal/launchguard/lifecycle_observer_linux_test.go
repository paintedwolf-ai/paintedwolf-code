//go:build linux

package launchguard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

const lifecycleObserverEnv = "PW_FIXTURE_LIFECYCLE_OBSERVER"

type lifecycleSample struct {
	MonotonicNS    int64               `json:"monotonic_ns"`
	Ready          [3]bool             `json:"ready"`
	DesktopRecheck *lifecycleReadiness `json:"desktop_recheck,omitempty"`
}

type lifecycleReadiness struct {
	BeginNS int64 `json:"begin_ns"`
	EndNS   int64 `json:"end_ns"`
	Ready   bool  `json:"ready"`
}

type lifecycleObservation struct {
	Roles      []lifecycleRole   `json:"roles"`
	Samples    []lifecycleSample `json:"samples"`
	Checkpoint bool              `json:"checkpoint,omitempty"`
}

// The observer retains pidfds only. Retaining a health/lifetime/socket endpoint
// would change peer EOF and contaminate the teardown being measured.
func observeFixtureLifecycle(t *testing.T, fixture launchFixture, launch runningLaunch) (func(), func()) {
	t.Helper()
	args := []string{"-n", "env", lifecycleObserverEnv + "=1", fixtureDirEnv + "=" + fixture.dir,
		fixtureUIDEnv + "=" + strconv.Itoa(os.Getuid()), fixtureDesktopPIDEnv + "=" + strconv.Itoa(launch.facts.PID),
		"PW_FIXTURE_DESKTOP_START=" + strconv.FormatUint(launch.facts.ProcessStarts[launch.facts.PID], 10),
		"PW_FIXTURE_BOOTSTRAP_PID=" + strconv.Itoa(launch.cmd.Process.Pid), os.Args[0],
		"-test.run=^TestProtectedLaunchLifecycleObserver$"}
	cmd := exec.Command("sudo", args...)
	control, err := cmd.StdinPipe()
	testutil.FailErr(t, "open owned observer checkpoint control", err)
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "open lifecycle observer report", err)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	testutil.FailErr(t, "start owned lifecycle observer", cmd.Start())
	lines := make(chan []byte, 3)
	done := make(chan struct{})
	var waitErr error
	var scanErr error
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- bytes.Clone(scanner.Bytes())
		}
		scanErr = scanner.Err()
		close(lines)
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = control.Close()
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})
	select {
	case line, ok := <-lines:
		if !ok || string(line) != `{"ready":true}` {
			<-done
			t.Fatalf("lifecycle observation unavailable: report=%s process=%v reader=%v; %s", line, waitErr, scanErr, stderr.String())
		}
	case <-time.After(mountSourceSetupLimit):
		t.Fatal("owned lifecycle observer did not authenticate before setup deadline")
	}
	finalConsumed := false
	finish := func() {
		if !finalConsumed {
			select {
			case line, ok := <-lines:
				if !ok {
					<-done
					t.Errorf("lifecycle observer closed before its final report: process=%v reader=%v; %s", waitErr, scanErr, stderr.String())
					return
				}
				var result lifecycleObservation
				if err := json.Unmarshal(line, &result); err != nil {
					t.Errorf("decode exact-role lifecycle observation %q: %v", line, err)
				} else {
					t.Logf("registered role lifecycle observation: %s", line)
				}
			case <-time.After(20 * time.Second):
				t.Error("registered role lifecycle observation did not settle within launch exit bound")
			}
		}
		<-done
		for line := range lines {
			t.Logf("remaining bounded lifecycle observation: %s", line)
		}
		if scanErr != nil {
			t.Errorf("read lifecycle observation: %v", scanErr)
		}
		if waitErr != nil {
			t.Errorf("lifecycle observation unavailable: %v; %s", waitErr, stderr.String())
		}
	}
	checkpoint := func() {
		if _, err := control.Write([]byte{'s'}); err != nil {
			t.Errorf("request pre-close lifecycle checkpoint: %v", err)
			return
		}
		select {
		case line := <-lines:
			var result lifecycleObservation
			if err := json.Unmarshal(line, &result); err != nil {
				t.Errorf("pre-close lifecycle checkpoint unavailable: %s (%v)", line, err)
			} else {
				t.Logf("pre-close exact-role lifecycle checkpoint: %s", line)
				finalConsumed = !result.Checkpoint
			}
		case <-time.After(5 * time.Second):
			t.Error("pre-close lifecycle checkpoint did not arrive within observation lifetime")
		}
	}
	return finish, checkpoint
}

func TestProtectedLaunchLifecycleObserver(t *testing.T) {
	if os.Getenv(lifecycleObserverEnv) == "" {
		return
	}
	if err := runLifecycleObserver(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runLifecycleObserver() error {
	owned, err := captureFixtureLifecyclePins()
	if err != nil {
		return err
	}
	defer owned.close()
	result := lifecycleObservation{Roles: owned.roles}
	pins := owned.pins
	began := time.Now()
	previous := [3]bool{}
	polls := make([]unix.PollFd, 4)
	for index, pin := range pins {
		polls[index] = unix.PollFd{Fd: int32(pin), Events: unix.POLLIN}
	}
	polls[3] = unix.PollFd{Fd: 0, Events: unix.POLLIN}
	if _, err := unix.Poll(polls[:3], 0); err != nil {
		return err
	}
	for _, poll := range polls[:3] {
		if poll.Revents != 0 {
			return errors.New("registered role ended before observation admission")
		}
	}
	if _, err := fmt.Fprintln(os.Stdout, `{"ready":true}`); err != nil {
		return err
	}
	result.Samples = append(result.Samples, lifecycleSample{MonotonicNS: lifecycleMonotonicNS()})
	deadline := began.Add(mountSourceSetupLimit + 20*time.Second)
	for time.Now().Before(deadline) {
		if _, err := unix.Poll(polls, 1000); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		ready := previous
		for index, poll := range polls[:3] {
			if poll.Fd < 0 {
				continue
			}
			ended, err := lifecyclePIDFDReady(poll.Revents)
			if err != nil {
				return fmt.Errorf("registered role %d pidfd observation: %w", index, err)
			}
			ready[index] = ended
			if ready[index] {
				polls[index].Fd = -1 // Process death is irreversible for this retained pidfd.
			}
		}
		checkpoint := polls[3].Revents&unix.POLLIN != 0
		if checkpoint {
			var request [1]byte
			n, err := unix.Read(0, request[:])
			if err != nil || n != 1 || request[0] != 's' {
				return errors.New("invalid owned lifecycle checkpoint request")
			}
			polls[3].Fd = -1 // Exactly one bounded pre-cleanup checkpoint.
		} else if polls[3].Revents != 0 {
			return errors.New("owned lifecycle checkpoint control closed")
		}
		if ready != previous || checkpoint {
			sample := lifecycleSample{MonotonicNS: lifecycleMonotonicNS(), Ready: ready}
			if !ready[0] && ((ready[1] && !previous[1]) || (ready[2] && !previous[2])) {
				check := &lifecycleReadiness{BeginNS: lifecycleMonotonicNS()}
				desktop := []unix.PollFd{{Fd: int32(pins[0]), Events: unix.POLLIN}}
				if _, err := unix.Poll(desktop, 0); err != nil {
					return err
				}
				check.EndNS = lifecycleMonotonicNS()
				check.Ready, err = lifecyclePIDFDReady(desktop[0].Revents)
				if err != nil {
					return fmt.Errorf("desktop pidfd recheck: %w", err)
				}
				sample.DesktopRecheck = check
			}
			result.Samples = append(result.Samples, sample)
			previous = ready
		}
		if checkpoint {
			partial := result
			partial.Checkpoint = true
			if err := json.NewEncoder(os.Stdout).Encode(partial); err != nil {
				return err
			}
		}
		if ready == [3]bool{true, true, true} {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	return errors.New("exact registered roles did not settle within observation bound")
}

func lifecycleMonotonicNS() int64 {
	var stamp unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &stamp); err != nil {
		panic(fmt.Errorf("read fixture monotonic clock: %w", err))
	}
	if stamp.Nano() <= 0 {
		panic("fixture monotonic clock is not positive")
	}
	return stamp.Nano()
}
