package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type capabilityReviewCheckpoints struct {
	cannedWriteRootCheckpoints
	requests      []hitl.CheckpointRequest
	write, read   *approvalstate.SandboxPathGrantRuntime
	sockets       *approvalstate.SocketCapabilityRuntime
	listen        *approvalstate.SandboxPortGrantRuntime
	loopback      *approvalstate.SandboxPortGrantRuntime
	omitRead      bool
	once          bool
	afterApproval func()
}

func (c *capabilityReviewCheckpoints) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	c.requests = append(c.requests, req)
	if err := req.ApprovalPlan.Validate(); err != nil {
		return nil, err
	}
	if c.approve {
		option, ok := req.ApprovalPlan.Option(req.ApprovalPlan.RecommendedOptionID)
		if c.once {
			ok = false
			for _, offered := range req.ApprovalPlan.Options {
				if offered.Rung == hitl.ApprovalRungOnce && !offered.Disabled {
					option, ok = offered, true
				}
			}
		}
		if !ok || option.Disabled {
			return nil, fmt.Errorf("review has no selectable recommendation")
		}
		for _, delta := range option.Authority {
			c.install(delta)
		}
		if c.afterApproval != nil {
			c.afterApproval()
		}
	}
	return &hitl.CheckpointResponse{CheckpointID: "cp-1", Status: hitl.DecisionStatusPending}, nil
}

func (c *capabilityReviewCheckpoints) install(delta hitl.ApprovalAuthorityDelta) {
	switch delta.Kind {
	case hitl.AuthorityWriteRootChat:
		for _, path := range delta.WriteRoots {
			c.write.GrantSessionWriteRoot(delta.ChatSession(), path)
		}
	case hitl.AuthorityReadPathChat:
		if !c.omitRead {
			for _, path := range delta.ReadPaths {
				c.read.GrantSessionWriteRoot(delta.ChatSession(), path)
			}
		}
	case hitl.AuthoritySocketChat:
		for _, socket := range delta.Sockets {
			c.sockets.GrantChat(delta.ChatSession(), confine.SocketGrant{ApprovedPath: socket.ApprovedPath, ResolvedPath: socket.ResolvedPath}, delta.Grant.ID, "cp-1", delta.ActionDigest, nil)
		}
	case hitl.AuthoritySocketPermit:
		for _, socket := range delta.Sockets {
			c.sockets.IssuePermit(delta.SessionID, delta.ToolCallID, delta.ActionDigest, confine.SocketGrant{ApprovedPath: socket.ApprovedPath, ResolvedPath: socket.ResolvedPath})
		}
	case hitl.AuthorityLocalListenChat:
		c.listen.GrantSessionPorts(delta.ChatSession(), delta.ListenPorts)
	case hitl.AuthorityLoopbackConnectChat:
		c.loopback.GrantSessionPorts(delta.ChatSession(), delta.ConnectPorts)
	default:
	}
}

type capabilityReviewFixture struct {
	executor    *toolexecution.Executor
	checkpoints *capabilityReviewCheckpoints
	context     tools.ToolContext
	args        map[string]any
	invocations int
	wantPaths   bool
}

