// Package hostscope derives host-lease boundaries and their display text.
package hostscope

import (
	"net"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Pattern returns a host's registrable site pattern.
func Pattern(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.Trim(h, "[]")
	if h == "" {
		return ""
	}
	// An address has nothing above it to widen into.
	if net.ParseIP(h) != nil {
		return h
	}
	// A single label — localhost, a container alias — has no registrable parent.
	if !strings.Contains(h, ".") {
		return h
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil || domain == "" {
		// Widening stops at the public suffix boundary.
		return h
	}
	return "*." + domain
}

// TunnelPattern retains the port because opaque tunnel protocols are unobserved.
func TunnelPattern(host string, port uint16) string {
	site := Pattern(host)
	if site == "" || port == 0 {
		return site
	}
	return net.JoinHostPort(site, strconv.Itoa(int(port)))
}

// SplitTunnelPattern separates a tunnel pattern into its site pattern and port.
// A pattern with no port reports port 0 and is a readable-request lease.
func SplitTunnelPattern(pattern string) (site string, port uint16) {
	p := strings.TrimSpace(pattern)
	host, portText, err := net.SplitHostPort(p)
	if err != nil || host == "" {
		return p, 0
	}
	n, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || n == 0 {
		return p, 0
	}
	return host, uint16(n)
}

// Coverage names the lease's host range and any port restriction.
func Coverage(pattern string) string {
	site, port := SplitTunnelPattern(pattern)
	suffix := ""
	if port != 0 {
		suffix = " on port " + strconv.Itoa(int(port))
	}
	if domain, ok := strings.CutPrefix(site, "*."); ok {
		return "connections to `" + domain + "` and its subdomains" + suffix
	}
	return "connections to `" + site + "`" + suffix
}

// AllowLine describes the host range derived from the lease pattern.
func AllowLine(host string) string {
	pattern := Pattern(host)
	if domain, ok := strings.CutPrefix(pattern, "*."); ok {
		return "connections to " + domain + " and its subdomains"
	}
	return "connections to " + pattern
}
