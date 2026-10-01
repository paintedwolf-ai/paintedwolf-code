package confine_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A local-listen grant emits listener rules without touching egress mode.
func TestListenGrantProfileRules(t *testing.T) {
	t.Parallel()
	// Unnarrowed grant under proxy-only: full local listener scope.
	p, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkProxyOnly,
		ProxyAddr: "127.0.0.1:8472", SocksProxyAddr: "127.0.0.1:8473",
		LocalListen: true,
	})
	testutil.FailErr(t, "BuildProfile proxy-only+listen", err)
	for _, want := range []string{
		`(allow network-bind (local ip "localhost:*"))`,
		`(allow network-inbound (local ip "localhost:*"))`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("granted profile missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, `(allow network-outbound (remote ip "*:*"))`) {
		t.Fatalf("listen grant must not widen egress:\n%s", p)
	}

	// Port-narrowed grant: exact ports only, no wildcard listener scope.
	pn, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkProxyOnly,
		ProxyAddr: "127.0.0.1:8472", SocksProxyAddr: "127.0.0.1:8473",
		LocalListen: true, LocalListenPorts: []uint16{8000, 9000},
	})
	testutil.FailErr(t, "BuildProfile narrowed listen", err)
	for _, want := range []string{
		`(allow network-bind (local ip "localhost:8000"))`,
		`(allow network-inbound (local ip "localhost:8000"))`,
		`(allow network-bind (local ip "localhost:9000"))`,
	} {
		if !strings.Contains(pn, want) {
			t.Fatalf("narrowed profile missing %q:\n%s", want, pn)
		}
	}
	if strings.Contains(pn, `"localhost:*"`) {
		t.Fatalf("narrowed grant must not emit wildcard listener scope:\n%s", pn)
	}

	// Without the grant, mediated profiles stay listener-free.
	pu, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkProxyOnly,
		ProxyAddr: "127.0.0.1:8472", SocksProxyAddr: "127.0.0.1:8473",
	})
	testutil.FailErr(t, "BuildProfile ungranted", err)
	if strings.Contains(pu, "(allow network-bind") {
		t.Fatalf("ungranted proxy-only profile carries listener authority:\n%s", pu)
	}
}

// A granted profile lets a confined command actually bind (syscall probe).
func TestSeatbeltListenGrantAllowsBind(t *testing.T) {
	self := requireSeatbelt(t)
	python := requirePython(t)
	c := confine.Confinement{
		Roots: []string{t.TempDir()}, Network: confine.NetworkDeny,
		LocalListen: true,
	}
	const bindLoopback = `
import socket, sys
s = socket.socket()
try:
    s.bind(('127.0.0.1', 0))
    s.listen(1)
    print('BOUND', s.getsockname()[1])
except OSError as e:
    print('DENIED', e); sys.exit(1)
`
	code, out := confinedRun(t, self, c, python, "-c", bindLoopback)
	if code != 0 || !strings.Contains(out, "BOUND") {
		t.Fatalf("granted profile must bind a loopback listener: exit=%d out=%q", code, out)
	}
}

// The boundary fact reports listener authority for granted profiles.
func TestBoundaryLocalListenFact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		c    confine.Confinement
		want bool
	}{
		{"proxy-ungranted", confine.Confinement{Network: confine.NetworkProxyOnly}, false},
		{"proxy-granted", confine.Confinement{Network: confine.NetworkProxyOnly, LocalListen: true}, true},
		{"direct-ip", confine.Confinement{Network: confine.NetworkDirectIP}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := confine.BoundaryOf(&tc.c).LocalListen; got != tc.want {
				t.Fatalf("LocalListen=%v want %v", got, tc.want)
			}
		})
	}
}
