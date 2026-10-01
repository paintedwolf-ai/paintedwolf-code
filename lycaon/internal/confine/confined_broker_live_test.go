package confine_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The whole chain, as a real command runs it: the applied Seatbelt profile, the
// environment the boundary hands the process, the descendant marker it carries,
// and the broker resolving that marker back to the action.
func TestConfinedCommandReachesTheBrokerByLineage(t *testing.T) {
	self := requireSeatbelt(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required to drive a confined process through the broker")
	}
	liveBroker(t)
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reached")
	}))
	defer upstream.Close()

	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	root := t.TempDir()
	c, ok := confine.DefaultConfinement(confine.Request{
		Roots: []string{root}, ProjectID: "live-probe", LoopbackConnect: true,
	})
	if !ok || c == nil || c.Network != confine.NetworkProxyOnly {
		t.Fatalf("expected a proxy-only boundary: ok=%v c=%+v", ok, c)
	}
	lease, err := confine.BindAction(c, confine.EgressCommand{
		ProjectID: "live-probe", SessionID: "s1", ToolCallID: "call-1",
		ToolName: "command", CommandLine: "curl " + upstream.URL,
	})
	testutil.FailErr(t, "bind action", err)
	t.Cleanup(func() { lease.Close(t.Context()) })

	// No -x: the process finds the front door the way any tool does, by reading
	// its environment.
	cmd, cleanup, err := confine.Command(context.Background(), self, "curl",
		[]string{"-s", "-m", "10", "-o", "/dev/null", "-w", "%{http_code}", upstream.URL}, *c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.Env = confine.ProcessEnvironment(os.Environ(), *c)
	out, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) != "200" {
		t.Fatalf("confined request through the broker = %q, want 200", out)
	}

	hosts := lease.ObservedHosts()
	if len(hosts) == 0 {
		t.Fatal("the broker recorded no destination for the confined command")
	}
	if !hosts[0].Allowed {
		t.Fatalf("recorded destination was not allowed: %+v", hosts[0])
	}
}
