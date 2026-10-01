package mcp

import "testing"

func TestParseHTTPURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in        string
		canonical string
		loopback  bool
		scheme    string
		err       bool
	}{
		{in: "127.0.0.1:8765", canonical: "http://127.0.0.1:8765", loopback: true, scheme: "http"},
		{in: "127.0.0.1:8765/mcp", canonical: "http://127.0.0.1:8765/mcp", loopback: true, scheme: "http"},
		{in: "localhost:8765", canonical: "http://localhost:8765", loopback: true, scheme: "http"},
		{in: "http://127.0.0.1:8765/mcp", canonical: "http://127.0.0.1:8765/mcp", loopback: true, scheme: "http"},
		{in: "https://127.0.0.1:8765/mcp", canonical: "https://127.0.0.1:8765/mcp", loopback: true, scheme: "https"},
		{in: "[::1]:8765", canonical: "http://[::1]:8765", loopback: true, scheme: "http"},
		{in: "::1", canonical: "http://[::1]", loopback: true, scheme: "http"},
		{in: "intel.example/mcp", canonical: "https://intel.example/mcp", loopback: false, scheme: "https"},
		{in: "https://intel.example/mcp", canonical: "https://intel.example/mcp", loopback: false, scheme: "https"},
		{in: "http://intel.example/mcp", canonical: "http://intel.example/mcp", loopback: false, scheme: "http"},
		{in: "172.17.0.2:8080/mcp", canonical: "https://172.17.0.2:8080/mcp", loopback: false, scheme: "https"},
		{in: "http://172.17.0.2:8080/mcp", canonical: "http://172.17.0.2:8080/mcp", loopback: false, scheme: "http"},
		{in: "localhost.evil.com/mcp", canonical: "https://localhost.evil.com/mcp", loopback: false, scheme: "https"},
		{in: "", err: true},
		{in: "ftp://intel.example/mcp", err: true},
		{in: "http://user:pass@127.0.0.1/mcp", err: true},
		{in: "/mcp", err: true},
	}
	for _, tc := range cases {
		got, err := ParseHTTPURL(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("%q: want error, got %+v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got.Canonical != tc.canonical || got.Loopback != tc.loopback || got.Scheme != tc.scheme {
			t.Fatalf("%q: %+v want canonical=%q loopback=%v scheme=%q", tc.in, got, tc.canonical, tc.loopback, tc.scheme)
		}
	}
}

func TestClassifyHTTPURL(t *testing.T) {
	t.Parallel()
	canonical, loopback, reject := ClassifyHTTPURL("127.0.0.1:8765")
	if reject != "" || !loopback || canonical != "http://127.0.0.1:8765" {
		t.Fatalf("loopback host:port = %q loopback=%v reject=%q", canonical, loopback, reject)
	}
	canonical, loopback, reject = ClassifyHTTPURL("https://intel.example/mcp")
	if reject != "" || loopback || canonical != "https://intel.example/mcp" {
		t.Fatalf("remote https = %q loopback=%v reject=%q", canonical, loopback, reject)
	}
	if _, _, reject = ClassifyHTTPURL("http://intel.example/mcp"); reject != RejectRemoteRequiresHTTPS {
		t.Fatalf("remote http reject = %q", reject)
	}
	if _, _, reject = ClassifyHTTPURL("ftp://intel.example/mcp"); reject != RejectInvalidURL {
		t.Fatalf("bad scheme reject = %q", reject)
	}
}

func TestIsLoopbackURLSchemeless(t *testing.T) {
	t.Parallel()
	if !isLoopbackURL("127.0.0.1:8765") {
		t.Fatal("schemeless loopback must count as local")
	}
	if isLoopbackURL("172.17.0.2:8080") {
		t.Fatal("docker bridge is remote")
	}
}
