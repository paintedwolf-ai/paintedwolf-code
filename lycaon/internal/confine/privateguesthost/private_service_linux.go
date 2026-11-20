// Package privateguesthost launches a session's packaged service environment.
package privateguesthost

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/installbinding"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// OpenPrivateGuest starts one bounded service environment in the user's manager.
func OpenPrivateGuest(ctx context.Context) (*serviceguest.Client, error) {
	root := configlayout.EngineRoot()
	if !filepath.IsAbs(root) {
		return nil, errors.Join(serviceguest.ErrUnavailable, errors.New("private service package is missing"))
	}
	return openPrivateGuestFrom(ctx, filepath.Join(root, "service-guest"))
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
	// The manager owns the complete process group, including descendants of PID 1.
	cmd := exec.Command("systemd-run", "--user", "--quiet", "--collect", "--pipe", "--wait", "--service-type=exec", "--property=TasksMax=256", "--property=MemoryMax=512M", "--property=CPUQuota=100%", "--property=LimitCORE=0", "--", image, "--launch-native")
	cmd.Stdin = child
	cmd.Stderr = os.Stderr
	cmd.Dir = "/"
	if err := cmd.Start(); err != nil {
		_ = transport.Close()
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	_ = child.Close()
	go func() { _ = cmd.Wait(); _ = transport.Close() }()
	boot, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := serviceguest.Open(boot, transport)
	if err != nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	return client, nil
}
