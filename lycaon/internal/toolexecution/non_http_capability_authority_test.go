package toolexecution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// capabilityAuthorityExecutor registers a stub for each tool that records
// whether dispatch reached it. No checkpoint manager is wired, so any card the
// invocation tries to raise fails the call.
func capabilityAuthorityExecutor(t *testing.T, toolNames ...string) (*Executor, map[string]bool) {
	t.Helper()
	registry := tools.NewDefaultRegistry()
	entered := map[string]bool{}
	for _, name := range toolNames {
		testutil.FailErr(t, "register "+name, registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			entered[name] = true
			return "ok", nil
		}))
	}
	return NewExecutor(nil, registry, "implement"), entered
}

func capabilityAuthorityContext(t *testing.T, tool, root string) tools.ToolContext {
	t.Helper()
	return tools.ToolContext{
		Invocation: tools.Invocation{Contract: catalogContract(t, tool)},
		Identity:   tools.InvocationIdentity{SessionID: "task", ToolCallID: "call", ProjectID: "project", Agent: "implement"},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root",
		},
	}
}

// A socket-only tool refuses a direct-IP capability request against its
// contract before dispatch; a tool that declares direct IP does not.
func TestSocketOnlyToolRefusesDirectIPRequest(t *testing.T) {
	ctx := socketInvocationContext(t)
	dir := shortTempDir(t)
	executor, entered := capabilityAuthorityExecutor(t, "terminal_open", "command")
	args := map[string]any{"command": "client", "capability_request": map[string]any{"direct_ip": true}}

	_, err := executor.Invoke(ctx, "terminal_open", args, capabilityAuthorityContext(t, "terminal_open", dir))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != isolation.CodeCapabilityRequestInvalid || reject.Data["capability"] != "direct_ip" {
		t.Fatalf("terminal_open with direct_ip = %v, want %s for direct_ip", err, isolation.CodeCapabilityRequestInvalid)
	}
	if entered["terminal_open"] {
		t.Fatal("refused terminal_open reached dispatch")
	}

	_, err = executor.Invoke(ctx, "command", args, capabilityAuthorityContext(t, "command", dir))
	if reject := toolrejection.AsToolReject(err); reject != nil && reject.Code == isolation.CodeCapabilityRequestInvalid {
		t.Fatalf("command declares direct IP but was refused against its contract: %+v", reject)
	}
}

// With approvals disabled a socket request raises no card, issues the
// current-call permit and records a never_ask decision; an invalid socket path
// is still refused. With approvals enabled the same request needs a card.
func TestNeverAskStandsDownSocketCardButKeepsValidationAndRecord(t *testing.T) {
	ctx := socketInvocationContext(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "daemon.sock")
	_ = listenUnixSocket(t, sock)
	request := func(path string) map[string]any {
		return map[string]any{"command": "client", "capability_request": map[string]any{"socket_paths": []any{path}}}
	}

	asking, askingEntered := capabilityAuthorityExecutor(t, "command")
	asking.Capabilities.SetSocketCapabilityRuntime(&memorySocketRuntime{})
	if _, err := asking.Invoke(ctx, "command", request(sock), capabilityAuthorityContext(t, "command", dir)); err == nil || askingEntered["command"] {
		t.Fatalf("socket request with approvals enabled ran without a card: err=%v", err)
	}

	executor, entered := capabilityAuthorityExecutor(t, "command")
	runtime := &memorySocketRuntime{}
	recorder := &capabilityRecorder{}
	executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	executor.Capabilities.SetApprovalsDisabled(func(string) bool { return true })
	executor.Approvals.SetAuthzRecorder(recorder)

	_, err := executor.Invoke(ctx, "command", request(sock), capabilityAuthorityContext(t, "command", dir))
	testutil.FailErr(t, "never_ask socket invocation", err)
	if !entered["command"] {
		t.Fatal("never_ask socket invocation did not dispatch")
	}
	if len(runtime.permits) != 1 {
		t.Fatalf("never_ask issued %d current-call permits, want 1", len(runtime.permits))
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityGranted, authzledger.AuthorizationSourceNeverAsk)

	entered["command"] = false
	_, err = executor.Invoke(ctx, "command", request(filepath.Join(dir, "missing.sock")), capabilityAuthorityContext(t, "command", dir))
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != isolation.CodeSocketPathNotFound {
		t.Fatalf("missing socket under never_ask = %v, want %s", err, isolation.CodeSocketPathNotFound)
	}
	if entered["command"] {
		t.Fatal("invalid socket request reached dispatch under never_ask")
	}
}
