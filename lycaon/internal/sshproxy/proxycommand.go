// Package sshproxy implements OpenSSH ProxyCommand mediation through the egress SOCKS front door.
package sshproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// RunProxyCommand connects to host:port through the broker's SOCKS front door
// and relays stdio. Env: LYCAON_SOCKS_PROXY (host:port). The broker attributes
// the connection through this process's descent from the confined command.
func RunProxyCommand(ctx context.Context, host, port string) error {
	return runProxyCommand(ctx, host, port, os.Stdin, os.Stdout)
}

func runProxyCommand(ctx context.Context, host, port string, in io.Reader, out io.Writer) error {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return fmt.Errorf("ssh-proxy-command requires host and port")
	}
	if strings.ContainsAny(host, "\x00\r\n\t") {
		return fmt.Errorf("invalid host")
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	socksAddr := strings.TrimSpace(os.Getenv("LYCAON_SOCKS_PROXY"))
	if socksAddr == "" {
		return fmt.Errorf("mediated ssh env missing")
	}
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNum == 0 {
		return fmt.Errorf("invalid port")
	}
	d, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		return fmt.Errorf("socks dialer: %w", err)
	}
	dest := net.JoinHostPort(host, port)
	conn, err := d.Dial("tcp", dest)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	}
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, in)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		errCh <- err
	}()
	go func() { _, err := io.Copy(out, conn); errCh <- err }()
	var first error
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return ctx.Err()
		case err := <-errCh:
			if first == nil && err != nil {
				first = err
			}
		}
	}
	return first
}