func newCapabilityReviewFixture(t *testing.T, approve bool) *capabilityReviewFixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cap-review-") //nolint:usetesting // Unix socket paths must fit sockaddr_un.
	testutil.FailErr(t, "create socket directory", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove socket directory", os.RemoveAll(dir)) })
	socket := filepath.Join(dir, "daemon.sock")
	listener, err := net.Listen("unix", socket)
	testutil.FailErr(t, "listen on fixture socket", err)
	t.Cleanup(func() { testutil.FailErr(t, "close fixture socket", listener.Close()) })
	// A temporary home can already be writable under the default confinement.
	write := filepath.Join(filepath.VolumeName(dir)+string(filepath.Separator), "capability-review", filepath.Base(dir), "build-state")
	read := filepath.Join(t.TempDir(), "protected-config")
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create fixture approval store", err)
	authority := settings.NewRuleApprovalGate(store, settings.NoSources())
	c := &capabilityReviewCheckpoints{
		cannedWriteRootCheckpoints: cannedWriteRootCheckpoints{approve: approve},
		write:                      approvalstate.NewSandboxPathGrantRuntime(), read: approvalstate.NewSandboxPathGrantRuntime(), sockets: approvalstate.NewSocketCapabilityRuntime(),
		listen: approvalstate.NewSandboxPortGrantRuntime(), loopback: approvalstate.NewSandboxPortGrantRuntime(),
	}
	broker := &WriteRootCheckpointBroker{Checkpoints: c, Runtime: c.write, ReadRuntime: c.read, Authority: authority}
	f := &capabilityReviewFixture{checkpoints: c, wantPaths: true, context: tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "cap-session",
			ProjectID:  "cap-project",
			ToolCallID: "cap-call",
			Agent:      "implement"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "root"},
	}, args: map[string]any{"command": "true", "capability_request": map[string]any{
		"write_root": write, "read_path": read, "socket_paths": []any{socket},
		"local_listen":     map[string]any{"ports": []any{8080}},
		"loopback_connect": map[string]any{"ports": []any{5432}},
	}}}
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register fixture command", registry.Register("command", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		f.invocations++
		request, reject := tools.ConfineRequestForSpawn(ctx, tc, broker.SessionWriteRoots(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID))
		if reject != nil {
			return "", reject
		}
		if len(request.SocketGrants) != 1 || !request.LocalListen || !request.LoopbackConnect {
			return "", fmt.Errorf("execution missed reviewed authority: %+v", request)
		}
		if f.wantPaths && (len(request.ProtectedReadGrants) != 1 || !slices.Contains(request.GrantedWriteRoots, write)) {
			return "", fmt.Errorf("execution missed reviewed paths: %+v", request)
		}
		return "verified", nil
	}))
	f.executor = toolexecution.NewExecutor(nil, registry, "implement")
	f.executor.Approvals.SetCheckpointManager(c, authority)
	f.executor.Capabilities.SetSocketCapabilityRuntime(c.sockets)
	f.executor.Boundary.SetSessionWriteRootOverlay(broker.SessionWriteRoots)
	f.executor.Boundary.SetSessionReadPathOverlay(broker.SessionReadPaths)
	f.executor.Boundary.SetWriteRootPreflight(func(ctx context.Context, tool string, _ map[string]any, tc tools.ToolContext, path string) (bool, bool, string, error) {
		result, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{SessionID: tc.Identity.SessionID, ProjectID: tc.Identity.ProjectID, ToolCallID: tc.Identity.ToolCallID, ProjectDir: tc.ActiveRootPath(), ToolName: tool, ProposedWriteRoot: path})
		return result.Authorized, result.Denied, result.UserGuidance, err
	})
	f.executor.Boundary.SetReadPathPreflight(func(ctx context.Context, tool string, _ map[string]any, tc tools.ToolContext, path string) (bool, bool, string, error) {
		result, err := broker.AuthorizeRead(ctx, command.SandboxReadPathAsk{SessionID: tc.Identity.SessionID, ProjectID: tc.Identity.ProjectID, ToolCallID: tc.Identity.ToolCallID, ProjectDir: tc.ActiveRootPath(), ToolName: tool, ProposedReadPath: path, ReadDenyPaths: []string{read}})
		return result.Authorized, result.Denied, result.UserGuidance, err
	})
	f.executor.Boundary.SetSessionListenGrant(func(_ context.Context, session, _ string) (bool, []uint16) {
		return c.listen.SessionPorts(session)
	})
	f.executor.Boundary.SetSessionLoopbackGrant(func(_ context.Context, session, _ string) (bool, []uint16) {
		return c.loopback.SessionPorts(session)
	})
	f.executor.Capabilities.SetLocalNetworkGate(&LocalNetworkCheckpointBroker{Checkpoints: c, Listen: c.listen, Loopback: c.loopback, Authority: authority})
	return f
}

