package egress

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/hostproxy"
	"github.com/lycaon/lycaon/internal/socketref"
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

// UnixSocketTransport dials only a retained approved object, with proxies disabled.
func UnixSocketTransport(binding socketref.Binding) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		lease, err := binding.Acquire()
		if err != nil {
			return nil, err
		}
		defer lease.Close()
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return lease.DialContext(dialCtx)
	}
	return t
}
