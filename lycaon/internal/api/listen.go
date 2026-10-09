package api

import (
	"fmt"
	"net"
	"os"

	"github.com/lycaon/lycaon/internal/egress"
)

const DefaultListenAddr = "127.0.0.1:8787"

// ResolveListenAddr returns the sidecar bind address from LYCAON_ADDR or the default.
// Non-loopback and wildcard binds require LYCAON_BIND_ALL=1.
func ResolveListenAddr() (string, error) {
	addr := os.Getenv("LYCAON_ADDR")
	if addr == "" {
		return DefaultListenAddr, nil
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid LYCAON_ADDR %q: %w", addr, err)
	}

	if isWildcardBind(host) && !allowBindAll() {
		return "", fmt.Errorf("binding to %q requires LYCAON_BIND_ALL=1", host)
	}
	if !allowBindAll() && !egress.SyntacticLoopback(host) {
		return "", fmt.Errorf("bind address must be loopback, got %q", host)
	}
	return addr, nil
}

func allowBindAll() bool {
	return os.Getenv("LYCAON_BIND_ALL") == "1"
}

func isWildcardBind(host string) bool {
	switch host {
	case "0.0.0.0", "::", "[::]":
		return true
	default:
		return false
	}
}

// ParseListenHostPort splits a listen address into host and port.
func ParseListenHostPort(addr string) (host string, port int, err error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	var p int
	_, err = fmt.Sscanf(portStr, "%d", &p)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	return host, p, nil
}
