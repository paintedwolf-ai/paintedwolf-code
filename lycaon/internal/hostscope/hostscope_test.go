package hostscope

import "testing"

// One registrable site maps to one lease subject.
func TestSubdomainsOfOneSiteShareALease(t *testing.T) {
	t.Parallel()
	want := "*.github.com"
	for _, host := range []string{
		"github.com", "docs.github.com", "api.github.com", "raw.githubusercontent.com.github.com",
		"GitHub.com", "  docs.github.com  ",
	} {
		if got := Pattern(host); got != want {
			t.Errorf("Pattern(%q) = %q, want %q", host, got, want)
		}
	}
}

// Tenant hosts remain separate beneath a public suffix.
func TestPublicSuffixBoundaryIsNotCrossed(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"google.github.io":     "*.google.github.io",
		"someone.github.io":    "*.someone.github.io",
		"app.s3.amazonaws.com": "*.app.s3.amazonaws.com",
		"foo.co.uk":            "*.foo.co.uk",
		"bar.foo.co.uk":        "*.foo.co.uk",
	}
	for host, want := range cases {
		if got := Pattern(host); got != want {
			t.Errorf("Pattern(%q) = %q, want %q", host, got, want)
		}
	}
	// Two sites under one public suffix must not share a lease subject.
	if Pattern("google.github.io") == Pattern("someone.github.io") {
		t.Fatal("two sites under a public suffix share a lease subject")
	}
}

// An address is not a domain and has no registrable parent to widen into.
func TestAddressesAndSingleLabelsDoNotWiden(t *testing.T) {
	t.Parallel()
	for _, host := range []string{
		"127.0.0.1", "10.0.0.5", "::1", "[::1]", "fe80::1",
		"localhost", "db", "",
	} {
		got := Pattern(host)
		if len(got) > 2 && got[:2] == "*." {
			t.Errorf("Pattern(%q) = %q — widened something with no registrable parent", host, got)
		}
	}
}

// A public suffix has no registrable parent to widen into.
func TestBarePublicSuffixDoesNotWiden(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"co.uk", "github.io", "com"} {
		if got := Pattern(host); got != host {
			t.Errorf("Pattern(%q) = %q, want the host itself", host, got)
		}
	}
}

// Card copy and grant reach come from one pattern.
func TestCopyStatesTheBreadthItGrants(t *testing.T) {
	t.Parallel()
	if got := Coverage(Pattern("docs.github.com")); got != "connections to `github.com` and its subdomains" {
		t.Errorf("Coverage = %q", got)
	}
	if got := AllowLine("docs.github.com"); got != "connections to github.com and its subdomains" {
		t.Errorf("AllowLine = %q", got)
	}
	if got := Coverage(Pattern("127.0.0.1")); got != "connections to `127.0.0.1`" {
		t.Errorf("Coverage(ip) = %q", got)
	}
	if got := AllowLine("127.0.0.1"); got != "connections to 127.0.0.1" {
		t.Errorf("AllowLine(ip) = %q", got)
	}
}

func TestTunnelPatternsPreserveHostAndPort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host, pattern, site string
	}{
		{"api.example.com", "*.example.com:443", "*.example.com"},
		{"127.0.0.1", "127.0.0.1:443", "127.0.0.1"},
		{"2001:db8::1", "[2001:db8::1]:443", "2001:db8::1"},
		{"[::1]", "[::1]:443", "::1"},
	} {
		pattern := TunnelPattern(tc.host, 443)
		if pattern != tc.pattern {
			t.Errorf("TunnelPattern(%q) = %q, want %q", tc.host, pattern, tc.pattern)
		}
		site, port := SplitTunnelPattern(pattern)
		if site != tc.site || port != 443 {
			t.Errorf("SplitTunnelPattern(%q) = (%q, %d)", pattern, site, port)
		}
	}
}

func TestBareIPv6DoesNotAcquireAPort(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"::1", "2001:db8::443", "fe80::80"} {
		pattern := TunnelPattern(host, 0)
		site, port := SplitTunnelPattern(pattern)
		if site != host || port != 0 {
			t.Errorf("SplitTunnelPattern(%q) = (%q, %d)", pattern, site, port)
		}
		if got, want := Coverage(pattern), "connections to `"+host+"`"; got != want {
			t.Errorf("Coverage(%q) = %q, want %q", pattern, got, want)
		}
	}
}
