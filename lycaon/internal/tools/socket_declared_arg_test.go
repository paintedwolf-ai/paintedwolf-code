package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func httpRequestSocketContext(t *testing.T, dir string) ToolContext {
	t.Helper()
	contract, ok := toolcontract.Lookup("http_request")
	if !ok || contract.SocketArg == "" {
		t.Fatalf("http_request contract declares no socket argument: %+v", contract)
	}
	return ToolContext{SessionID: "task", ToolCallID: "call", ProjectID: "project",
		Roots: []projectroot.RootRef{{ID: "root", Path: dir, IsPrimary: true}}, ActiveRootID: "root",
		Invocation: Invocation{Contract: contract},
	}
}

func TestDeclaredSocketArgumentUsesExistingGrantWithoutAsking(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "daemon.sock")
	_ = listenUnixSocket(t, sock)
	grant, err := confine.ResolveSocketRequest(sock)
	testutil.FailErr(t, "resolve socket", err)
	executor := &DefaultToolExecutor{socketRuntime: &memorySocketRuntime{task: []confine.SocketGrant{grant}}}
	tc := httpRequestSocketContext(t, dir)

	result, err := executor.preflightSocketCapability(t.Context(), "http_request", map[string]any{"url": "http://localhost/", "unix_socket": sock}, tc)
	testutil.FailErr(t, "preflight granted socket", err)
	if result == nil || len(result.SocketGrants) != 1 || result.SocketGrants[0] != grant {
		t.Fatalf("preflight = %+v, want the granted socket", result)
	}
	tc.SocketGrants, tc.SocketCapabilityRuntime = result.SocketGrants, executor.socketRuntime
	claimed, reject := ClaimDeclaredSocket(t.Context(), tc, sock)
	if reject != nil || claimed.ResolvedPath != grant.ResolvedPath {
		t.Fatalf("claim = %+v %v", claimed, reject)
	}
	if _, reject := ClaimDeclaredSocket(t.Context(), tc, filepath.Join(dir, "other.sock")); reject == nil {
		t.Fatal("an unreviewed socket was claimable")
	}
}

func TestSocketPreflightWorkerChildFindsRootLease(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "daemon.sock")
	_ = listenUnixSocket(t, sock)
	grant, err := confine.ResolveSocketRequest(sock)
	testutil.FailErr(t, "resolve socket", err)

	rt := &memorySocketRuntime{task: []confine.SocketGrant{grant}}
	executor := &DefaultToolExecutor{socketRuntime: rt}

	tc := httpRequestSocketContext(t, dir)
	tc.SessionID = "worker-child-session"
	tc.RootSessionID = "root-chat-session"

	result, err := executor.preflightSocketCapability(t.Context(), "http_request", map[string]any{"url": "http://localhost/", "unix_socket": sock}, tc)
	testutil.FailErr(t, "preflight worker child", err)
	if result == nil || len(result.SocketGrants) != 1 || result.SocketGrants[0] != grant {
		t.Fatalf("worker child preflight = %+v, want the root's granted socket", result)
	}
}
