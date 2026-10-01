package httpaction

import (
	"net/netip"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Locality and loopback authority follow the address and port the first hop
// dials, not the URL's spelling.
func TestFirstHopFollowsTheDialedAddress(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	public := netip.MustParseAddr("203.0.113.9")
	for name, tt := range map[string]struct {
		url      string
		socket   string
		resolve  []outboundhttp.ResolveMapping
		loopback bool
		port     uint16
	}{
		"localhost":                   {url: "http://localhost:8081/token", loopback: true, port: 8081},
		"loopback literal":            {url: "http://127.0.0.1:8080/", loopback: true, port: 8080},
		"ipv6 loopback":               {url: "http://[::1]/", loopback: true, port: 80},
		"public host":                 {url: "https://auth.example.com/token"},
		"localhost mapped away":       {url: "http://localhost:8081/", resolve: []outboundhttp.ResolveMapping{{Host: "localhost", Port: 8081, Address: public}}},
		"public host mapped loopback": {url: "http://auth.test:8081/", resolve: []outboundhttp.ResolveMapping{{Host: "auth.test", Address: loopback}}, loopback: true, port: 8081},
		"mapped to another port":      {url: "https://auth.test/", resolve: []outboundhttp.ResolveMapping{{Host: "auth.test", Port: 443, Address: loopback, TargetPort: 8443}}, loopback: true, port: 8443},
		"mapping for another port":    {url: "http://auth.test:8081/", resolve: []outboundhttp.ResolveMapping{{Host: "auth.test", Port: 9000, Address: loopback}}},
		"unix socket":                 {url: "http://localhost/v1.45/info", socket: "/var/run/docker.sock"},
	} {
		t.Run(name, func(t *testing.T) {
			target, err := url.Parse(tt.url)
			testutil.FailErr(t, "parse url", err)
			spec := requestSpec{target: target, socket: tt.socket, resolve: tt.resolve}
			if got := spec.dialsLoopback(); got != tt.loopback {
				t.Fatalf("dialsLoopback = %v, want %v", got, tt.loopback)
			}
			reject := checkLoopbackAuthority(spec, func(netip.Addr, uint16) bool { return false })
			if (reject != nil) != tt.loopback {
				t.Fatalf("loopback authority reject = %+v, want one only for loopback", reject)
			}
			if reject != nil && reject.Data["port"] != tt.port {
				t.Fatalf("authority port = %v, want the dialed port %d", reject.Data["port"], tt.port)
			}
		})
	}
}
