// Command pw-service-guest is PID 1 of a session's private Linux VM.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine/privateguesthost/linuxruntime"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--acquire-native" || os.Args[1] == "--launch-native" || os.Args[1] == "--enter-native") {
		if err := runNative(os.Args[1]); err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stderr, "private service environment:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 || os.Getpid() != 1 || os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "private service controller requires guest PID 1")
		os.Exit(2)
	}
	if err := run(); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "private service guest:", err)
	}
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(1)
}

func run() error {
	config, err := prepareVM()
	if err != nil {
		return err
	}
	controller, err := hostChannel()
	if err != nil {
		return err
	}
	return serviceguest.Serve(context.Background(), controller, newJobs(config))
}

func hostChannel() (*os.File, error) {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return nil, err
	}
	if slices.Contains(strings.Fields(string(cmdline)), linuxruntime.ConsoleArgument) {
		return privateConsole("/sys/class/virtio-ports", "/dev", time.Now().Add(15*time.Second))
	}
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	if err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: serviceguest.Port}); err != nil {
		return nil, err
	}
	if err = unix.Listen(fd, 1); err != nil {
		return nil, err
	}
	for {
		peer, address, err := unix.Accept4(fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if remote, ok := address.(*unix.SockaddrVM); ok && remote.CID == unix.VMADDR_CID_HOST {
			return os.NewFile(uintptr(peer), "private host channel"), nil
		}
		_ = unix.Close(peer)
	}
}

func privateConsole(ports, devices string, deadline time.Time) (*os.File, error) {
	for {
		entries, err := os.ReadDir(ports)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			name, err := os.ReadFile(filepath.Join(ports, entry.Name(), "name"))
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(string(name)) == linuxruntime.ConsoleName {
				return os.OpenFile(filepath.Join(devices, entry.Name()), os.O_RDWR|unix.O_NOCTTY, 0)
			}
		}
		if time.Now().After(deadline) {
			return nil, errors.New("private controller port unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
