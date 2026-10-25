package egress

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/hostproxy"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PinnedTransport dials only validated IPs for target through current proxy policy; a nonzero
// targetPort replaces the requested port.
func PinnedTransport(target *url.URL, ips []netip.Addr, targetPort uint16) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	host := ""
	if target != nil {
		host = target.Hostname()
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host == "" || len(ips) == 0 {
			return nil, &DestinationDeniedError{Reason: "blocked: destination has no authorized addresses"}
		}
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
			numericPort, err := strconv.ParseUint(dialPort, 10, 16)
			if err != nil || numericPort == 0 {
				return nil, fmt.Errorf("invalid pinned port")
			}
			conn, err := hostproxy.DialPinned(ctx, target, network, netip.AddrPortFrom(ip, uint16(numericPort)))
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
