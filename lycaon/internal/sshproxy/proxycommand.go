// Package sshproxy mediates OpenSSH ProxyCommand streams through the host egress broker.
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

// RunProxyCommand connects to host:port through the managed Git SSH service socket
// when LYCAON_GIT_SSH_SOCKET is set, or through the shared SOCKS listener otherwise.
func RunProxyCommand(ctx context.Context, host, port string) error {
	return runProxyCommand(ctx, host, port, os.Stdin, os.Stdout)
}

func runProxyCommand(ctx context.Context, host, port string, in io.Reader, out io.Writer) error {
	if socketPath := strings.TrimSpace(os.Getenv(SocketEnv)); socketPath != "" {
		return runManagedProxyCommand(ctx, socketPath, host, port, in, out)
	}
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
	return relayClientStdio(ctx, conn, in, out)
}

func runManagedProxyCommand(ctx context.Context, socketPath, host, port string, in io.Reader, out io.Writer) error {
	conn, _, err := Connect(ctx, socketPath, host, port)
	if err != nil {
		return err
	}
	return relayClientStdio(ctx, conn, in, out)
}

func relayClientStdio(ctx context.Context, conn net.Conn, in io.Reader, out io.Writer) error {
	defer func() { _ = conn.Close() }()
	touch := func() {
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
			return
		}
		_ = conn.SetDeadline(time.Now().Add(relayIdleTimeout))
	}
	touch()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(touchWriter{w: conn, touch: touch}, in)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(out, touchReader{r: conn, touch: touch})
		errCh <- err
	}()
	var first error
	for range 2 {
		err := <-errCh
		if first == nil && err != nil {
			first = err
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return first
}