func TestInvocationCapabilitiesShareOneReviewAndReuseAuthority(t *testing.T) {
	f := newCapabilityReviewFixture(t, true)
	_, err := f.executor.Invoke(t.Context(), "command", f.args, f.context)
	testutil.FailErr(t, "invoke combined capabilities", err)
	if len(f.checkpoints.requests) != 1 || f.invocations != 1 {
		t.Fatalf("reviews=%d executions=%d, want one each", len(f.checkpoints.requests), f.invocations)
	}
	plan := f.checkpoints.requests[0].ApprovalPlan
	for _, kind := range []string{"write_root", "read_path", "socket", "local_listen", "loopback_connect"} {
		if !slices.ContainsFunc(plan.Subject.Targets, func(target hitl.ApprovalTarget) bool { return target.Kind == kind }) {
			t.Errorf("combined review omitted %s", kind)
		}
	}
	f.context.Identity.ToolCallID = "next-call"
	_, err = f.executor.Invoke(t.Context(), "command", f.args, f.context)
	testutil.FailErr(t, "reuse combined authority", err)
	if len(f.checkpoints.requests) != 1 || f.invocations != 2 {
		t.Fatalf("reuse reviews=%d executions=%d", len(f.checkpoints.requests), f.invocations)
	}
}

func TestInvocationCapabilityRejectionNeverExecutesOrInstalls(t *testing.T) {
	f := newCapabilityReviewFixture(t, false)
	_, err := f.executor.Invoke(t.Context(), "command", f.args, f.context)
	if err == nil || f.invocations != 0 || len(f.checkpoints.requests) != 1 {
		t.Fatalf("denied review: err=%v executions=%d reviews=%d", err, f.invocations, len(f.checkpoints.requests))
	}
	if len(f.checkpoints.write.SessionWriteRoots(f.context.Identity.SessionID)) != 0 || len(f.checkpoints.read.SessionWriteRoots(f.context.Identity.SessionID)) != 0 || len(f.checkpoints.sockets.AppliedGrants(f.context.Identity.SessionID)) != 0 {
		t.Fatal("denied review installed authority")
	}
}

func TestInvocationCapabilityMissingInstallationStopsExecution(t *testing.T) {
	f := newCapabilityReviewFixture(t, true)
	f.checkpoints.omitRead = true
	_, err := f.executor.Invoke(t.Context(), "command", f.args, f.context)
	if err == nil || f.invocations != 0 || len(f.checkpoints.requests) != 1 {
		t.Fatalf("missing read grant: err=%v executions=%d reviews=%d", err, f.invocations, len(f.checkpoints.requests))
	}
}

func TestInvocationCapabilityOnceDoesNotAuthorizeLaterCalls(t *testing.T) {
	f := newCapabilityReviewFixture(t, true)
	f.checkpoints.once, f.wantPaths = true, false
	request := f.args["capability_request"].(map[string]any)
	delete(request, "write_root")
	delete(request, "read_path")
	for i := range 2 {
		f.context.Identity.ToolCallID = fmt.Sprintf("once-call-%d", i)
		_, err := f.executor.Invoke(t.Context(), "command", f.args, f.context)
		testutil.FailErr(t, "invoke with one-action authority", err)
		if len(f.checkpoints.requests) != i+1 || f.invocations != i+1 {
			t.Fatalf("one-action permission leaked or split: reviews=%d executions=%d", len(f.checkpoints.requests), f.invocations)
		}
	}
	if len(f.checkpoints.sockets.AppliedGrants(f.context.Identity.SessionID)) != 0 {
		t.Fatal("once installed a reusable socket grant")
	}
}

func TestInvocationCapabilityInvalidSocketDoesNotPromptForPaths(t *testing.T) {
	f := newCapabilityReviewFixture(t, true)
	f.args["capability_request"].(map[string]any)["socket_paths"] = []any{filepath.Join(t.TempDir(), "missing.sock")}
	_, err := f.executor.Invoke(t.Context(), "command", f.args, f.context)
	if err == nil || f.invocations != 0 || len(f.checkpoints.requests) != 0 {
		t.Fatalf("invalid socket: err=%v executions=%d reviews=%d", err, f.invocations, len(f.checkpoints.requests))
	}
	if len(f.checkpoints.write.SessionWriteRoots(f.context.Identity.SessionID)) != 0 || len(f.checkpoints.read.SessionWriteRoots(f.context.Identity.SessionID)) != 0 {
		t.Fatal("failed preparation installed path grants")
	}
}

