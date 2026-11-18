package toolexecution_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// Authorization can cover a capability without changing what the process can do.
// Observe the real executor's gate inputs and claim the real launch permit; the
// collaborator allows review so this isolates factual projection, not matching.
type containmentObservingGate struct {
	countingApprovalGate
	actions []hitl.ProposedAction
}

func (g *containmentObservingGate) Evaluate(_ context.Context, action hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	g.actions = append(g.actions, action)
	return &hitl.ApprovalResult{}, nil
}

func TestReviewedHostExecutionPreservesPhysicalBoundaryFacts(t *testing.T) {
	root := t.TempDir()
	approvals := &containmentObservingGate{}
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	entered := false
	testutil.FailErr(t, "register command", registry.Register("command", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		req, reject := tools.ConfineRequestForSpawn(ctx, tc, nil)
		if reject != nil {
			return "", reject
		}
		if !req.HostExecution {
			t.Error("approved host execution lost its launch capability")
		}
		entered = true
		return "done", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvals), registry, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), &asyncHITL{requested: make(chan struct{}, 4)}, approvals)
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "task", ToolCallID: "call", Agent: "implement"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"},
	}
	_, err := executor.Invoke(t.Context(), "command", map[string]any{"command": "printf fixture", "capability_request": map[string]any{"host_execution": true}}, tc)
	testutil.FailErr(t, "invoke authorized host execution", err)
	if !entered || len(approvals.actions) == 0 {
		t.Fatal("test did not reach gate and authorized handler")
	}
	for _, action := range approvals.actions {
		if action.Invocation.Tool != "command" {
			continue
		}
		if !action.Execution.Contained.HostExecution || action.Execution.Contained.FSJailed {
			t.Fatalf("authorization rewrote physical confinement facts: %+v", action.Execution.Contained)
		}
	}
}

// The gate reviews the boundary the spawn applies: Contained on the gated
// command is the projection of the request the handler's spawn finalizes, and
// it carries the attached root under a jailed, mediated boundary.
func TestGateContainedIsTheAppliedSpawnBoundary(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	if !confine.Available() {
		caps, _ := confine.LinuxCapabilities()
		t.Fatalf("confinement unavailable: %+v", caps)
	}
	root := t.TempDir()
	approvals := &containmentObservingGate{}
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	var applied []confine.Request
	testutil.FailErr(t, "register command", registry.Register("command", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		req, reject := tools.ConfineRequestForSpawn(ctx, tc, nil)
		if reject != nil {
			return "", reject
		}
		applied = append(applied, req)
		return "done", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvals), registry, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), &asyncHITL{requested: make(chan struct{}, 4)}, approvals)
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "task", ToolCallID: "call", Agent: "implement"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"},
	}
	_, err := executor.Invoke(t.Context(), "command", map[string]any{"command": "printf fixture"}, tc)
	testutil.FailErr(t, "invoke command", err)
	if len(applied) != 1 {
		t.Fatalf("spawn finalized %d requests, want 1", len(applied))
	}
	var gated []hitl.Contained
	for _, action := range approvals.actions {
		if action.Invocation.Tool == "command" {
			gated = append(gated, action.Execution.Contained)
		}
	}
	if len(gated) == 0 {
		t.Fatal("the gate never saw the command")
	}
	want := hitl.ContainedForRequest(applied[0])
	for _, got := range gated {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("gate Contained differs from the applied spawn request:\ngate:    %+v\napplied: %+v", got, want)
		}
		if !got.FSJailed || got.Egress != hitl.ContainedEgressProxy || !slices.Contains(got.Roots, fspath.CanonicalPath(root)) && !slices.Contains(got.Roots, root) {
			t.Fatalf("gate Contained is not the jailed, mediated root boundary: %+v", got)
		}
	}
}

// A socket the tool context names but this invocation never captured is not
// part of the applied boundary: the spawn refuses it with the typed socket
// code before anything launches.
func TestSpawnRefusesASocketTheBoundaryLacks(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	setSocketReferenceState(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	root := t.TempDir()
	sockDir, err := os.MkdirTemp("/tmp", "lyc-gc-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "svc.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	grant, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "resolve socket", err)

	approvals := &containmentObservingGate{}
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	launched := false
	var spawnReject *toolrejection.ToolReject
	testutil.FailErr(t, "register command", registry.Register("command", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		if _, reject := tools.ConfineRequestForSpawn(ctx, tc, nil); reject != nil {
			spawnReject = reject
			return "", reject
		}
		launched = true
		return "done", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvals), registry, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), &asyncHITL{requested: make(chan struct{}, 4)}, approvals)
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "task", ToolCallID: "call", Agent: "implement"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"},
		Socket:   tools.InvocationSocket{SocketGrants: []confine.SocketGrant{grant}},
	}
	_, err = executor.Invoke(t.Context(), "command", map[string]any{"command": "printf fixture"}, tc)
	if launched {
		t.Fatal("a socket without a permit reached launch")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != isolation.CodeSocketPathChanged || spawnReject == nil {
		t.Fatalf("invoke = %v (spawn reject %+v), want a spawn refusal %s", err, spawnReject, isolation.CodeSocketPathChanged)
	}
}
