package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"github.com/lycaon/lycaon/internal/tools"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestSavedSocketAuthorityRequiresInvocationDeclaration(t *testing.T) {
	dir := shortTempDir(t)
	paths := []string{filepath.Join(dir, "one.sock"), filepath.Join(dir, "two.sock")}
	for _, path := range paths {
		_ = listenUnixSocket(t, path)
	}
	grants, reject := capabilityrequest.ResolveCapabilitySockets(&capabilityrequest.CapabilityRequest{SocketPaths: paths})
	if reject != nil {
		t.Fatalf("resolve fixture sockets: %v", reject)
	}
	rt := &memorySocketRuntime{task: grants[:1]}
	executor := func() *Executor {
		e := NewExecutor(nil, nil, "")
		e.Capabilities.socketRuntime = rt
		e.Capabilities.durableSockets = func(string) []confine.SocketGrant { return grants[1:] }
		return e
	}()
	tc := tools.ToolContext{SessionID: "task", ToolCallID: "call", ProjectID: "project",
		Roots: []projectroot.RootRef{{ID: "root", Path: dir, IsPrimary: true}}, ActiveRootID: "root",
		Invocation: tools.Invocation{Contract: toolcontract.Contract{Capabilities: toolcontract.CapabilitySocket}},
	}
	result, err := executor.Capabilities.preflightSocketCapability(t.Context(), "command", map[string]any{"command": "pwd"}, tc)
	testutil.FailErr(t, "preflight unrelated command", err)
	if result != nil {
		t.Fatalf("unrelated command inherited sockets: %+v", result)
	}
	for _, path := range paths {
		args := map[string]any{"command": "client", "capability_request": map[string]any{"socket_paths": []any{path}}}
		result, err = executor.Capabilities.preflightSocketCapability(t.Context(), "command", args, tc)
		testutil.FailErr(t, "reuse declared socket", err)
		if result == nil || len(result.SocketGrants) != 1 || result.SocketGrants[0].ApprovedPath != path {
			t.Fatalf("declaring %s activated the wrong sockets: %+v", path, result)
		}
	}
	if len(rt.task) != 1 {
		t.Fatal("invocation narrowing changed the saved grant")
	}
}
