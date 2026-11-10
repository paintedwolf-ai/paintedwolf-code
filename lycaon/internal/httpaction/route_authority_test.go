package httpaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
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
				if host == "unused.example.com" || host == routeHost && denied {
					return confine.EgressRuleResult{Effect: confine.EgressRuleDeny, Pattern: host}
				}
				return confine.EgressRuleResult{}
			}))
			confine.SetEgressPosture(confine.PostureObserve)
			t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })
			tc := tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: t.Name(), ToolCallID: "denied-route"},
				Local:    tools.InvocationLocal{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{endpoint.Port()}},
			}
			args := map[string]any{"url": "http://nominal.example.com/route?value=fixture"}
			// A rule on an inapplicable mapping must not poison the selected route.
			opts := ExchangeOptions{Dial: []outboundhttp.ResolveMapping{
				{Host: "unrelated.example.com", Port: 80, TargetHost: "unused.example.com", TargetPort: endpoint.Port()},
				dial,
			}}
			_, err := Perform(t.Context(), Deps{Boundary: testBoundary()}, args, tc, opts)
			var refusal *egressgate.HostDeniedError
			if !errors.As(err, &refusal) || refusal.Host != routeHost || connections.Load() != 0 || requests.Load() != 0 || lookups.Load() != 0 {
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

// Under Ask posture, nominal host coverage cannot silently authorize a changed
// explicit route or port override, while ordinary DNS answers for an approved
// route host do not become per-address approval prompts.
func TestExplicitRouteAskPostureRequiresSelectedRouteAndPreservesOrdinaryDNS(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
	var connections, requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if r.Method != http.MethodPost || r.Host != "nominal.example.com" || r.URL.RequestURI() != "/v1/items?mode=mapped" || string(body) != `{"item":"alpha"}` {
			t.Errorf("unexpected wire request: method=%s host=%q uri=%q body=%q", r.Method, r.Host, r.URL.RequestURI(), string(body))
		}
		_, _ = io.WriteString(w, "post-route-ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	endpoint := server.Listener.Addr().(*net.TCPAddr).AddrPort()
	routeAddr := endpoint.Addr().String()
	routePort := endpoint.Port()

	var lookups atomic.Int32
	egress.TestingResolve(t, func(_ context.Context, _, host string) ([]netip.Addr, error) {
		lookups.Add(1)
		switch host {
		case "route.example.com", "nominal.example.com":
			return []netip.Addr{endpoint.Addr()}, nil
		default:
			return nil, fmt.Errorf("unexpected DNS lookup for %q", host)
		}
	})

	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		confine.SetEgressResolver(nil)
	})

	for _, tcCase := range []struct {
		name            string
		dial            outboundhttp.ResolveMapping
		deniedHost      string
		wantAllowedHost string
		wantEndpoints   []string
		wantLookups     int32
	}{
		{
			name:            "hostname-route-does-not-prompt-for-dns-ip",
			dial:            outboundhttp.ResolveMapping{Host: "nominal.example.com", Port: 80, TargetHost: "route.example.com", TargetPort: routePort},
			deniedHost:      "route.example.com",
			wantAllowedHost: "route.example.com",
			wantEndpoints: []string{
				"nominal.example.com:80",
				fmt.Sprintf("route.example.com:%d", routePort),
			},
			wantLookups: 1,
		},
		{
			name:            "same-host-port-override",
			dial:            outboundhttp.ResolveMapping{Host: "nominal.example.com", Port: 80, TargetPort: routePort},
			deniedHost:      "nominal.example.com",
			wantAllowedHost: "nominal.example.com",
			wantEndpoints: []string{
				"nominal.example.com:80",
				fmt.Sprintf("nominal.example.com:%d", routePort),
			},
			wantLookups: 1,
		},
		{
			name:            "chained-connect-to-and-resolve-address",
			dial:            outboundhttp.ResolveMapping{Host: "nominal.example.com", Port: 80, TargetHost: "route.example.com", Address: endpoint.Addr(), TargetPort: routePort},
			deniedHost:      routeAddr,
			wantAllowedHost: routeAddr,
			wantEndpoints: []string{
				"nominal.example.com:80",
				fmt.Sprintf("route.example.com:%d", routePort),
				net.JoinHostPort(routeAddr, fmt.Sprint(routePort)),
			},
			wantLookups: 0,
		},
	} {
		t.Run(tcCase.name, func(t *testing.T) {
			connections.Store(0)
			requests.Store(0)
			lookups.Store(0)

			var mu sync.Mutex
			var observed []string
			allowRoute := false
			confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
				mu.Lock()
				observed = append(observed, ep.DialAddr())
				mu.Unlock()
				if ep.Host == "nominal.example.com" && ep.Port == 80 {
					return true
				}
				if !allowRoute && ep.Host == tcCase.deniedHost && ep.Port == routePort {
					return false
				}
				return true
			})

			tc := tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: t.Name(), ToolCallID: "ask-denied"},
				Local:    tools.InvocationLocal{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{routePort}},
			}
			args := map[string]any{
				"method":    "POST",
				"url":       "http://nominal.example.com/v1/items?mode=mapped",
				"body_text": `{"item":"alpha"}`,
			}
			opts := ExchangeOptions{Dial: []outboundhttp.ResolveMapping{
				{Host: "unrelated.example.com", Port: 80, TargetHost: "unused.example.com", TargetPort: routePort},
				tcCase.dial,
			}}

			_, err := Perform(t.Context(), Deps{Boundary: testBoundary()}, args, tc, opts)
			var refusal *egressgate.HostDeniedError
			if !errors.As(err, &refusal) || refusal.Host != tcCase.wantAllowedHost || connections.Load() != 0 || requests.Load() != 0 || lookups.Load() != 0 {
				t.Fatalf("denied route under Ask posture: err=%v connections=%d requests=%d DNS=%d, want HostDeniedError(%q) with zero effects",
					err, connections.Load(), requests.Load(), lookups.Load(), tcCase.wantAllowedHost)
			}

			mu.Lock()
			observed = nil
			mu.Unlock()
			allowRoute = true
			tc.Identity.ToolCallID = "ask-approved"

			got, err := Perform(t.Context(), Deps{Boundary: testBoundary()}, args, tc, opts)
			testutil.FailErr(t, "approved route under Ask posture", err)
			if string(got.Response.Body) != "post-route-ok" || got.SentBodyBytes != int64(len(`{"item":"alpha"}`)) {
				t.Fatalf("unexpected exchange response=%q sentBytes=%d", string(got.Response.Body), got.SentBodyBytes)
			}
			if connections.Load() != 1 || requests.Load() != 1 || lookups.Load() != tcCase.wantLookups {
				t.Fatalf("approved route effects: connections=%d requests=%d DNS=%d, want 1/1/%d",
					connections.Load(), requests.Load(), lookups.Load(), tcCase.wantLookups)
			}
			mu.Lock()
			gotEndpoints := slices.Clone(observed)
			mu.Unlock()
			if !slices.Equal(gotEndpoints, tcCase.wantEndpoints) {
				t.Fatalf("egress resolver endpoints = %v, want %v (ordinary DNS must not add per-IP prompts)", gotEndpoints, tcCase.wantEndpoints)
			}
		})
	}
}
