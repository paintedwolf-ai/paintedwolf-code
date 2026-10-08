package confine

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func writeNetworkRules(b *strings.Builder, c Confinement) error {
	// Direct IP and Browser carry local listen; otherwise only an explicit grant.
	if c.Network == NetworkDirectIP || c.Browser {
		writeLocalListenRules(b)
	} else if c.LocalListen {
		writeListenGrantRules(b, c.LocalListenPorts)
	}
	// One fact carries local outbound authority and its port limits.
	if c.Network != NetworkDirectIP && c.LoopbackConnect {
		writeLoopbackConnectRules(b, c.LoopbackConnectPorts)
	}
	switch c.Network {
	case NetworkDirectIP:
		// Exact local sockets remain a separate capability.
		b.WriteString("(allow network-outbound (remote ip \"localhost:*\"))\n")
		writeDirectIPRules(b, c.DirectIPPermits)
		writeSystemResolverRule(b)
	case NetworkProxyOnly:
		if err := writeMediatedProxyRules(b, c); err != nil {
			return err
		}
	case NetworkDeny:
		// Exact local socket grants are rendered below.
	}
	writeSocketGrantRules(b, c.SocketGrants)
	return nil
}

// writeLoopbackConnectRules grants outbound TCP/UDP to the specified local ports.
func writeLoopbackConnectRules(b *strings.Builder, ports []uint16) {
	if len(ports) == 0 {
		b.WriteString("(allow network-outbound (remote ip \"localhost:*\"))\n")
		return
	}
	for _, p := range ports {
		b.WriteString("(allow network-outbound (remote ip " + sbplString("localhost:"+strconv.Itoa(int(p))) + "))\n")
	}
}

// writeMediatedProxyRules grants only the action lease endpoints.
func writeMediatedProxyRules(b *strings.Builder, c Confinement) error {
	seen := map[string]struct{}{}
	for _, addr := range []string{c.ProxyAddr, c.SocksProxyAddr} {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		host, portText, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid mediated proxy address %q: %w", addr, err)
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		port, err := strconv.Atoi(portText)
		if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid mediated proxy endpoint %q", addr)
		}
		endpoint := "localhost:" + strconv.Itoa(port)
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		seen[endpoint] = struct{}{}
		b.WriteString("(allow network-outbound (remote tcp " + sbplString(endpoint) + "))\n")
	}
	if len(seen) == 0 {
		return fmt.Errorf("proxy-only confinement has no bound action endpoint")
	}
	return nil
}

// writeLocalListenRules grants the backend's host-local bind scope.
func writeLocalListenRules(b *strings.Builder) {
	b.WriteString("(allow network-bind (local ip \"localhost:*\"))\n")
	b.WriteString("(allow network-inbound (local ip \"localhost:*\"))\n")
}

// writeListenGrantRules narrows an approved listener by port.
func writeListenGrantRules(b *strings.Builder, ports []uint16) {
	if len(ports) == 0 {
		writeLocalListenRules(b)
		return
	}
	for _, p := range ports {
		endpoint := sbplString("localhost:" + strconv.Itoa(int(p)))
		b.WriteString("(allow network-bind (local ip " + endpoint + "))\n")
		b.WriteString("(allow network-inbound (local ip " + endpoint + "))\n")
	}
}

// writeDirectIPRules narrows direct IP by transport and port, not peer.
func writeDirectIPRules(b *strings.Builder, permits []DirectIPPermit) {
	if len(permits) == 0 {
		b.WriteString("(allow network-outbound (remote ip \"*:*\"))\n")
		return
	}
	b.WriteString("; direct IP narrowed to declared transports (host is not expressible in SBPL)\n")
	for _, p := range permits {
		port := strconv.Itoa(int(p.Port))
		b.WriteString("(allow network-outbound (remote " + p.Protocol + " " + sbplString("*:"+port) + "))\n")
	}
}

// systemResolverSocketPaths includes both canonical resolver aliases.
var systemResolverSocketPaths = []string{
	"/var/run/mDNSResponder",
	"/private/var/run/mDNSResponder",
}

// writeSystemResolverRule enables names only for direct-IP egress.
func writeSystemResolverRule(b *strings.Builder) {
	b.WriteString("; system resolver: names for direct IP, which already reaches any resolver\n")
	for _, path := range systemResolverSocketPaths {
		b.WriteString("(allow network-outbound (literal " + sbplString(path) + "))\n")
	}
}

func writeSocketGrantRules(b *strings.Builder, grants []SocketGrant) {
	if len(grants) == 0 {
		return
	}
	b.WriteString("; exact AF_UNIX socket grants\n")
	for _, g := range grants {
		b.WriteString("(allow network-outbound (literal " + sbplString(g.ResolvedPath) + "))\n")
	}
}
