package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
)

func TestDestinationOpacityComesFromTransport(t *testing.T) {
	cmd := confine.EgressCommand{}
	http := destinationFact(cmd, egressproxy.Endpoint{
		Host: "127.0.0.1", Port: 8872, Transport: egressproxy.TransportHTTPRequest,
	}, true, "", "")
	if http.Opaque {
		t.Fatal("parsed HTTP request reported as opaque")
	}
	for _, transport := range []egressproxy.Transport{
		egressproxy.TransportHTTPConnect, egressproxy.TransportSocksTCP,
	} {
		fact := destinationFact(cmd, egressproxy.Endpoint{Host: "example.com", Port: 443, Transport: transport}, true, "", "")
		if !fact.Opaque {
			t.Fatalf("%s tunnel reported as inspectable", transport)
		}
	}
}

func TestLoopbackEgressComposesTaskCapability(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Boundary.SetSessionLoopbackGrant(func(_ context.Context, sessionID, parentSessionID string) (bool, []uint16) {
		if sessionID != "worker" || parentSessionID != "chat" {
			t.Fatalf("grant lookup = (%q, %q)", sessionID, parentSessionID)
		}
		return true, []uint16{8872}
	})
	cmd := confine.EgressCommand{SessionID: "worker", RootSessionID: "chat"}
	if !exec.Network.loopbackLeaseCovers(t.Context(), cmd, egressproxy.Endpoint{Host: "127.0.0.1", Port: 8872}) {
		t.Fatal("approved loopback port did not cover mediated egress")
	}
	if exec.Network.loopbackLeaseCovers(t.Context(), cmd, egressproxy.Endpoint{Host: "127.0.0.1", Port: 8873}) {
		t.Fatal("narrow loopback grant covered another port")
	}
	if exec.Network.loopbackLeaseCovers(t.Context(), cmd, egressproxy.Endpoint{Host: "example.com", Port: 8872}) {
		t.Fatal("loopback grant covered a remote host")
	}
	// Resolved addresses determine destination authority.
	for _, host := range []string{"localhost", "db.localhost", "exfil.localhost", "localhost."} {
		if exec.Network.loopbackLeaseCovers(t.Context(), cmd, egressproxy.Endpoint{Host: host, Port: 8872}) {
			t.Fatalf("loopback grant claimed %q from its spelling", host)
		}
	}
}

func detectionKey(cmd confine.EgressCommand, ep egressproxy.Endpoint, det *confine.EgressDetectionCitation) string {
	subject := egressAskFor(cmd, ep, det)
	return subject.ApprovalKey
}

func TestDetectionEgressApprovalKeyUsesExactExecutionAndRule(t *testing.T) {
	match := &confine.EgressDetectionCitation{PackID: "p", RuleID: "r", Level: "high"}
	base := confine.EgressCommand{
		SessionID: "chat", ProjectDir: "/project", ToolCallID: "tc-1",
		CommandLine: "script --mode read",
	}
	ep := egressproxy.Endpoint{Host: "api.example.com", Transport: egressproxy.TransportHTTPConnect, Port: 443}
	key := detectionKey(base, ep, match)
	if key == "" || key != detectionKey(base, ep, match) {
		t.Fatal("identical observations must have one stable coalesce key")
	}

	altered := base
	altered.CommandLine = "script --mode write"
	if key == detectionKey(altered, ep, match) {
		t.Fatal("altered commands must not share a detection decision")
	}
	otherRule := *match
	otherRule.RuleID = "another-rule"
	if key == detectionKey(base, ep, &otherRule) {
		t.Fatal("different rules must not share a detection decision")
	}
}

// A declared-set ask is identified by the set, so every endpoint in one fan-out
// resolves to one approval identity — and a different catalog is a different ask.
func TestDeclaredSetApprovalKeyIsTheSetNotTheEndpoint(t *testing.T) {
	hosts := []string{"crates.io", "pkg.go.dev", "registry.npmjs.org"}
	cmd := confine.EgressCommand{SessionID: "chat", ToolCallID: "tc-1", DeclaredHosts: hosts}

	first := egressAskFor(cmd, egressproxy.Endpoint{Host: "crates.io"}, nil)
	second := egressAskFor(cmd, egressproxy.Endpoint{Host: "pkg.go.dev"}, nil)
	if first.ApprovalKey == "" || first.ApprovalKey != second.ApprovalKey {
		t.Fatalf("endpoints of one declared set must share an approval key: %q vs %q", first.ApprovalKey, second.ApprovalKey)
	}

	// Host order is not identity.
	reordered := cmd
	reordered.DeclaredHosts = []string{"pkg.go.dev", "REGISTRY.NPMJS.ORG", "crates.io"}
	same := egressAskFor(reordered, egressproxy.Endpoint{Host: "crates.io"}, nil)
	if same.ApprovalKey != first.ApprovalKey {
		t.Fatal("the same set in a different order or case must not re-ask")
	}

	// A changed catalog is a new decision.
	widened := cmd
	widened.DeclaredHosts = append(append([]string(nil), hosts...), "evil.example")
	changed := egressAskFor(widened, egressproxy.Endpoint{Host: "crates.io"}, nil)
	if changed.ApprovalKey == first.ApprovalKey {
		t.Fatal("a widened destination set must re-ask")
	}
}

// Verifies that the card payload, grant identity args, and copy name the same destinations.
func TestDeclaredSetSubjectDescribesTheWholeSet(t *testing.T) {
	hosts := []string{"crates.io", "pkg.go.dev"}
	cmd := confine.EgressCommand{SessionID: "chat", ToolCallID: "tc-1", DeclaredHosts: hosts}
	subject := egressAskFor(cmd, egressproxy.Endpoint{Host: "crates.io"}, nil)

	if subject.Declared == nil || subject.Declared.HostCount != 2 {
		t.Fatalf("declared payload = %+v want 2 hosts", subject.Declared)
	}
	if subject.Contained.DeclaredHostCount != 2 || subject.Contained.DeclaredHostsDigest == "" {
		t.Fatalf("contained must carry the permit: %+v", subject.Contained)
	}
	if subject.Contained.DeclaredHostsDigest != subject.Declared.Digest {
		t.Fatal("the permit witness and the card payload must cite one digest")
	}
	if _, ok := subject.Args["host"]; ok {
		t.Fatal("a set ask must not present itself as a single-host ask")
	}
	if got, _ := subject.Args["host_count"].(int); got != 2 {
		t.Fatalf("args host_count = %v want 2", subject.Args["host_count"])
	}
	if !strings.Contains(subject.Title, "2 configured endpoints") {
		t.Fatalf("title should name the set: %q", subject.Title)
	}
	if !strings.Contains(subject.Explanation.AllowLine, "2 configured endpoints") {
		t.Fatalf("allow line should name what approval covers: %q", subject.Explanation.AllowLine)
	}
}

// Observed endpoints remain distinct from the declared approval set.
func TestSingleEndpointSubjectStillNamesItsHost(t *testing.T) {
	cmd := confine.EgressCommand{SessionID: "chat", ToolCallID: "tc-1"}
	ep := egressproxy.Endpoint{Host: "api.example.com", Transport: egressproxy.TransportHTTPConnect, Port: 443}
	subject := egressAskFor(cmd, ep, nil)
	if subject.Args["host"] != "api.example.com" {
		t.Fatalf("args = %+v", subject.Args)
	}
	if subject.Declared != nil {
		t.Fatal("an endpoint ask must not carry a declared-set payload")
	}
	if subject.ApprovalKey != "" {
		t.Fatal("an endpoint ask is identified by its action digest, not an override key")
	}
}
