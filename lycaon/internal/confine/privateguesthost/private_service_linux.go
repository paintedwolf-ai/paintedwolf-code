package privateguesthost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	hostexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/installbinding"
	"github.com/lycaon/lycaon/internal/nixgeneration"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"golang.org/x/sys/unix"
)

func launchService(ctx context.Context) (net.Conn, error) {
	host, err := os.Executable()
	if err != nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	layout, err := nixgeneration.Running()
	if err != nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	bundle, err := serviceBundle(host, layout, configdir.IsDevelopmentBuild())
	if err != nil {
		return nil, err
	}
	return launchServiceBundle(ctx, bundle)
}
func serviceBundle(host string, layout *nixgeneration.Layout, development bool) (string, error) {
	root := configlayout.LinuxInstallRoot
	if layout != nil {
		root = layout.Root
	}
	if filepath.Dir(host) == filepath.Join(root, "bin") {
		return filepath.Join(root, configlayout.LinuxEngineRootDirName, "service-guest"), nil
	}
	if development && filepath.IsAbs(host) {
		return filepath.Join(filepath.Dir(host), "service-guest"), nil
	}
	return "", errors.Join(serviceguest.ErrUnavailable, errors.New("private service package is missing"))
}

func launchServiceBundle(ctx context.Context, bundle string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	image := filepath.Join(bundle, "rootfs", "init")
	if err := installbinding.Verify(image); err != nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	if info, err := os.Stat(image); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, errors.Join(serviceguest.ErrUnavailable, fmt.Errorf("invalid private service executable %q", image), err)
	}
	transport, child, err := activationChannel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = child.Close() }()
	cmd, err := hostexec.StartPrivateService(image, child)
	if err != nil {
		_ = transport.Close()
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	channel := &linuxServiceConnection{Conn: transport, done: make(chan struct{})}
	go func() { channel.status = cmd.Wait(); close(channel.done); _ = channel.Conn.Close() }()
	return channel, nil
}

// The launcher waits for namespace PID 1 and the kernel's descendant teardown.
type linuxServiceConnection struct {
	net.Conn
	done   chan struct{}
	status error
}

func (c *linuxServiceConnection) Close() error {
	_ = c.Conn.Close()
	<-c.done
	return c.status
}

func activationDescriptors() ([2]int, error) {
	return unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
}
