// Command pw-service-guest is PID 1 of a session's private Linux VM.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

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
