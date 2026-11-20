package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGuestNamespaceActivation(t *testing.T) {
	if os.Getenv("PW_GUEST_TEST_NAMESPACE") == "1" {
		if os.Getenv("PW_GUEST_TEST_COVERAGE") == "1" {
			unix.CloseOnExec(4)
		}
		err := runNative("--enter-native")
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("serve private namespace: %v", err)
		}
		return
	}
	root := t.TempDir()
	image := filepath.Join(root, "init")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", image, "./testdata/workload")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static guest workload: %v\n%s", err, out)
	}
	testutil.FailErr(t, "create program directory", os.Mkdir(filepath.Join(root, "bin"), 0755))
	testutil.FailErr(t, "link guest workload", os.Symlink("/init", filepath.Join(root, "bin/workload")))
	for _, name := range []string{"dev", "proc", "tmp", "home/service"} {
		testutil.FailErr(t, "create root mount point", os.MkdirAll(filepath.Join(root, name), 0755))
	}
	testutil.FailErr(t, "link workspace", os.Symlink("home/service/.config/paintedwolf/scratch/session", filepath.Join(root, "work")))
	pin, err := os.Open(root)
	testutil.FailErr(t, "pin packaged root", err)
	defer pin.Close()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "open activation stream", err)
	parent, child := os.NewFile(uintptr(pair[0]), "controller"), os.NewFile(uintptr(pair[1]), "namespace")
	channel, err := net.FileConn(parent)
	_ = parent.Close()
	testutil.FailErr(t, "initialize pollable controller", err)
	defer channel.Close()
	defer child.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := namespaceCommand(ctx, pin)
	cmd.Args = []string{cmd.Path, "-test.run=^TestGuestNamespaceActivation$"}
	cmd.Env = []string{"PW_GUEST_TEST_NAMESPACE=1"}
	var output bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = child, &output, &output
	if directory := guestTestCoverageDir(); directory != "" {
		counters, err := os.Open(directory)
		testutil.FailErr(t, "open child counter directory", err)
		defer counters.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, counters)
		cmd.Env = append(cmd.Env, "PW_GUEST_TEST_COVERAGE=1")
		cmd.Args = append(cmd.Args, "-test.gocoverdir=/proc/self/fd/4")
	}
	testutil.FailErr(t, "start isolated guest fixture", cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	testutil.FailErr(t, "release child endpoint", child.Close())
	client, err := serviceguest.Open(ctx, channel)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("guest readiness: %v\n%s", err, &output)
	}
	t.Cleanup(func() { _ = client.Close() })
	input := bytes.Repeat([]byte("a"), serviceguest.MaxOutput+1)
	result, err := client.RunInput(ctx, []string{"/bin/workload", "streams"}, input)
	testutil.FailErr(t, "run confined workload", err)
	if result.ExitCode != 7 || !result.StdoutTruncated || !bytes.Equal(result.Stdout, input[:serviceguest.MaxOutput]) || string(result.Stderr) != "uid=65534 gid=65534" {
		t.Fatalf("streams code=%d, bytes=%d, truncated=%v, identity=%q", result.ExitCode, len(result.Stdout), result.StdoutTruncated, result.Stderr)
	}
	active, err := client.Start(ctx, []string{"/bin/workload", "hold"})
	testutil.FailErr(t, "launch retained guest", err)
	status, err := client.Status(ctx, active.ID)
	testutil.FailErr(t, "inspect retained guest", err)
	if status.State != serviceguest.Running {
		t.Fatalf("active guest state %s, stderr %s", status.State, status.Stderr)
	}
	stopped, err := client.Stop(ctx, active.ID)
	testutil.FailErr(t, "stop retained guest", err)
	if stopped.State != serviceguest.Exited {
		t.Fatalf("stopped guest state %s", stopped.State)
	}
	_, err = client.Start(ctx, []string{"/bin/workload", "hold"})
	testutil.FailErr(t, "launch guest for controller teardown", err)
	testutil.FailErr(t, "close guest controller", client.Close())
	if err := cmd.Wait(); err != nil {
		t.Fatalf("guest shutdown: %v\n%s", err, &output)
	}
}

func TestGuestCLIRequiresActivation(t *testing.T) {
	if mode := os.Getenv("PW_GUEST_TEST_MODE"); mode != "" {
		os.Args = []string{os.Args[0], mode}
		main()
		return
	}
	self, err := os.Executable()
	testutil.FailErr(t, "locate guest command", err)
	for mode, want := range map[string]int{"--unexpected": 2, "--acquire-native": 1, "--launch-native": 1, "--enter-native": 1} {
		cmd := exec.CommandContext(t.Context(), self, "-test.run=^TestGuestCLIRequiresActivation$")
		cmd.Env = []string{"PW_GUEST_TEST_MODE=" + mode}
		if counters := guestTestCoverageDir(); counters != "" {
			cmd.Env = append(cmd.Env, "GOCOVERDIR="+counters)
		}
		var exit *exec.ExitError
		if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != want {
			t.Fatalf("%s exit = %v, want %d", mode, err, want)
		}
	}
}

func TestGuestSelectsOneAvailableCPU(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var before, after unix.CPUSet
	testutil.FailErr(t, "read available CPUs", unix.SchedGetaffinity(0, &before))
	defer func() { testutil.FailErr(t, "restore CPU affinity", unix.SchedSetaffinity(0, &before)) }()
	testutil.FailErr(t, "select controller CPU", pinAvailableCPU())
	testutil.FailErr(t, "inspect controller affinity", unix.SchedGetaffinity(0, &after))
	if after.Count() != 1 {
		t.Fatalf("expected one CPU, got %d", after.Count())
	}
	for i, mask := range after {
		if mask&^before[i] != 0 {
			t.Fatalf("selected CPU outside inherited affinity: %v", after)
		}
	}
}

func guestTestCoverageDir() string {
	if testing.CoverMode() == "" {
		return ""
	}
	if dir := os.Getenv("PW_GO_CHILD_COVERDIR"); dir != "" {
		return dir
	}
	return os.Getenv("GOCOVERDIR")
}
