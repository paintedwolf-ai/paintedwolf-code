package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// PinnedTransport dials only validated IPs for host, with proxies disabled; a nonzero
// targetPort replaces the requested port.
func PinnedTransport(host string, ips []netip.Addr, targetPort uint16) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	d := net.Dialer{Timeout: 10 * time.Second}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		reqHost, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(reqHost, host) {
			return nil, &DestinationDeniedError{Reason: fmt.Sprintf("blocked: unexpected dial host %q", reqHost)}
		}
		dialPort := port
		if targetPort > 0 {
			dialPort = strconv.Itoa(int(targetPort))
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), dialPort))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return t
}

// UnixSocketTransport dials a local Unix domain socket, with proxies disabled.
func UnixSocketTransport(socketPath string) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	d := net.Dialer{Timeout: 10 * time.Second}
	t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return d.DialContext(ctx, "unix", socketPath)
	}
	return t
}
