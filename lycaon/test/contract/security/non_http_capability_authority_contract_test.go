package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Closed authority paths for structured non-HTTP capability requests.

// The schema each process tool ships and its compiled contract agree on the
// capability request fields: command takes sockets and direct IP, a held
// terminal takes sockets only, and every process tool exposes socks_proxy.
func TestNonHTTPContractSocketAndDirectToolBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool   string
		socket bool
		direct bool
	}{
		{tool: "command", socket: true, direct: true},
		{tool: "terminal_open", socket: true, direct: false},
		{tool: "verify", socket: true, direct: true},
	} {
		raw, err := config.Read(config.ToolSchemasDir.Join(tc.tool + ".yaml"))
		contractcheck.FailErr(t, "read "+tc.tool+" schema", err)
		entry, err := toolschema.ParseEntry(raw)
		contractcheck.FailErr(t, "parse "+tc.tool+" schema", err)
		fields := map[string]bool{}
		for _, path := range toolschema.ArgFieldPaths(entry.Schema) {
			fields[path] = true
		}
		contract, ok := toolcontract.Lookup(tc.tool)
		if !ok {
			t.Fatalf("%s has no compiled contract", tc.tool)
		}
		for _, axis := range []struct {
			field      string
			capability toolcontract.Capability
			want       bool
		}{
			{"socket_paths", toolcontract.CapabilitySocket, tc.socket},
			{"direct_ip", toolcontract.CapabilityDirectIP, tc.direct},
		} {
			if got := fields["capability_request."+axis.field]; got != axis.want {
				t.Errorf("%s schema capability_request.%s = %v, want %v", tc.tool, axis.field, got, axis.want)
			}
			if got := contract.Supports(axis.capability); got != axis.want {
				t.Errorf("%s contract supports %s = %v, want %v", tc.tool, axis.field, got, axis.want)
			}
		}
		if !fields["socks_proxy"] {
			t.Errorf("%s schema has no top-level socks_proxy", tc.tool)
		}
	}
}

func TestNonHTTPContractTypedAuthResolvesOnlyMatchingSubstrate(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	g := confine.SocketGrant{ApprovedPath: "/tmp/a.sock", ResolvedPath: "/tmp/a.sock"}
	digest := confine.SocketPathsDigest([]confine.SocketGrant{g})

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	contractcheck.FailErr(t, "new approval store", err)
	approvals := settings.NewRuleApprovalGate(store, settings.NoSources())

	socketOnly := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "true"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{
				FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj},
				SocketPathsDigest: digest, SocketCount: 1,
			},
		},
		Sockets: hitl.ActionSockets{
			SocketGrants:            []confine.SocketGrant{g},
			AuthorizedSocketDigests: []string{digest},
		},
	}
	res, err := approvals.Evaluate(context.Background(), socketOnly)
	contractcheck.FailErr(t, "evaluate authorized socket", err)
	if !res.AutoApproved() {
		t.Fatalf("an authorized socket set must clear its own channel: %+v", res)
	}

	unauthSocket := socketOnly
	unauthSocket.Sockets.AuthorizedSocketDigests = nil
	res, err = approvals.Evaluate(context.Background(), unauthSocket)
	contractcheck.FailErr(t, "evaluate unauthorized socket", err)
	if res.Gate() != api.GateUnobservedChannel {
		t.Fatalf("an unauthorized socket set must still ask: %+v", res)
	}

	direct := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "true"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{
				FSJailed: true, Egress: hitl.ContainedEgressDirectIP, DirectIP: true, Roots: []string{proj},
			},
		},
	}
	res, err = approvals.Evaluate(context.Background(), direct)
	contractcheck.FailErr(t, "evaluate direct ip", err)
	if res.Gate() != api.GateUnobservedChannel {
		t.Fatalf("unmediated direct networking must ask: %+v", res)
	}

	directWithSocketAuth := direct
	directWithSocketAuth.Sockets.AuthorizedSocketDigests = []string{digest}
	res, err = approvals.Evaluate(context.Background(), directWithSocketAuth)
	contractcheck.FailErr(t, "evaluate direct ip with socket auth", err)
	if res.Gate() != api.GateUnobservedChannel {
		t.Fatalf("socket authority must not resolve the direct-network axis: %+v", res)
	}

	authorizedDirect := direct
	authorizedDirect.Egress.AuthorizedDirectIP = true
	res, err = approvals.Evaluate(context.Background(), authorizedDirect)
	contractcheck.FailErr(t, "evaluate authorized direct ip", err)
	if !res.AutoApproved() {
		t.Fatalf("an authorized direct-network action must clear its own axis: %+v", res)
	}
}

// A direct-IP card offers no durable authority: every enabled rung is
// chat-scoped and the project slot is disabled, whatever identity the action
// carries.
func TestNonHTTPContractDirectAndExecutionSocketOfferScopes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess", ToolCallID: "call", ProjectID: "proj"},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "root",
		},
	}
	action, lease := toolexecution.DirectIPReview("command", map[string]any{"command": "client"}, tc, []string{"203.0.113.7"}, nil)
	offers := capabilitygrants.DirectIPExecutionGrantOffers(action, lease)
	enabled := 0
	for _, offer := range offers {
		if offer.Disabled {
			continue
		}
		enabled++
		if offer.Scope != hitl.ApprovalGrantScopeChat || offer.Grant.Scope != hitl.ApprovalGrantScopeChat {
			t.Errorf("direct-IP card offered %s scope: %+v", offer.Scope, offer)
		}
	}
	if enabled == 0 {
		t.Fatalf("direct-IP card offered no rung for a complete lease: %+v", offers)
	}
}
