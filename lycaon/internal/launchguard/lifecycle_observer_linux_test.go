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
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

const lifecycleObserverEnv = "PW_FIXTURE_LIFECYCLE_OBSERVER"

type lifecycleRole struct {
	Role   string `json:"role"`
	PID    int    `json:"pid"`
	Parent int    `json:"parent"`
	Start  uint64 `json:"start"`
}

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
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- bytes.Clone(scanner.Bytes())
		}
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
	case line := <-lines:
		if string(line) != `{"ready":true}` {
			<-done
			t.Fatalf("lifecycle observation unavailable: %s; %s", line, stderr.String())
		}
	case <-time.After(mountSourceSetupLimit):
		t.Fatal("owned lifecycle observer did not authenticate before setup deadline")
	}
	finalConsumed := false
	finish := func() {
		if !finalConsumed {
			select {
			case line := <-lines:
				var result lifecycleObservation
				testutil.FailErr(t, "decode exact-role lifecycle observation", json.Unmarshal(line, &result))
				t.Logf("registered role lifecycle observation: %s", line)
			case <-time.After(20 * time.Second):
				t.Error("registered role lifecycle observation did not settle within launch exit bound")
			}
		}
		<-done
		for line := range lines {
			t.Logf("remaining bounded lifecycle observation: %s", line)
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
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return errors.New("lifecycle observer requires initial-root fixture inspection")
	}
	pid, err := strconv.Atoi(os.Getenv(fixtureDesktopPIDEnv))
	if err != nil || pid <= 1 {
		return errors.New("invalid fixture desktop PID")
	}
	start, err := strconv.ParseUint(os.Getenv("PW_FIXTURE_DESKTOP_START"), 10, 64)
	if err != nil || start == 0 {
		return errors.New("missing creator-recorded desktop birth")
	}
	uid, err := strconv.ParseUint(os.Getenv(fixtureUIDEnv), 10, 32)
	if err != nil || uid == 0 {
		return errors.New("invalid fixture caller identity")
	}
	bootstrap, err := strconv.Atoi(os.Getenv("PW_FIXTURE_BOOTSTRAP_PID"))
	if err != nil || bootstrap <= 1 {
		return errors.New("invalid owned fixture bootstrap")
	}
	policyPath = filepath.Join(os.Getenv(fixtureDirEnv), "launch.json")
	lockDirectory = filepath.Join(os.Getenv(fixtureDirEnv), "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return err
	}
	defer generation.Close()
	desktop, err := lifecycleBirth(pid)
	if err != nil || desktop.Start != start {
		return errors.New("desktop birth differs from launch report")
	}
	guardian, err := lifecycleBirth(desktop.Parent)
	if err != nil || guardian.Parent != bootstrap {
		return errors.New("guardian is not the owned bootstrap child")
	}
	guardFD, err := unix.PidfdOpen(guardian.PID, 0)
	if err != nil {
		return err
	}
	defer unix.Close(guardFD)
	guardDir, err := unix.Open(fmt.Sprintf("/proc/%d", guardian.PID), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(guardDir)
	if err := verifyFixtureBirth(guardDir, guardian.PID, guardian.Start, uint32(uid)); err != nil {
		return err
	}
	matched, err := matchFixtureMountRole(guardDir, bootstrap, GuardianArgument, generation.Entry)
	if err != nil || !matched {
		return fmt.Errorf("guardian image/role differs from owned selected entry: %v", err)
	}
	var result lifecycleObservation
	var pins []int
	defer func() {
		for _, pin := range pins {
			_ = unix.Close(pin)
		}
	}()
	for index, name := range []string{"desktop", "mount-watcher", "mount-intake"} {
		pin, err := unix.PidfdGetfd(guardFD, 15+index, 0)
		if err != nil {
			return fmt.Errorf("duplicate authenticated %s pidfd: %w", name, err)
		}
		pins = append(pins, pin)
		rolePID, err := lifecyclePIDFDIdentity(pin)
		if err != nil {
			return err
		}
		birth, err := lifecycleBirth(rolePID)
		if err != nil || birth.Parent != guardian.PID {
			return errors.New("registered role has another guardian")
		}
		birth.Role = name
		directory, err := unix.Open(fmt.Sprintf("/proc/%d", rolePID), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		verifyErr := verifyFixtureBirth(directory, rolePID, birth.Start, uint32(uid))
		if verifyErr == nil && index == 0 {
			if rolePID != pid || birth.Start != start {
				verifyErr = errors.New("desktop pidfd differs from exact fixture birth")
			} else {
				verifyErr = lifecycleDesktopImage(rolePID, generation.Desktop)
			}
		} else if verifyErr == nil {
			role := MountViewArgument
			if index == 2 {
				role = MountIntakeArgument
			}
			matched, matchErr := matchFixtureMountRole(directory, guardian.PID, role, generation.Entry)
			if matchErr != nil || !matched {
				verifyErr = fmt.Errorf("registered %s image/role differs: %v", name, matchErr)
			}
		}
		_ = unix.Close(directory)
		if verifyErr != nil {
			return verifyErr
		}
		result.Roles = append(result.Roles, birth)
	}
	currentGuardian, err := lifecycleBirth(guardian.PID)
	if err != nil || currentGuardian != guardian {
		return errors.New("guardian birth changed during pidfd authentication")
	}
	// Authentication no longer needs the selected image/lock descriptors.
	// Close them before admission so only pidfds survive into observation.
	generation.Close()
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
			if poll.Revents & ^int16(unix.POLLIN) != 0 {
				return errors.New("pidfd observation returned an unexpected event")
			}
			ready[index] = poll.Revents&unix.POLLIN != 0
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
				if desktop[0].Revents & ^int16(unix.POLLIN) != 0 {
					return errors.New("desktop recheck returned an unexpected pidfd event")
				}
				check.Ready = desktop[0].Revents&unix.POLLIN != 0
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

func lifecyclePIDFDIdentity(fd int) (int, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && key == "Pid" {
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || pid <= 1 {
				return 0, errors.New("authenticated pidfd has no living PID")
			}
			return pid, nil
		}
	}
	return 0, errors.New("authenticated descriptor is not a pidfd")
}

func lifecycleBirth(pid int) (lifecycleRole, error) {
	role := lifecycleRole{PID: pid}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return role, err
	}
	begin, end := bytes.IndexByte(raw, '('), bytes.LastIndexByte(raw, ')')
	if begin < 1 || end < begin {
		return role, errors.New("process birth lacks command field")
	}
	actual, err := strconv.Atoi(strings.TrimSpace(string(raw[:begin])))
	if err != nil || actual != pid {
		return role, errors.New("process birth names another PID")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return role, errors.New("process birth is incomplete")
	}
	role.Parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return role, err
	}
	role.Start, err = strconv.ParseUint(fields[19], 10, 64)
	return role, err
}

func lifecycleDesktopImage(pid int, selected *os.File) error {
	file, err := os.Open(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	wanted, err := selected.Stat()
	if err != nil || !os.SameFile(actual, wanted) {
		return errors.New("observed desktop image differs from selected fixture generation")
	}
	return nil
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
