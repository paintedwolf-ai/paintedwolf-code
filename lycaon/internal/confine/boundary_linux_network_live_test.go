//go:build linux

package confine_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

// probeTarget is a destination only the broker can answer for: it does not
// resolve, so a response proves the request reached the broker.
const probeTarget = "http://pw-confine-probe.invalid/"

// proxyGetProbe fetches probeTarget the way any tool does, through the proxy
// its environment names, and prints the status or the transport error.
func proxyGetProbe() int {
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	resp, err := client.Get(probeTarget)
	if err != nil {
		fmt.Println("proxyget error:", err)
		return 0
	}
	_ = resp.Body.Close()
	fmt.Println("proxyget status:", resp.StatusCode)
	return 0
}

// proxyConfinement binds a proxy-only boundary to a fresh action.
func proxyConfinement(t *testing.T, root string) (*confine.Confinement, *confine.ActionLease) {
	t.Helper()
	c := &confine.Confinement{Roots: []string{root}, Network: confine.NetworkProxyOnly, ProjectID: "linux-proxy"}
	lease, err := confine.BindAction(c, confine.EgressCommand{
		ProjectID: "linux-proxy", SessionID: "s1", ToolCallID: "call-" + t.Name(), ToolName: "command", CommandLine: "proxyget",
	})
	testutil.FailErr(t, "bind action", err)
	t.Cleanup(func() { lease.Close(context.Background()) })
	return c, lease
}

func runProxyProbe(t *testing.T, self string, c *confine.Confinement) string {
	t.Helper()
	env := confine.ProcessEnvironment(probeEnv("proxyget"), *c)
	status, out := linuxConfinedRun(t, self, *c, env, self, probeTestFlag)
	if status.ExitStatus() != 0 {
		t.Fatalf("proxy probe: exit %d: %s", status.ExitStatus(), out)
	}
	return out
}

// decideEgress makes every new destination ask, and answers allow.
func decideEgress(t *testing.T, allow bool) {
	t.Helper()
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })
	release := confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return allow
	})
	t.Cleanup(func() { _ = release(context.Background()) })
}

// The confined process reaches the broker only through the outer helper's
// forwarder, and the broker places the request under this action's lease.
func TestLinuxProxyOnlyReachesTheBrokerByLineage(t *testing.T) {
	self := requireLinuxSandbox(t)
	liveBroker(t)
	for _, allow := range []bool{false, true} {
		decideEgress(t, allow)
		c, lease := proxyConfinement(t, t.TempDir())
		out := runProxyProbe(t, self, c)
		if !strings.Contains(out, "proxyget status:") {
			t.Fatalf("allow=%v: the request did not reach the broker: %s", allow, out)
		}
		hosts := lease.ObservedHosts()
		if len(hosts) != 1 || hosts[0].Host != "pw-confine-probe.invalid" || hosts[0].Allowed != allow {
			t.Fatalf("allow=%v: broker observations = %+v\n%s", allow, hosts, out)
		}
	}
}

// With the broker gone, the forwarder has nowhere to send the request, and
// the namespace offers no other route.
func TestLinuxProxyOnlyWithoutTheBrokerFailsClosed(t *testing.T) {
	self := requireLinuxSandbox(t)
	liveBroker(t)
	decideEgress(t, true)
	c, lease := proxyConfinement(t, t.TempDir())
	testutil.FailErr(t, "stop broker", confine.StopEgressBroker())
	out := runProxyProbe(t, self, c)
	if !strings.Contains(out, "proxyget error:") {
		t.Fatalf("request succeeded without the broker: %s", out)
	}
	if hosts := lease.ObservedHosts(); len(hosts) != 0 {
		t.Fatalf("observations without a broker: %+v", hosts)
	}
	if _, err := confine.BindAction(&confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkProxyOnly}, confine.EgressCommand{ToolCallID: "after-stop"}); err == nil {
		t.Fatal("a proxy-only action bound with no broker running")
	}
}
