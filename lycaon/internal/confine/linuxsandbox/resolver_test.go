package linuxsandbox

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestSandboxResolvConfNamesOnlyTheStub(t *testing.T) {
	t.Parallel()
	stub := "nameserver 127.0.0.53\noptions edns0 trust-ad\nsearch example.test\n"
	if got := sandboxResolvConf(stub); got != "" {
		t.Fatalf("host already naming the stub needs no overlay, got %q", got)
	}
	host := "# generated\nnameserver 192.0.2.1\nnameserver fe80::1%eth0\nsearch example.test\noptions ndots:2\n"
	want := "nameserver 127.0.0.53\nsearch example.test\noptions ndots:2\n"
	if got := sandboxResolvConf(host); got != want {
		t.Fatalf("overlay = %q, want %q", got, want)
	}
	servers := hostNameservers(host)
	wantServers := []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:53"), netip.MustParseAddrPort("[fe80::1]:53")}
	if !reflect.DeepEqual(servers, wantServers) {
		t.Fatalf("nameservers = %v, want %v", servers, wantServers)
	}
	if got := hostNameservers("nameserver not-an-address\n"); len(got) != 0 {
		t.Fatalf("malformed nameserver parsed: %v", got)
	}
}