func TestInvocationCapabilityChangedSocketStopsAfterReview(t *testing.T) {
	f := newCapabilityReviewFixture(t, true)
	request := f.args["capability_request"].(map[string]any)
	original := request["socket_paths"].([]any)[0].(string)
	link := filepath.Join(filepath.Dir(original), "current.sock")
	next := filepath.Join(filepath.Dir(original), "next.sock")
	listener, err := net.Listen("unix", next)
	testutil.FailErr(t, "listen on replacement socket", err)
	t.Cleanup(func() { testutil.FailErr(t, "close replacement socket", listener.Close()) })
	testutil.FailErr(t, "link original socket", os.Symlink(original, link))
	request["socket_paths"] = []any{link}
	f.checkpoints.afterApproval = func() {
		testutil.FailErr(t, "remove original socket link", os.Remove(link))
		testutil.FailErr(t, "retarget socket link", os.Symlink(next, link))
	}
	_, err = f.executor.Invoke(t.Context(), "command", f.args, f.context)
	if err == nil || f.invocations != 0 || len(f.checkpoints.requests) != 1 {
		t.Fatalf("changed socket: err=%v executions=%d reviews=%d", err, f.invocations, len(f.checkpoints.requests))
	}
}

func TestPreparedCapabilityDenialPreservesConcurrentCheckpoint(t *testing.T) {
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	path := filepath.Join(t.TempDir(), "shared")
	begin, _ := runtime.Begin("session", path, "other-call")
	if begin != approvalstate.SandboxAskMint {
		t.Fatalf("reserve concurrent checkpoint: %v", begin)
	}
	runtime.RegisterPending("session", path, "other-checkpoint")
	runtime.RecordDenied("session", path)
	runtime.ClearDenied("session", path)
	begin, checkpoint := runtime.Begin("session", path, "later-call")
	if begin != approvalstate.SandboxAskJoin || checkpoint != "other-checkpoint" {
		t.Fatalf("prepared denial lost concurrent checkpoint: %v %q", begin, checkpoint)
	}
}

func TestInvocationCapabilityOnceKeepsSecretReviewStable(t *testing.T) {
	f := newCapabilityReviewFixture(t, true)
	f.checkpoints.once, f.wantPaths = true, false
	request := f.args["capability_request"].(map[string]any)
	delete(request, "write_root")
	delete(request, "read_path")
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, database, f.context.Identity.ProjectID)
	testdbseed.InsertSession(t, database, f.context.Identity.SessionID, f.context.Identity.ProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets, Context: "capability review test",
	}, func(string) bool { return true })
	service := secretcap.NewWithStore(database, values, nil)
	meta, err := service.CreateSettingsSecret(t.Context(), secretcap.CreateSettingsSecretRequest{
		ProjectID: f.context.Identity.ProjectID, PersonID: testdbseed.OwnerID(t, database), OperationID: "fixture-secret", Name: "Service password",
		Purpose: "local service", Value: "capability-review-fixture-password",
	})
	testutil.FailErr(t, "create protected fixture value", err)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build fixture secret matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("h", 32)))
	testutil.FailErr(t, "build fixture fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)
	f.executor.Secrets.SetSecretResolver(service)
	f.executor.Secrets.SetSecretMatcher(matcher)
	f.args["command"] = "setup --password " + meta.Reference
	_, err = f.executor.Invoke(t.Context(), "command", f.args, f.context)
	testutil.FailErr(t, "invoke capabilities with one-action disclosure", err)
	if len(f.checkpoints.requests) != 1 || f.invocations != 1 {
		t.Fatalf("disclosure split capability review: reviews=%d executions=%d", len(f.checkpoints.requests), f.invocations)
	}
	if !slices.ContainsFunc(f.checkpoints.requests[0].ApprovalPlan.Subject.Targets, func(target hitl.ApprovalTarget) bool { return target.Kind == "secret" }) {
		t.Fatal("combined review omitted disclosure")
	}
}
