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
	"github.com/lycaon/lycaon/internal/toolrejection"
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

func TestPerHopRedirectRouteAdmissionCancellationAndPartialFacts(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
	var originConnections, originRequests, originBytes atomic.Int32
	var targetConnections, targetRequests, targetBytes atomic.Int32
	var originLookups, targetLookups atomic.Int32

	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		body, _ := io.ReadAll(r.Body)
		targetBytes.Add(int32(len(body)))
		if r.Method != http.MethodGet || r.Host != "redirected.example.com" {
			t.Errorf("redirected hop wire request: method=%s host=%q", r.Method, r.Host)
		}
		if r.URL.Path == "/hop1" {
			w.Header().Set("Location", "http://redirected.example.com/landed")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "redirect-target-ok")
	}))
	target.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			targetConnections.Add(1)
		}
	}
	target.Start()
	t.Cleanup(target.Close)
	targetEndpoint := target.Listener.Addr().(*net.TCPAddr).AddrPort()

	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		body, _ := io.ReadAll(r.Body)
		originBytes.Add(int32(len(body)))
		w.Header().Set("Location", "http://redirected.example.com/hop1")
		w.WriteHeader(http.StatusFound)
	}))
	origin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			originConnections.Add(1)
		}
	}
	origin.Start()
	t.Cleanup(origin.Close)
	originEndpoint := origin.Listener.Addr().(*net.TCPAddr).AddrPort()

	egress.TestingResolve(t, func(_ context.Context, _, host string) ([]netip.Addr, error) {
		switch host {
		case "nominal.example.com":
			originLookups.Add(1)
			return []netip.Addr{originEndpoint.Addr()}, nil
		case "redirected.example.com", "route2.example.com":
			targetLookups.Add(1)
			return []netip.Addr{targetEndpoint.Addr()}, nil
		default:
			return nil, fmt.Errorf("unexpected DNS lookup for %q", host)
		}
	})

	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		confine.SetEgressResolver(nil)
	})

	var mu sync.Mutex
	var observed []string
	allowTarget := false
	cancelOnTarget := false
	var cancelFunc context.CancelFunc
	confine.SetEgressResolver(func(ctx context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		mu.Lock()
		observed = append(observed, fmt.Sprintf("%s %s", ep.RequestMethod, ep.DialAddr()))
		shouldCancel := cancelOnTarget && ep.Host == "redirected.example.com"
		allow := ep.Host == "nominal.example.com" || allowTarget
		mu.Unlock()
		if shouldCancel && cancelFunc != nil {
			cancelFunc()
			<-ctx.Done()
			return false
		}
		return allow
	})

	opts := ExchangeOptions{Dial: []outboundhttp.ResolveMapping{
		{Host: "nominal.example.com", Port: 80, TargetPort: originEndpoint.Port()},
		{Host: "redirected.example.com", Port: 80, TargetHost: "route2.example.com", TargetPort: targetEndpoint.Port()},
	}}
	args := map[string]any{
		"method":    "POST",
		"url":       "http://nominal.example.com/start",
		"body_text": "hop0-body",
		"redirects": "safe",
	}

	// 1. Cancellation while awaiting Hop 1 approval returns context.Canceled (not HostDeniedError),
	// leaves Hop 1 unsent, and does not poison session or confine verdict caches.
	sessionID := t.Name()
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID, ToolCallID: "hop1-canceled"},
		Local:    tools.InvocationLocal{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{originEndpoint.Port(), targetEndpoint.Port()}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	mu.Lock()
	cancelOnTarget = true
	cancelFunc = cancel
	mu.Unlock()
	_, err := Perform(ctx, Deps{Boundary: testBoundary()}, args, tc, opts)
	var hostDenied *egressgate.HostDeniedError
	if !errors.Is(err, context.Canceled) || errors.As(err, &hostDenied) {
		t.Fatalf("canceled Hop 1 wait error = %v (hostDenied=%v), want context.Canceled without HostDeniedError", err, hostDenied)
	}
	if originConnections.Load() != 1 || originRequests.Load() != 1 || originBytes.Load() != int32(len("hop0-body")) || originLookups.Load() != 1 {
		t.Fatalf("canceled Hop 1 initial hop accounting = conn:%d req:%d bytes:%d dns:%d", originConnections.Load(), originRequests.Load(), originBytes.Load(), originLookups.Load())
	}
	if targetConnections.Load() != 0 || targetRequests.Load() != 0 || targetBytes.Load() != 0 || targetLookups.Load() != 0 {
		t.Fatalf("canceled Hop 1 received effects: conn:%d req:%d bytes:%d dns:%d", targetConnections.Load(), targetRequests.Load(), targetBytes.Load(), targetLookups.Load())
	}

	// 2. Retrying in the SAME session with un-canceled context while policy still denies Hop 1:
	// proves cancellation did not create a false quiet grant, rewritten GET method is passed to Hop 1
	// admission, Hop 1 receives zero resolver/connection/request effects, and reject retains Hop 0 progress + redirects.
	mu.Lock()
	cancelOnTarget = false
	cancelFunc = nil
	observed = nil
	mu.Unlock()
	tc.Identity.ToolCallID = "hop1-denied"
	_, err = Perform(t.Context(), Deps{Boundary: testBoundary()}, args, tc, opts)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "HTTP_REQUEST_HOST_DENIED" {
		t.Fatalf("denied redirect hop error = %v (reject=%+v), want HTTP_REQUEST_HOST_DENIED", err, reject)
	}
	if originRequests.Load() != 2 || originBytes.Load() != int32(2*len("hop0-body")) {
		t.Fatalf("initial hop accounting after second attempt = req:%d bytes:%d", originRequests.Load(), originBytes.Load())
	}
	if targetConnections.Load() != 0 || targetRequests.Load() != 0 || targetBytes.Load() != 0 || targetLookups.Load() != 0 {
		t.Fatalf("denied redirect hop received effects: conn:%d req:%d bytes:%d dns:%d", targetConnections.Load(), targetRequests.Load(), targetBytes.Load(), targetLookups.Load())
	}
	if reject.Data["http_requests_started"] != 1 || reject.Data["http_responses_received"] != 1 || reject.Data["http_last_status"] != http.StatusFound {
		t.Fatalf("reject lost initial hop progress: %+v", reject.Data)
	}
	redirects, ok := reject.Data["redirects"].([]any)
	if !ok || len(redirects) != 1 {
		t.Fatalf("reject lost completed redirect hop chain: %#v", reject.Data["redirects"])
	}
	hop0, _ := redirects[0].(map[string]any)
	if hop0["status"] != http.StatusFound || hop0["method"] != http.MethodGet {
		t.Fatalf("reject redirect hop0 = %#v, want 302 GET", hop0)
	}
	mu.Lock()
	gotObserved := slices.Clone(observed)
	observed = nil
	mu.Unlock()
	wantObserved := []string{
		"POST nominal.example.com:80",
		fmt.Sprintf("POST nominal.example.com:%d", originEndpoint.Port()),
		"GET redirected.example.com:80",
	}
	if !slices.Equal(gotObserved, wantObserved) {
		t.Fatalf("per-hop admission commands/endpoints = %v, want %v", gotObserved, wantObserved)
	}

	// 3. Authorizing Hop 1 in the SAME session after cancellation and denial succeeds across a multi-hop
	// redirect chain (Hop 0 -> Hop 1 /hop1 -> Hop 2 /landed on redirected.example.com) and reuses
	// the action's route approval across same-host hops without re-prompting.
	mu.Lock()
	allowTarget = true
	observed = nil
	mu.Unlock()
	tc.Identity.ToolCallID = "hop1-approved-1"
	got, err := Perform(t.Context(), Deps{Boundary: testBoundary()}, args, tc, opts)
	testutil.FailErr(t, "perform after canceled wait", err)
	if string(got.Response.Body) != "redirect-target-ok" || len(got.Redirects) != 2 || targetConnections.Load() != 2 || targetRequests.Load() != 2 || targetLookups.Load() != 2 {
		t.Fatalf("approved redirect exchange = %q redirects=%d conn:%d req:%d dns:%d", string(got.Response.Body), len(got.Redirects), targetConnections.Load(), targetRequests.Load(), targetLookups.Load())
	}
	mu.Lock()
	observedAfterFirstApprove := slices.Clone(observed)
	observed = nil
	mu.Unlock()
	wantApproved := []string{
		"POST nominal.example.com:80",
		fmt.Sprintf("POST nominal.example.com:%d", originEndpoint.Port()),
		"GET redirected.example.com:80",
		fmt.Sprintf("GET route2.example.com:%d", targetEndpoint.Port()),
	}
	if !slices.Equal(observedAfterFirstApprove, wantApproved) {
		t.Fatalf("multi-hop redirect chain endpoints = %v, want %v (same-host Hop 2 must reuse Hop 1 coverage without re-prompting)", observedAfterFirstApprove, wantApproved)
	}

	// 4. Explicit host_header across redirects: preserved on same-origin hops, cleared when redirecting
	// cross-origin to redirected.example.com, while cross-origin hop admission still gates redirected.example.com.
	allowRedirected := false
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		if ep.Host == "redirected.example.com" || ep.Host == "route2.example.com" {
			return allowRedirected
		}
		return true
	})
	vhostArgs := map[string]any{
		"method":      "GET",
		"url":         "http://nominal.example.com/start",
		"host_header": "vhost.example.com",
		"redirects":   "safe",
	}
	beforeTargetReqs := targetRequests.Load()
	tc.Identity.ToolCallID = "vhost-cross-origin-denied"
	_, err = Perform(t.Context(), Deps{Boundary: testBoundary()}, vhostArgs, tc, opts)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "HTTP_REQUEST_HOST_DENIED" || reject.Data["host"] != "redirected.example.com" || targetRequests.Load() != beforeTargetReqs {
		t.Fatalf("denied cross-origin redirect with host_header: err=%v reject=%+v targetReqsDelta=%d", err, reject, targetRequests.Load()-beforeTargetReqs)
	}
	allowRedirected = true
	tc.Identity.ToolCallID = "vhost-cross-origin-approved"
	got, err = Perform(t.Context(), Deps{Boundary: testBoundary()}, vhostArgs, tc, opts)
	testutil.FailErr(t, "approved host_header across redirect", err)
	if string(got.Response.Body) != "redirect-target-ok" || len(got.Redirects) != 2 {
		t.Fatalf("approved host_header redirect response = %q redirects=%d", string(got.Response.Body), len(got.Redirects))
	}
}

