package egressproxy_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

func TestParseHTTPEndpointDefaults(t *testing.T) {
	ep, err := egressproxy.ParseHTTPEndpoint("Example.COM", egressproxy.TransportHTTPConnect)
	if err != nil || ep.Host != "example.com" || ep.Port != 443 {
		t.Fatalf("connect default: ep=%+v err=%v", ep, err)
	}
	ep, err = egressproxy.ParseHTTPEndpoint("example.com:8080", egressproxy.TransportHTTPRequest)
	if err != nil || ep.Port != 8080 || ep.Transport != egressproxy.TransportHTTPRequest {
		t.Fatalf("request port: ep=%+v err=%v", ep, err)
	}
	ep, err = egressproxy.ParseHTTPEndpoint("[2001:db8::1]:443", egressproxy.TransportHTTPConnect)
	if err != nil || ep.Host != "2001:db8::1" || ep.Port != 443 {
		t.Fatalf("ipv6: ep=%+v err=%v", ep, err)
	}
	ep, err = egressproxy.ParseHTTPEndpoint("[2001:db8::1]", egressproxy.TransportHTTPRequest)
	if err != nil || ep.Host != "2001:db8::1" || ep.Port != 80 {
		t.Fatalf("ipv6 default port: ep=%+v err=%v", ep, err)
	}
	ep, err = egressproxy.ParseHTTPEndpoint("Example.COM.", egressproxy.TransportHTTPConnect)
	if err != nil || ep.Host != "example.com" {
		t.Fatalf("rooted DNS name: ep=%+v err=%v", ep, err)
	}
}

func TestParseSocksEndpoint(t *testing.T) {
	ep, err := egressproxy.ParseSocksEndpoint(0x01, []byte{1, 2, 3, 4}, 5432)
	if err != nil || ep.Port != 5432 || ep.Transport != egressproxy.TransportSocksTCP {
		t.Fatalf("ipv4: ep=%+v err=%v", ep, err)
	}
	if _, err := egressproxy.ParseSocksEndpoint(0x03, []byte("db.example"), 0); err == nil {
		t.Fatal("zero port must fail")
	}
}

// An IPv4-mapped IPv6 literal is the IPv4 host it maps, so rules and
// decisions written for that address apply to it.
func TestIPv4MappedLiteralIsTheIPv4Host(t *testing.T) {
	for _, hostport := range []string{"[::ffff:192.0.2.10]:443", "[::ffff:c000:20a]:443"} {
		ep, err := egressproxy.ParseHTTPEndpoint(hostport, egressproxy.TransportHTTPConnect)
		if err != nil {
			t.Fatalf("%s: %v", hostport, err)
		}
		if ep.Host != "192.0.2.10" {
			t.Fatalf("%s: host %q, want 192.0.2.10", hostport, ep.Host)
		}
	}
}

// One trailing dot roots a name; otherwise a host keeps DNS's label and name
// limits. An address carries no interface zone.
func TestParseHTTPEndpointRefusesHostsDNSCannotName(t *testing.T) {
	for _, in := range []string{"[fe80::1%eth0]:443", "[::% ]", "fe80::1%lo", ".", "..", "..:443", "a..b", ".example.com", "example.com..", strings.Repeat("a", 64) + ".com",
		strings.Repeat("t", 60) + "‣", strings.Repeat("abcdefgh.", 28) + "com"} {
		if ep, err := egressproxy.ParseHTTPEndpoint(in, egressproxy.TransportHTTPConnect); err == nil {
			t.Fatalf("ParseHTTPEndpoint(%q) = %+v, want refusal", in, ep)
		}
	}
}

// Non-ASCII hosts whose UTS #46 mapping introduces RTL runes that violate the
// bidi rule must be refused rather than emitting a punycode host whose dial
// address ParseHTTPEndpoint cannot parse back.
func TestParseHTTPEndpointIDNANormalizationRoundTrip(t *testing.T) {
	refused := []string{
		"⅄ℵ",                 // nightly fuzz corpus 99e51b61e24ee86c (mapped to xn--4db961l)
		"㌽ℵ",                 // nightly fuzz corpus b21acc490c34c18d (mapped to xn--4db140tddand5e)
		"⅄ℵ:443",             // with explicit port
		"㌽ℵ:8080",            // with explicit port
		"aℵ",                 // ASCII L + U+2135 ALEF SYMBOL (-> U+05D0 R)
		"aℶ",                 // ASCII L + U+2136 BET SYMBOL (-> U+05D1 R)
		"aℷ",                 // ASCII L + U+2137 GIMEL SYMBOL (-> U+05D2 R)
		"aℸ",                 // ASCII L + U+2138 DALET SYMBOL (-> U+05D3 R)
		"⅄ℵ.example.com",     // multi-label with offending label
		"xn--4db961l",        // punycode form of ⅄ℵ
		"xn--4db140tddand5e", // punycode form of ㌽ℵ
	}
	for _, transport := range []egressproxy.Transport{egressproxy.TransportHTTPConnect, egressproxy.TransportHTTPRequest} {
		for _, in := range refused {
			if ep, err := egressproxy.ParseHTTPEndpoint(in, transport); err == nil {
				t.Fatalf("ParseHTTPEndpoint(%q, %q) = %+v, want refusal", in, transport, ep)
			}
		}
	}
	for _, domain := range []string{"⅄ℵ", "㌽ℵ", "aℵ"} {
		if ep, err := egressproxy.ParseSocksEndpoint(0x03, []byte(domain), 443); err == nil {
			t.Fatalf("ParseSocksEndpoint(0x03, %q, 443) = %+v, want refusal", domain, ep)
		}
	}

	accepted := []struct {
		in   string
		want string
	}{
		{in: "bücher.example.com", want: "xn--bcher-kva.example.com"},
		{in: "ℵ", want: "xn--4db"},
		{in: "אבג.example.com", want: "xn--4dbcd.example.com"},
	}
	for _, tc := range accepted {
		ep, err := egressproxy.ParseHTTPEndpoint(tc.in, egressproxy.TransportHTTPConnect)
		if err != nil {
			t.Fatalf("ParseHTTPEndpoint(%q): %v", tc.in, err)
		}
		if ep.Host != tc.want {
			t.Fatalf("ParseHTTPEndpoint(%q).Host = %q, want %q", tc.in, ep.Host, tc.want)
		}
		again, err := egressproxy.ParseHTTPEndpoint(ep.DialAddr(), ep.Transport)
		if err != nil || again != ep {
			t.Fatalf("ParseHTTPEndpoint(%q) dial addr %q round-trip = %+v, %v; want %+v", tc.in, ep.DialAddr(), again, err, ep)
		}
	}
}
