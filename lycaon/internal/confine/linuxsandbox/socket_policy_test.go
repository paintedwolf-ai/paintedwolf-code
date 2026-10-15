package linuxsandbox

import (
	"net/netip"
	"testing"
)

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func TestDecideSocketCalls(t *testing.T) {
	t.Parallel()
	deny := Network{Mode: NetworkDeny}
	loopback := Network{Mode: NetworkProxyOnly, ProxyAddr: "127.0.0.1:9", LoopbackConnect: true, LoopbackConnectPorts: []uint16{5432}}
	anyLoopback := Network{Mode: NetworkDeny, LoopbackConnect: true}
	listen := Network{Mode: NetworkDeny, Listen: true, ListenPorts: []uint16{3000}}
	anyListen := Network{Mode: NetworkDeny, Listen: true}
	direct := Network{Mode: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "tcp", Port: 443}}}
	directUDP := Network{Mode: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "tcp", Port: 443}, {Protocol: "udp", Port: 53}}}
	anyDirect := Network{Mode: NetworkDirectIP}
	grant := Network{Mode: NetworkDeny, SocketPaths: []string{"/run/user/1/agent.sock"}}
	browser := Network{Mode: NetworkDeny, Browser: true, LoopbackConnect: true}

	cases := []struct {
		name    string
		net     Network
		req     SocketRequest
		action  Action
		refusal string
	}{
		{"unix grant", grant, SocketRequest{Op: OpConnect, Unix: "/run/user/1/agent.sock", Stream: true}, Perform, ""},
		{"unix grant spelled with dots", grant, SocketRequest{Op: OpConnect, Unix: "/run/user/1/../1/agent.sock", Stream: true}, Perform, ""},
		{"unix other path", grant, SocketRequest{Op: OpConnect, Unix: "/run/dbus/system_bus_socket", Stream: true}, Refuse, "network-outbound /run/dbus/system_bus_socket"},
		{"unix without grants", deny, SocketRequest{Op: OpConnect, Unix: "/var/run/docker.sock", Stream: true}, Refuse, "network-outbound /var/run/docker.sock"},
		{"unix bind path", grant, SocketRequest{Op: OpBind, Unix: "/tmp/x.sock", Stream: true}, Refuse, "network-bind /tmp/x.sock"},
		{"unix abstract", deny, SocketRequest{Op: OpConnect, Abstract: true, Stream: true}, Perform, ""},
		{"unspecified", deny, SocketRequest{Op: OpConnect, Stream: false}, Perform, ""},
		{"deny external", deny, SocketRequest{Op: OpConnect, IP: ap("1.1.1.1:80"), Stream: true}, Perform, "network-outbound remote:1.1.1.1:80"},
		{"deny loopback no listener", deny, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:5432"), Stream: true}, Perform, "network-outbound remote:127.0.0.1:5432"},
		{"sandbox listener", deny, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:8080"), Stream: true, SandboxListener: true}, Perform, ""},
		{"loopback granted port", loopback, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:5432"), Stream: true}, HostSocket, ""},
		{"loopback v6 granted port", loopback, SocketRequest{Op: OpConnect, IP: ap("[::1]:5432"), Stream: true}, HostSocket, ""},
		{"loopback mapped granted port", loopback, SocketRequest{Op: OpConnect, IP: ap("[::ffff:127.0.0.1]:5432"), Stream: true}, HostSocket, ""},
		{"loopback other port", loopback, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:6379"), Stream: true}, Perform, "network-outbound remote:127.0.0.1:6379"},
		{"loopback any port", anyLoopback, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:6379"), Stream: true}, HostSocket, ""},
		{"loopback datagram relays", anyLoopback, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:8125"), Stream: false}, Perform, ""},
		{"loopback datagram other port", loopback, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:8125"), Stream: false}, Perform, "network-outbound remote:127.0.0.1:8125"},
		{"datagram to a sandbox socket", deny, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:8125"), SandboxListener: true}, Perform, ""},
		{"direct udp permitted", directUDP, SocketRequest{Op: OpConnect, IP: ap("9.9.9.9:53")}, Perform, ""},
		{"direct udp other port", directUDP, SocketRequest{Op: OpConnect, IP: ap("9.9.9.9:123")}, Refuse, "network-outbound remote:9.9.9.9:123"},
		{"direct stream refused beside the gateway", directUDP, SocketRequest{Op: OpConnect, IP: ap("9.9.9.9:80"), Stream: true}, Refuse, "network-outbound remote:9.9.9.9:80"},
		{"direct datagram without a udp permit", direct, SocketRequest{Op: OpConnect, IP: ap("9.9.9.9:53")}, Perform, "network-outbound remote:9.9.9.9:53"},
		{"direct any datagram", anyDirect, SocketRequest{Op: OpConnect, IP: ap("[2001:db8::1]:443")}, Perform, ""},
		{"proxy endpoint is a sandbox listener", loopback, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:9"), Stream: true, SandboxListener: true}, Perform, ""},
		{"direct permitted", direct, SocketRequest{Op: OpConnect, IP: ap("93.184.216.34:443"), Stream: true}, HostSocket, ""},
		{"direct other port", direct, SocketRequest{Op: OpConnect, IP: ap("93.184.216.34:80"), Stream: true}, Perform, "network-outbound remote:93.184.216.34:80"},
		{"direct any", anyDirect, SocketRequest{Op: OpConnect, IP: ap("[2001:db8::1]:22"), Stream: true}, HostSocket, ""},
		{"direct loopback", direct, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:1234"), Stream: true}, HostSocket, ""},
		{"host socket elsewhere", direct, SocketRequest{Op: OpConnect, IP: ap("10.0.0.1:22"), Stream: true, HostSocket: true}, Refuse, "network-outbound remote:10.0.0.1:22"},
		{"host socket to its sandbox listener", anyLoopback, SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:80"), Stream: true, HostSocket: true, SandboxListener: true}, HostSocket, ""},
		{"bind ungranted stays inside", deny, SocketRequest{Op: OpBind, IP: ap("127.0.0.1:3000"), Stream: true}, Perform, ""},
		{"bind granted port", listen, SocketRequest{Op: OpBind, IP: ap("127.0.0.1:3000"), Stream: true}, HostSocket, ""},
		{"bind other port", listen, SocketRequest{Op: OpBind, IP: ap("127.0.0.1:3001"), Stream: true}, Perform, ""},
		{"bind ephemeral with port list", listen, SocketRequest{Op: OpBind, IP: ap("127.0.0.1:0"), Stream: true}, Perform, ""},
		{"bind ephemeral any port", anyListen, SocketRequest{Op: OpBind, IP: ap("0.0.0.0:0"), Stream: true}, HostSocket, ""},
		{"bind granted datagram", anyListen, SocketRequest{Op: OpBind, IP: ap("127.0.0.1:5353"), Stream: false}, Perform, ""},
		{"bind ephemeral datagram", anyListen, SocketRequest{Op: OpBind, IP: ap("0.0.0.0:0"), Stream: false}, Perform, ""},
		{"bind datagram other port", listen, SocketRequest{Op: OpBind, IP: ap("127.0.0.1:5353"), Stream: false}, Perform, ""},
		{"bind browser", browser, SocketRequest{Op: OpBind, IP: ap("[::]:9222"), Stream: true}, HostSocket, ""},
		{"bind on host socket", anyListen, SocketRequest{Op: OpBind, IP: ap("127.0.0.1:4000"), Stream: true, HostSocket: true}, Refuse, "network-bind local:127.0.0.1:4000"},
		{"listen granted host listener", anyListen, SocketRequest{Op: OpListen, HostSocket: true, HostListener: true}, Perform, ""},
		{"listen on connected host socket", anyDirect, SocketRequest{Op: OpListen, HostSocket: true}, Refuse, "network-bind"},
		{"listen inside", deny, SocketRequest{Op: OpListen}, Perform, ""},
	}
	for _, tc := range cases {
		d := tc.net.Decide(tc.req)
		if d.Action != tc.action {
			t.Errorf("%s: action %v, want %v", tc.name, d.Action, tc.action)
		}
		got := ""
		if d.Refusal != nil {
			got = d.Refusal.Operation
			if d.Refusal.Target != "" {
				got += " " + d.Refusal.Target
			}
		}
		if got != tc.refusal {
			t.Errorf("%s: refusal %q, want %q", tc.name, got, tc.refusal)
		}
	}
}

func TestGrantedWildcardListenerNarrowsToLoopback(t *testing.T) {
	t.Parallel()
	n := Network{Mode: NetworkDeny, Listen: true}
	for in, want := range map[string]string{"0.0.0.0:3000": "127.0.0.1:3000", "[::]:3000": "[::1]:3000", "[::1]:3000": "[::1]:3000"} {
		d := n.Decide(SocketRequest{Op: OpBind, IP: ap(in), Stream: true})
		if d.Action != HostSocket || d.Bind != ap(want) {
			t.Errorf("%s: %+v, want host socket bound to %s", in, d, want)
		}
	}
}

// Datagrams reach the host's loopback services through a relay, and a granted
// datagram listener is relayed from the host's loopback, narrowed like a
// stream listener.
func TestDatagramDecisionsNameTheirRelays(t *testing.T) {
	t.Parallel()
	anyLoopback := Network{Mode: NetworkDeny, LoopbackConnect: true}
	if d := anyLoopback.Decide(SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:8125")}); !d.Relay {
		t.Fatalf("granted loopback datagram: %+v, want a relay", d)
	}
	if d := anyLoopback.Decide(SocketRequest{Op: OpConnect, IP: ap("127.0.0.1:8125"), Stream: true}); d.Relay {
		t.Fatalf("stream connect asked for a datagram relay: %+v", d)
	}
	listen := Network{Mode: NetworkDeny, Listen: true, ListenPorts: []uint16{5353}}
	for in, want := range map[string]string{"0.0.0.0:5353": "127.0.0.1:5353", "[::]:5353": "[::1]:5353", "127.0.0.1:5353": "127.0.0.1:5353"} {
		if d := listen.Decide(SocketRequest{Op: OpBind, IP: ap(in)}); d.Action != Perform || d.Expose != ap(want) {
			t.Errorf("%s: %+v, want the sandbox bind exposed at %s", in, d, want)
		}
	}
	for _, in := range []string{"127.0.0.1:5354", "0.0.0.0:0", "192.0.2.1:5353"} {
		if d := listen.Decide(SocketRequest{Op: OpBind, IP: ap(in)}); d.Expose.IsValid() {
			t.Errorf("%s: exposed at %s", in, d.Expose)
		}
	}
}

func TestDatagramGatewayFollowsUDPReach(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		net     Network
		gateway bool
	}{
		{Network{Mode: NetworkDirectIP}, true},
		{Network{Mode: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "udp", Port: 53}}}, true},
		{Network{Mode: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "tcp", Port: 443}}}, false},
		{Network{Mode: NetworkProxyOnly, ProxyAddr: "127.0.0.1:1"}, false},
		{Network{Mode: NetworkDeny, LoopbackConnect: true}, false},
	} {
		if got := tc.net.DatagramGateway(); got != tc.gateway {
			t.Errorf("%+v: gateway %v, want %v", tc.net, got, tc.gateway)
		}
	}
	if (Network{Mode: NetworkDeny}).LoopbackDatagrams() || !(Network{Mode: NetworkDeny, LoopbackConnect: true}).LoopbackDatagrams() || !(Network{Mode: NetworkDirectIP}).LoopbackDatagrams() {
		t.Fatal("loopback datagram reach must follow loopback connect and direct IP")
	}
}
