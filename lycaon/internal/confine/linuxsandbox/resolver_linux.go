//go:build linux

package linuxsandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const resolvConf = "/etc/resolv.conf"

// resolverQueryTimeout bounds one relayed query.
const resolverQueryTimeout = 5 * time.Second

const (
	handoffResolverUDP = "resolver_udp"
	handoffResolverTCP = "resolver_tcp"
)

// handOffResolver binds the stub inside the sandbox, points resolv.conf at it
// when the host names another resolver, and passes both sockets to the
// supervisor. A host without resolv.conf names no resolver, so the sandbox
// gets no stub; one it cannot read fails the launch.
func handOffResolver() error {
	raw, err := os.ReadFile(resolvConf)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", resolvConf, err)
	}
	if content := sandboxResolvConf(string(raw)); content != "" {
		if err := overlayFile(resolvConf, content); err != nil {
			return mountFailure(resolvConf, err)
		}
	}
	family, sa := sockaddrOf(sandboxResolver)
	udp, err := unix.Socket(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	if err := unix.Bind(udp, sa); err != nil {
		_ = unix.Close(udp)
		return err
	}
	err = sendHandoff(handoff{Kind: handoffResolverUDP, Addr: sandboxResolver.String()}, udp)
	_ = unix.Close(udp)
	if err != nil {
		return err
	}
	tcp, err := listenTCP(sandboxResolver)
	if err != nil {
		return err
	}
	err = sendHandoff(handoff{Kind: handoffResolverTCP, Addr: sandboxResolver.String()}, tcp)
	_ = unix.Close(tcp)
	return err
}

// overlayFile covers path, through its symbolic links, with a read-only file
// holding content.
func overlayFile(path, content string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	tmpfs, err := newTmpfs("0755", maskTmpfsSize)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(tmpfs) }()
	fd, err := unix.Openat(tmpfs, "file", unix.O_CREAT|unix.O_WRONLY|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return err
	}
	_, err = unix.Write(fd, []byte(content))
	_ = unix.Close(fd)
	if err != nil {
		return err
	}
	clone, err := unix.OpenTree(tmpfs, "file", unix.OPEN_TREE_CLONE|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(clone) }()
	if err := unix.MountSetattr(clone, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}); err != nil {
		return err
	}
	return unix.MoveMount(clone, "", unix.AT_FDCWD, real, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// relayResolverDatagrams answers each query the stub receives from the host's
// first resolver, replying from the stub's address.
func relayResolverDatagrams(stub net.PacketConn, upstream netip.AddrPort) {
	buf := make([]byte, 65535)
	for {
		n, from, err := stub.ReadFrom(buf)
		if err != nil {
			return
		}
		query := append([]byte(nil), buf[:n]...)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), resolverQueryTimeout)
			defer cancel()
			conn, err := (&net.Dialer{}).DialContext(ctx, "udp", upstream.String())
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(resolverQueryTimeout))
			if _, err := conn.Write(query); err != nil {
				return
			}
			reply := make([]byte, 65535)
			m, err := conn.Read(reply)
			if err != nil {
				return
			}
			_, _ = stub.WriteTo(reply[:m], from)
		}()
	}
}
