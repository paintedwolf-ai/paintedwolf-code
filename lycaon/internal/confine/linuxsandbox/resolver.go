package linuxsandbox

import (
	"net/netip"
	"strings"
	"time"
)

// Direct IP reaches names through the system resolver, as it does on macOS.
// The sandbox's namespace has no route to it, so a stub address inside the
// sandbox answers, relayed by the supervisor to the host's resolver.

// sandboxResolver is the stub address resolv.conf names inside the sandbox.
var sandboxResolver = netip.MustParseAddrPort("127.0.0.53:53")

const resolvConf = "/etc/resolv.conf"

// resolverQueryTimeout bounds one relayed query.
const resolverQueryTimeout = 5 * time.Second

const (
	handoffResolverUDP = "resolver_udp"
	handoffResolverTCP = "resolver_tcp"
)

// hostNameservers reads the host's resolvers in the order resolv.conf lists them.
func hostNameservers(conf string) []netip.AddrPort {
	var out []netip.AddrPort
	for _, line := range strings.Split(conf, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		host, _, _ := strings.Cut(fields[1], "%")
		if addr, err := netip.ParseAddr(host); err == nil {
			out = append(out, netip.AddrPortFrom(addr, 53))
		}
	}
	return out
}

// HostResolvers lists the resolvers resolv.conf names, for a direct-IP plan.
func HostResolvers(conf string) []string {
	var out []string
	for _, ap := range hostNameservers(conf) {
		out = append(out, ap.String())
	}
	return out
}

// sandboxResolvConf keeps the host's search and options lines and names only
// the stub; empty when the host already names exactly the stub.
func sandboxResolvConf(conf string) string {
	servers := hostNameservers(conf)
	if len(servers) == 1 && servers[0] == sandboxResolver {
		return ""
	}
	var b strings.Builder
	b.WriteString("nameserver " + sandboxResolver.Addr().String() + "\n")
	for _, line := range strings.Split(conf, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && (fields[0] == "search" || fields[0] == "domain" || fields[0] == "options") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
