package egressproxy_test

import (
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

func TestParseHTTPEndpointRejectsInvalid(t *testing.T) {
	for _, in := range []string{"", "host:0", "host:99999", "bad\x00host", "[example.com]", "[::1"} {
		if _, err := egressproxy.ParseHTTPEndpoint(in, egressproxy.TransportHTTPConnect); err == nil {
			t.Fatalf("expected error for %q", in)
		}
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
