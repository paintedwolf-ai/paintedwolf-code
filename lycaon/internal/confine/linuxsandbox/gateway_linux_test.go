//go:build linux

package linuxsandbox

import (
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGatewayCarriesOnlyPermittedUnicast(t *testing.T) {
	t.Parallel()
	n := Network{Mode: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "tcp", Port: 443}, {Protocol: "udp", Port: 53}}}
	for dst, want := range map[string]bool{
		"9.9.9.9:53": true, "[2001:db8::53]:53": true, "9.9.9.9:443": false, "9.9.9.9:123": false,
		"127.0.0.1:53": false, "[::ffff:127.0.0.1]:53": false, "0.0.0.0:53": false,
		"224.0.0.251:53": false, "255.255.255.255:53": false,
	} {
		if got := gatewayCarries(n, ap(dst)); got != want {
			t.Errorf("%s: carried %v, want %v", dst, got, want)
		}
	}
	if !gatewayCarries(Network{Mode: NetworkDirectIP}, ap("9.9.9.9:123")) {
		t.Fatal("direct IP without permits must carry every unicast datagram")
	}
	if gatewayCarries(Network{Mode: NetworkProxyOnly}, ap("9.9.9.9:53")) {
		t.Fatal("only direct IP carries datagrams off the sandbox")
	}
}

func TestIPv6AbsenceIsNotAFailure(t *testing.T) {
	t.Parallel()
	for _, err := range []error{unix.EAFNOSUPPORT, unix.EOPNOTSUPP, unix.EACCES} {
		if !ipv6Absent(err) {
			t.Errorf("%v must leave the gateway to IPv4", err)
		}
	}
	if ipv6Absent(unix.EEXIST) {
		t.Fatal("an unexpected routing error must fail the gateway")
	}
}

func TestGatewayReportsEachDroppedDestinationOnce(t *testing.T) {
	t.Parallel()
	d := droppedDestinations{}
	if !d.first(ap("192.0.2.1:53")) || d.first(ap("192.0.2.1:53")) || !d.first(ap("192.0.2.1:54")) {
		t.Fatal("each destination must be reported on its first drop only")
	}
	for i := 0; len(d) < maxDroppedDestinations; i++ {
		d.first(ap("198.51.100.1:" + strconv.Itoa(1000+i)))
	}
	if !d.first(ap("203.0.113.1:1")) || !d.first(ap("203.0.113.1:1")) {
		t.Fatal("past the bound every drop is reported")
	}
	if unmapped(ap("[::ffff:192.0.2.1]:53")) != ap("192.0.2.1:53") {
		t.Fatal("a mapped destination is reported by its IPv4 address")
	}
}
