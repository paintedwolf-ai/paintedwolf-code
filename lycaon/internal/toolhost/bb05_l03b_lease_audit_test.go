//go:build unix

package toolhost

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

// L03b (narrowed lease-state verdict): Proves ActionLease.Close unbinds the
// lease (EgressBound becomes false) while CommandForLineage retains the retired
// lineage metadata. This test does not launch a descendant or observe an actual
// SOCKS refusal; historical PID attribution remains unproven.
func TestBB05_L03b_SocksTCPLeaseAttributionAudit(t *testing.T) {
	testutil.RequireLineage(t, lineage.Supported())
	addrs, err := confine.StartEgressBroker(t.TempDir(), net.Listen)
	testutil.FailErr(t, "start egress broker", err)
	t.Cleanup(func() { _ = confine.StopEgressBroker() })
	if addrs.SOCKS == "" {
		t.Fatal("egress broker started without SOCKS listener")
	}

	c := &confine.Confinement{
		Network:       confine.NetworkProxyOnly,
		SocksProxyEnv: true,
		ProjectID:     "bb05-l03b-project",
	}
	lease, err := confine.BindAction(c, confine.EgressCommand{
		ProjectID:   "bb05-l03b-project",
		SessionID:   "bb05-l03b-session",
		ToolCallID:  "bb05-l03b-call",
		ToolName:    "command",
		CommandLine: "grandchild-socks-audit",
	})
	testutil.FailErr(t, "bind action lease", err)
	t.Cleanup(func() { lease.Close(context.Background()) })

	if !confine.EgressBound(c) {
		t.Fatal("live action lease must be EgressBound")
	}
	envJoined := strings.Join(confine.ProxyEnv(*c), "\n")
	if !strings.Contains(envJoined, "ALL_PROXY=socks5h://"+addrs.SOCKS) {
		t.Fatalf("ProxyEnv missing socks5h ALL_PROXY: %s", envJoined)
	}

	lineageID := lease.LineageID()
	if lineageID == "" {
		t.Fatal("bound action missing lineage ID")
	}
	lease.Close(t.Context())

	if confine.EgressBound(c) {
		t.Fatal("closed action lease remained EgressBound")
	}
	retired, ok := confine.CommandForLineage(lineageID)
	if !ok || retired.ProjectID != "bb05-l03b-project" {
		t.Fatalf("retired lineage metadata = (%+v, %v), want retained project metadata", retired, ok)
	}
	t.Logf("L03b verdict: lease unbound after Close and retired lineage %q retained (no descendant SOCKS refusal observed; historical PID attribution unproven)", lineageID)
}
