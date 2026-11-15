package linuxsandbox

import (
	"net/netip"
	"strconv"
	"testing"
)

// Every bind the sandbox keeps from this machine is reported, and only those
// a listen grant for the bound port would open: an unreported contained
// listener leaves a running server with no fact saying why nothing reaches it.
func TestBindReportsExactlyTheListenersAGrantWouldOpen(t *testing.T) {
	t.Parallel()
	ports := []uint16{0, 3000, 3001, 5353, 65535}
	networks := []Network{
		{Mode: NetworkDeny},
		{Mode: NetworkProxyOnly, ProxyAddr: "127.0.0.1:9", LoopbackConnect: true},
		{Mode: NetworkDeny, Listen: true, ListenPorts: []uint16{3000}},
		{Mode: NetworkDeny, Listen: true, ListenPorts: []uint16{3001, 5353}},
		{Mode: NetworkDeny, Listen: true},
		{Mode: NetworkDeny, Browser: true},
		{Mode: NetworkDirectIP},
	}
	var addrs []netip.Addr
	for _, s := range []string{"127.0.0.1", "127.0.0.2", "0.0.0.0", "::1", "::", "::ffff:127.0.0.1", "192.0.2.10", "2001:db8::1"} {
		addrs = append(addrs, netip.MustParseAddr(s))
	}
	checked := 0
	for _, n := range networks {
		for _, addr := range addrs {
			for _, port := range ports {
				for _, stream := range []bool{true, false} {
					for _, hostSocket := range []bool{false, true} {
						req := SocketRequest{Op: OpBind, IP: netip.AddrPortFrom(addr, port), Stream: stream, HostSocket: hostSocket}
						checkBindReport(t, n, req)
						checked++
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no bind requests were generated")
	}
}

func checkBindReport(t *testing.T, n Network, req SocketRequest) {
	t.Helper()
	d := n.Decide(req)
	ip := req.IP.Addr().Unmap()
	local := "local:" + netip.AddrPortFrom(ip, req.IP.Port()).String()
	if req.HostSocket {
		// A host socket is already in the host's namespace; rebinding it is refused outright.
		if d.Action != Refuse || d.Refusal == nil || d.Refusal.Target != local {
			t.Errorf("%+v under %+v: %+v, want a refused host-socket bind naming %s", req, n, d, local)
		}
		return
	}
	if d.Action == Refuse {
		t.Errorf("%+v under %+v: refused; a sandbox bind is performed or moved to the host", req, n)
		return
	}
	granted := n
	granted.Listen = true
	granted.ListenPorts = []uint16{req.IP.Port()}
	opens := reachesHost(granted.Decide(req)) && !reachesHost(d)
	reported := d.Refusal != nil
	if reported != (opens && req.IP.Port() != 0) {
		t.Errorf("%+v under %+v: reported=%v, want %v (a grant for port %d opens it: %v)", req, n, reported, opens && req.IP.Port() != 0, req.IP.Port(), opens)
	}
	if !reported {
		return
	}
	if d.Refusal.Operation != "network-bind" || d.Refusal.Target != local {
		t.Errorf("%+v under %+v: refusal %+v, want network-bind %s", req, n, *d.Refusal, local)
	}
	if got := portOf(d.Refusal.Target); got != strconv.Itoa(int(req.IP.Port())) {
		t.Errorf("%+v: refusal target %q names port %q", req, d.Refusal.Target, got)
	}
}

// reachesHost is whether a bind decision gives the listener an address on
// this machine outside the sandbox.
func reachesHost(d Decision) bool {
	return d.Action == HostSocket || d.Expose.IsValid()
}

func portOf(target string) string {
	for i := len(target) - 1; i >= 0; i-- {
		if target[i] == ':' {
			return target[i+1:]
		}
	}
	return ""
}
