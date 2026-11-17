//go:build linux || darwin

package toolexecution

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

type resourceSocketReview struct {
	hitl.CheckpointManager
	runtime *approvalstate.SocketCapabilityRuntime
	calls   int
	// quiet counts the quiet rungs on the latest plan.
	quiet int
}

func (r *resourceSocketReview) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	r.calls++
	r.quiet = 0
	for _, option := range req.ApprovalPlan.Options {
		if option.Kind == hitl.ApprovalOptionQuiet {
			r.quiet++
		}
	}
	for _, option := range req.ApprovalPlan.Options {
		if option.Rung != hitl.ApprovalRungOnce {
			continue
		}
		for _, delta := range option.Authority {
			if delta.Kind != hitl.AuthoritySocketPermit {
				continue
			}
			for _, target := range delta.Sockets {
				r.runtime.IssuePermit(delta.ChatSession(), hitl.EffectPermitIdentity{SessionID: delta.SessionID, ToolCallID: delta.ToolCallID}, delta.ActionDigest, confine.SocketGrant{ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath, Binding: target.Binding})
			}
		}
	}
	return &hitl.CheckpointResponse{CheckpointID: "human-review", Status: hitl.DecisionStatusPending}, nil
}
func (*resourceSocketReview) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{Status: hitl.DecisionStatusApproved, Result: &hitl.DecisionResult{Approved: true}}, nil
}
func (*resourceSocketReview) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestHostResourceSocketCoverageFollowsRealBindingAtCapabilitiesOwner(t *testing.T) {
	for _, operation := range []string{"http_request", "command"} {
		t.Run(operation, func(t *testing.T) { assertHostResourceSocketBinding(t, operation) })
	}
}

func assertHostResourceSocketBinding(t *testing.T, operation string) {
	t.Helper()
	ctx := socketInvocationContext(t)
	root := shortTempDir(t)
	path := filepath.Join(root, "service")
	original := listenUnixSocket(t, path)
	runtime := approvalstate.NewSocketCapabilityRuntime()
	defer runtime.Close()
	recorder := &capabilityRecorder{}
	executor := NewExecutor(nil, nil, "")
	executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	executor.Approvals.SetAuthzRecorder(recorder)
	review := &resourceSocketReview{runtime: runtime}
	executor.Approvals.SetCheckpointManager(ctx, review, resourceSocketGate{})
	defer confine.SetEgressResolver(nil)
	tc := httpRequestSocketContext(t, root)
	tc.Host.RealizationSockets = []string{path}
	tc.Host.HostResources = []string{"fixture-service"}
	args := map[string]any{"url": "http://service.invalid/", "unix_socket": path}
	if operation == "command" {
		contract, ok := toolcontract.Lookup(operation)
		if !ok {
			t.Fatal("command contract is unavailable")
		}
		tc.Invocation.Contract = contract
		args = map[string]any{"command": "external -- fixture-client", "capability_request": map[string]any{"socket_paths": []any{path}}}
	}
	first, err := executor.Capabilities.preflightSocketCapability(ctx, operation, args, tc)
	testutil.FailErr(t, "retained request reaches explicit socket approval", err)
	if review.calls != 1 || first.AuthorizationSource != authzledger.AuthorizationSourceHuman {
		t.Fatalf("pathname lease covered retained request: calls=%d, source=%s", review.calls, first.AuthorizationSource)
	}
	if review.quiet != 1 {
		t.Fatalf("socket approval plan offers %d quiet rungs, want 1", review.quiet)
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityGranted, authzledger.AuthorizationSourceHuman)
	grant := first.SocketGrants[0]
	if !runtime.GrantChat(tc.ChatSessionID(), grant, "reviewed-chat", "human-review", first.ActionDigest, nil) {
		t.Fatal("install explicitly reviewed live chat binding")
	}
	nextContext, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	tc.Identity.ToolCallID = "same-live-object"
	same, err := executor.Capabilities.preflightSocketCapability(nextContext, operation, args, tc)
	testutil.FailErr(t, "reuse live object", err)
	if review.calls != 1 || same.AuthorizationSource != authzledger.AuthorizationSourceLease || same.SocketGrants[0].Binding != grant.Binding {
		t.Fatalf("live binding reuse = %+v, calls=%d", same, review.calls)
	}
	tc.Socket = tools.InvocationSocket{SocketGrants: same.SocketGrants, DurableSocketGrants: same.DurableSocketGrants, SocketActionDigest: same.ActionDigest, SocketCapabilityRuntime: runtime, SocketAuthorizationSource: same.AuthorizationSource}
	tc.Local.AuthzRecorder = recorder
	if _, reject := tools.ClaimDeclaredSocket(nextContext, tc, path); reject != nil {
		t.Fatalf("claim live binding: %v", reject)
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityApplied, authzledger.AuthorizationSourceLease)
	testutil.FailErr(t, "rename prior listener", os.Rename(path, path+"-original"))
	replacement, err := net.Listen("unix", path)
	testutil.FailErr(t, "replacement listener", err)
	defer replacement.Close()
	replacementContext, replacementScope := socketref.WithScope(t.Context())
	defer replacementScope.Close()
	tc.Identity.ToolCallID = "replacement-object"
	changed, err := executor.Capabilities.preflightSocketCapability(replacementContext, operation, args, tc)
	testutil.FailErr(t, "replacement reaches fresh socket review", err)
	if review.calls != 2 || changed.AuthorizationSource != authzledger.AuthorizationSourceHuman || changed.SocketGrants[0].Binding == grant.Binding {
		t.Fatalf("replacement inherited authority: %+v, calls=%d", changed, review.calls)
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityGranted, authzledger.AuthorizationSourceHuman)
	_ = original

}

func assertSocketLedgerSource(t *testing.T, records []authzledger.CapabilityRecord, action, source string) {
	t.Helper()
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Action == action {
			if records[i].AuthorizationSource != source {
				t.Fatalf("%s source=%q, want %q", action, records[i].AuthorizationSource, source)
			}
			return
		}
	}
	t.Fatalf("ledger lacks %s", action)
}

// The direct-network card offers the same quiet rung as every other approval.
func TestDirectIPApprovalOffersQuiet(t *testing.T) {
	ctx := socketInvocationContext(t)
	executor := NewExecutor(nil, nil, "")
	review := &resourceSocketReview{}
	executor.Approvals.SetCheckpointManager(ctx, review, resourceSocketGate{})
	declared := []string{"https://rates.example.test:443"}
	tc := httpRequestSocketContext(t, shortTempDir(t))
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is unavailable")
	}
	tc.Invocation.Contract = contract
	args := map[string]any{
		"command":            "python3 rates.py",
		"capability_request": map[string]any{"direct_ip": map[string]any{"declared_destinations": []any{declared[0]}}},
	}
	action, lease := DirectIPReview("command", args, tc, declared, nil)
	approval, err := resourceSocketGate{}.Evaluate(ctx, action)
	testutil.FailErr(t, "evaluate the explicit approval request", err)
	testutil.FailErr(t, "direct-network approval", executor.Capabilities.awaitDirectIPCapability(ctx, action, lease, tc, approval))
	if review.calls != 1 || review.quiet != 1 {
		t.Fatalf("direct-network asks = %d with %d quiet rungs, want one ask with one quiet rung", review.calls, review.quiet)
	}
}

type resourceSocketGate struct{ coveringHostResourceGate }

func (resourceSocketGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
	return &hitl.ApprovalResult{Decision: decision}, nil
}
