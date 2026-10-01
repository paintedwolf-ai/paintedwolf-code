package confine_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoopbackConnectProfileIsIndependentAndPortNarrowed(t *testing.T) {
	t.Parallel()
	profile, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkProxyOnly,
		ProxyAddr: "127.0.0.1:8472", SocksProxyAddr: "127.0.0.1:8473",
		LoopbackConnect: true, LoopbackConnectPorts: []uint16{8123},
	})
	testutil.FailErr(t, "build loopback profile", err)
	if !strings.Contains(profile, `(allow network-outbound (remote ip "localhost:8123"))`) {
		t.Fatalf("profile missing local destination rule:\n%s", profile)
	}
	for _, forbidden := range []string{
		`(allow network-bind`, `(remote ip "localhost:8124")`, `(remote ip "*:*"))`,
	} {
		if strings.Contains(profile, forbidden) {
			t.Fatalf("loopback grant widened unrelated authority %q:\n%s", forbidden, profile)
		}
	}
	boundary := confine.BoundaryOf(&confine.Confinement{Network: confine.NetworkProxyOnly, LoopbackConnect: true})
	if !boundary.LoopbackConnect || boundary.LocalListen {
		t.Fatalf("boundary axes collapsed: %+v", boundary)
	}
}

func TestBrowserProfileFollowsTheRequestedLoopbackPorts(t *testing.T) {
	t.Parallel()
	narrowed := confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkDeny,
		Browser: true, LoopbackConnect: true, LoopbackConnectPorts: []uint16{8123},
	}
	profile, err := confine.BuildProfile(narrowed)
	testutil.FailErr(t, "build narrowed browser profile", err)
	if !strings.Contains(profile, `(allow network-outbound (remote ip "localhost:8123"))`) {
		t.Fatalf("narrowed browser profile missing its reviewed port:\n%s", profile)
	}
	if strings.Contains(profile, `(allow network-outbound (remote ip "localhost:*"))`) {
		t.Fatalf("narrowed browser profile still opened every local port:\n%s", profile)
	}

	// Empty port scope permits all local ports; page tools authorize each target.
	floorC := confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkDeny,
		Browser: true, LoopbackConnect: true,
	}
	floor, err := confine.BuildProfile(floorC)
	testutil.FailErr(t, "build browser floor profile", err)
	if !strings.Contains(floor, `(allow network-outbound (remote ip "localhost:*"))`) {
		t.Fatalf("browser floor lost local reach:\n%s", floor)
	}
	if strings.Contains(floor, `(allow network-outbound (remote ip "*:*"))`) {
		t.Fatalf("browser profile must never carry public egress:\n%s", floor)
	}
	b := confine.BoundaryOf(&floorC)
	if !b.LoopbackConnect || !b.LocalListen {
		t.Fatalf("browser boundary = %+v", b)
	}
}

func TestBrowserConfinementKeepsANarrowedRequest(t *testing.T) {
	t.Parallel()
	c, ok := confine.BrowserConfinement(confine.Request{
		Roots:                []string{t.TempDir()},
		LoopbackConnect:      true,
		LoopbackConnectPorts: []uint16{3000},
	})
	if !ok || c == nil {
		t.Skip("confinement unavailable on this platform")
	}
	if !c.LoopbackConnect || len(c.LoopbackConnectPorts) != 1 || c.LoopbackConnectPorts[0] != 3000 {
		t.Fatalf("browser confinement dropped the reviewed ports: %+v", c.LoopbackConnectPorts)
	}
}

func TestSeatbeltComposedLocalNetworkGrantRunsServerAndClient(t *testing.T) {
	self := requireSeatbelt(t)
	python := requirePython(t)
	c := confine.Confinement{
		Roots: []string{t.TempDir()}, Network: confine.NetworkDeny,
		LocalListen: true, LoopbackConnect: true,
	}
	const probe = `
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(('127.0.0.1', 0))
port = s.getsockname()[1]
c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
c.sendto(b'ping', ('127.0.0.1', port))
data, _ = s.recvfrom(16)
print(data.decode())
`
	code, out := confinedRun(t, self, c, python, "-c", probe)
	if code != 0 || !strings.Contains(out, "ping") {
		t.Fatalf("composed local grant failed: exit=%d out=%q", code, out)
	}
}
