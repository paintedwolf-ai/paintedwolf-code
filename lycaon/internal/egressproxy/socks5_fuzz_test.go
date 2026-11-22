package egressproxy

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
)

func FuzzParseSocksAddress(f *testing.F) {
	f.Add(byte(0x03), []byte("example.com"))
	f.Add(byte(0x01), []byte{1, 2, 3, 4})
	f.Add(byte(0x04), bytes.Repeat([]byte{0}, 16))
	f.Fuzz(func(t *testing.T, atyp byte, addr []byte) {
		_, _ = parseSocksAddress(atyp, addr)
	})
}

// FuzzParseHTTPEndpoint requires an accepted endpoint to be a dialable
// identity: a nonzero port, a host that is an IP or a lowercase name without
// brackets, controls or spaces, brackets only around an IPv6 literal, and
// parsing its dial address gives the same endpoint again.
func FuzzParseHTTPEndpoint(f *testing.F) {
	for _, seed := range []string{"example.com:443", "[::1]", "Example.COM.", "[::ffff:192.0.2.10]:443", "", "host:0", "host:99999", "bad\x00host", "[example.com]", "[::1", "a b:80", "xn--nxasmq6b"} {
		f.Add(seed, string(TransportHTTPConnect))
		f.Add(seed, string(TransportHTTPRequest))
	}
	f.Fuzz(func(t *testing.T, hostport, transport string) {
		ep, err := ParseHTTPEndpoint(hostport, Transport(transport))
		if err != nil {
			return
		}
		if ep.Port == 0 || ep.Host == "" || ep.Transport != Transport(transport) {
			t.Fatalf("ParseHTTPEndpoint(%q) = %+v", hostport, ep)
		}
		addr, ipErr := netip.ParseAddr(ep.Host)
		if ipErr == nil && addr.String() != ep.Host || ipErr != nil && (ep.Host != strings.ToLower(ep.Host) || strings.ContainsFunc(ep.Host, func(r rune) bool { return r <= ' ' || r == '[' || r == ']' || r >= 0x7f })) {
			t.Fatalf("ParseHTTPEndpoint(%q) host %q is neither an address nor a lowercase name", hostport, ep.Host)
		}
		if inner, bracketed := strings.CutPrefix(strings.TrimSpace(hostport), "["); bracketed {
			literal, _, _ := strings.Cut(inner, "]")
			if v6, err := netip.ParseAddr(literal); err != nil || !v6.Is6() || v6.Unmap().String() != ep.Host {
				t.Fatalf("ParseHTTPEndpoint(%q) read a bracketed host as %q", hostport, ep.Host)
			}
		}
		again, err := ParseHTTPEndpoint(ep.DialAddr(), ep.Transport)
		if err != nil || again != ep {
			t.Fatalf("ParseHTTPEndpoint(%q) = %+v; its dial address %q reads %+v, %v", hostport, ep, ep.DialAddr(), again, err)
		}
	})
}
