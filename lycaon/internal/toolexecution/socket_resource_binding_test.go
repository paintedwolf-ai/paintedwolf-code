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
	"github.com/lycaon/lycaon/internal/tools"
)

type resourceSocketReview struct {
	hitl.CheckpointManager
	runtime *approvalstate.SocketCapabilityRuntime
	calls   int
}

func (r *resourceSocketReview) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	r.calls++
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
	first, err := executor.Capabilities.preflightSocketCapability(ctx, "http_request", args, tc)
	testutil.FailErr(t, "retained request reaches explicit socket approval", err)
	if review.calls != 1 || first.AuthorizationSource != authzledger.AuthorizationSourceHuman {
		t.Fatalf("pathname lease covered retained request: calls=%d, source=%s", review.calls, first.AuthorizationSource)
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityGranted, authzledger.AuthorizationSourceHuman)
	grant := first.SocketGrants[0]
	if !runtime.GrantChat(tc.ChatSessionID(), grant, "reviewed-chat", "human-review", first.ActionDigest, nil) {
		t.Fatal("install explicitly reviewed live chat binding")
	}
	nextContext, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	tc.Identity.ToolCallID = "same-live-object"
	same, err := executor.Capabilities.preflightSocketCapability(nextContext, "http_request", args, tc)
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
	changed, err := executor.Capabilities.preflightSocketCapability(replacementContext, "http_request", args, tc)
	testutil.FailErr(t, "replacement reaches fresh socket review", err)
	if review.calls != 2 || changed.AuthorizationSource != authzledger.AuthorizationSourceHuman || changed.SocketGrants[0].Binding == grant.Binding {
		t.Fatalf("replacement inherited authority: %+v, calls=%d", changed, review.calls)
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityGranted, authzledger.AuthorizationSourceHuman)
	_ = original
	// Only the explicit kernel-pathname realization may use resource pathname coverage.
	resolved, err := filepath.EvalSymlinks(path)
	testutil.FailErr(t, "kernel realization label", err)
	kernel := confine.SocketGrant{ApprovedPath: path, ResolvedPath: resolved, Binding: socketref.Binding{Enforcement: socketref.KernelPathname}}
	covered, extras := executor.Capabilities.permitCoveredHostResourceSockets(t.Context(), hitl.ProposedAction{}, "kernel-action", []confine.SocketGrant{kernel}, tc)
	if !covered || len(extras) != 0 {
		t.Fatalf("kernel pathname lease = %v, %+v", covered, extras)
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityGranted, authzledger.AuthorizationSourceLease)
	if got := runtime.AuthorizedGrants(tc.ChatSessionID(), hitl.EffectPermitIdentity{SessionID: tc.Identity.SessionID, ToolCallID: tc.Identity.ToolCallID}, "kernel-action", []confine.SocketGrant{kernel}); len(got) != 1 {
		t.Fatal("kernel pathname permit was not issued")
	}
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

type resourceSocketGate struct{ coveringHostResourceGate }

func (resourceSocketGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
	return &hitl.ApprovalResult{Decision: decision}, nil
}
