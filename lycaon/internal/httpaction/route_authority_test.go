package httpaction

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// Local capability permits the fixture's socket. It cannot override an
// independent host denial on an explicitly selected route.
func TestExplicitRouteDenialPrecedesConnectionAndAllowsRecovery(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
	for _, mapping := range []string{"address", "hostname"} {
		t.Run(mapping, func(t *testing.T) {
			var connections, requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Host != "nominal.example.com" || r.URL.RequestURI() != "/route?value=fixture" {
					t.Errorf("mapped request changed logical recipient or bytes: host=%q uri=%q", r.Host, r.URL.RequestURI())
				}
				_, _ = io.WriteString(w, "exact fixture response")
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			t.Cleanup(server.Close)
			endpoint := server.Listener.Addr().(*net.TCPAddr).AddrPort()
			routeHost := endpoint.Addr().String()
			dial := outboundhttp.ResolveMapping{Host: "nominal.example.com", Port: 80, Address: endpoint.Addr(), TargetPort: endpoint.Port()}
			if mapping == "hostname" {
				routeHost = "route.example.com"
				dial.Address = netip.Addr{}
				dial.TargetHost = routeHost
			}
			var lookups atomic.Int32
			egress.TestingResolve(t, func(_ context.Context, _, host string) ([]netip.Addr, error) {
				lookups.Add(1)
				if host != routeHost {
					return nil, fmt.Errorf("unexpected fixture DNS host %q", host)
				}
				return []netip.Addr{endpoint.Addr()}, nil
			})
			denied := true
			t.Cleanup(confine.SetEgressRuleEvaluator(func(_ context.Context, _ confine.EgressCommand, host string) confine.EgressRuleResult {
				if host == routeHost && denied {
					return confine.EgressRuleResult{Effect: confine.EgressRuleDeny, Pattern: routeHost}
				}
				return confine.EgressRuleResult{}
			}))
			confine.SetEgressPosture(confine.PostureObserve)
			t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })
			tc := tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: t.Name(), ToolCallID: "denied-route"},
				Local: tools.InvocationLocal{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{endpoint.Port()}},
			}
			args := map[string]any{"url": "http://nominal.example.com/route?value=fixture"}
			opts := ExchangeOptions{Dial: []outboundhttp.ResolveMapping{dial}}
			_, err := Perform(t.Context(), Deps{Boundary: testBoundary()}, args, tc, opts)
			if err == nil || connections.Load() != 0 || requests.Load() != 0 || lookups.Load() != 0 {
				t.Errorf("denied explicit route: err=%v connections=%d requests=%d DNS=%d; want refusal before resolution or connection", err, connections.Load(), requests.Load(), lookups.Load())
			}
			before := requests.Load()
			denied = false
			for attempt := range 2 {
				tc.Identity.ToolCallID = fmt.Sprintf("authorized-route-%d", attempt)
				got, err := Perform(t.Context(), Deps{Boundary: testBoundary()}, args, tc, opts)
				testutil.FailErr(t, "send after route denial removed", err)
				if string(got.Response.Body) != "exact fixture response" {
					t.Fatalf("mapped response changed: %q", got.Response.Body)
				}
			}
			if requests.Load()-before != 2 {
				t.Fatalf("authorized recovery delivered %d requests, want two", requests.Load()-before)
			}
		})
	}
}
