// Package privateguesthost launches a session's packaged service environment.
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

// OpenPrivateGuest activates the service userland attached to this engine.
func OpenPrivateGuest(ctx context.Context) (*serviceguest.Client, error) {
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
	return openPrivateGuestFrom(ctx, bundle)
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

func openPrivateGuestFrom(ctx context.Context, bundle string) (*serviceguest.Client, error) {
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
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "service host")
	child := os.NewFile(uintptr(pair[1]), "service activation")
	defer child.Close()
	transport, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		return nil, err
	}
	cmd, err := hostexec.StartPrivateService(image, child)
	if err != nil {
		_ = transport.Close()
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	_ = child.Close()
	channel := &linuxServiceConnection{Conn: transport, done: make(chan struct{})}
	go func() { channel.status = cmd.Wait(); close(channel.done); _ = channel.Conn.Close() }()
	client, err := serviceguest.Open(ctx, channel)
	if err != nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	return client, nil
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
