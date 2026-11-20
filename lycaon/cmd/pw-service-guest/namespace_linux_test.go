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
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGuestNamespaceActivation(t *testing.T) {
	if os.Getenv("PW_GUEST_TEST_NAMESPACE") == "1" {
		err := runNative("--enter-native")
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("serve private namespace: %v", err)
		}
		return
	}
	root := t.TempDir()
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
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	self, err := os.Executable()
	testutil.FailErr(t, "locate guest fixture", err)
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestGuestNamespaceActivation$")
	cmd.Env = []string{"PW_GUEST_TEST_NAMESPACE=1"}
	var output bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = child, &output, &output
	cmd.ExtraFiles = []*os.File{pin}
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS, UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}, GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}, Pdeathsig: syscall.SIGKILL}
	testutil.FailErr(t, "start isolated guest fixture", cmd.Start())
	testutil.FailErr(t, "release child endpoint", child.Close())
	client, err := serviceguest.Open(ctx, channel)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("guest readiness: %v\n%s", err, &output)
	}
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
		var exit *exec.ExitError
		if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != want {
			t.Fatalf("%s exit = %v, want %d", mode, err, want)
		}
	}
}
