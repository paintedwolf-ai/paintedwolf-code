package httpaction

import (
	"net/netip"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
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
		"connect-to localhost":        {url: "https://app.example.test/", resolve: []outboundhttp.ResolveMapping{{Host: "app.example.test", TargetHost: "localhost", TargetPort: 8443}}, loopback: true, port: 8443},
		"connect-to keeps the host":   {url: "http://localhost/", resolve: []outboundhttp.ResolveMapping{{TargetPort: 9090}}, loopback: true, port: 9090},
		"connect-to a named service":  {url: "https://app.example.test/", resolve: []outboundhttp.ResolveMapping{{Host: "app.example.test", TargetHost: "backend.example.test"}}},
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

// A frontend's dial overrides replace the resolve argument rather than
// merging with it, so one list decides where every hop connects.
func TestPerformDialOverridesReplaceResolve(t *testing.T) {
	args := map[string]any{"url": "https://app.example.test/", "resolve": []any{map[string]any{"host": "app.example.test", "ip": "127.0.0.1"}}}
	_, err := Perform(t.Context(), Deps{}, args, tools.ToolContext{}, ExchangeOptions{Dial: []outboundhttp.ResolveMapping{{Host: "app.example.test", TargetPort: 8443}}})
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("Perform accepted both resolve and dial overrides: %v", err)
	}
}
