package toolprofiles

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func boundProfileFixture(t *testing.T, identity, command string) toolcontract.BoundOperation {
	t.Helper()
	contract, ok := toolcontract.Lookup(identity)
	if !ok {
		t.Fatalf("missing real owner %s", identity)
	}
	definition, err := toolcontract.ToolOperation(identity, json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "compile actual native declaration", err)
	mode := ""
	for _, form := range definition.Facts().Commands {
		if form.Name == command {
			mode = form.Effects[0].Mode
		}
	}
	invocation := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "child"}, ActorID: "host-person", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "source", Span: toolcontract.SourceSpan{Line: 2, Column: 3}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	bound, err := toolcontract.BindOperation(definition, invocation, command, mode, nil)
	testutil.FailErr(t, "bind actual native effect", err)
	return bound
}

func TestNativeProfilePolicyDoesNotRequireRetiredStructuredAllowlist(t *testing.T) {
	readonly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: readonly}})
	policy := NewProfilePolicyEngine(boundary)
	read := boundProfileFixture(t, "read", "cat")
	eval := platform.PolicyContext{ProfileID: "fixture", ToolName: "read", SessionID: "session", ActionID: "child", NativeOperation: &read}
	decision, err := policy.Evaluate(t.Context(), eval)
	testutil.FailErr(t, "evaluate explicit native binding", err)
	if decision.Blocked || !decision.Allowed {
		t.Fatalf("native binding inherited retired JSON allowlist: %+v", decision)
	}
	eval.NativeOperation = nil
	decision, err = policy.Evaluate(t.Context(), eval)
	testutil.FailErr(t, "evaluate retained interface independently", err)
	if !decision.Blocked {
		t.Fatal("native selection widened retained structured policy")
	}
	write := boundProfileFixture(t, "write", "tee")
	eval.NativeOperation = &write
	eval.ToolName = "write"
	decision, err = policy.Evaluate(t.Context(), eval)
	testutil.FailErr(t, "evaluate bound write restriction", err)
	if !decision.Blocked || decision.RejectCode != "COMMAND_EFFECT_NOT_ALLOWED" || decision.RejectData["source_line"] != 2 || decision.RejectData["owner_ref"] != write.Definition().Facts().Contract.Owner {
		t.Fatalf("native refusal lost actual owner/source: %+v", decision)
	}
	eval.SessionID = "other-session"
	if _, err := policy.Evaluate(t.Context(), eval); err == nil {
		t.Fatal("cross-session native policy accepted")
	}
}

func TestNativeProfilePolicyRetainsExplicitDenies(t *testing.T) {
	readonly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	policy := NewProfilePolicyEngine(sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: readonly, DenyTools: []string{"read"}}}))
	read := boundProfileFixture(t, "read", "cat")
	decision, err := policy.Evaluate(t.Context(), platform.PolicyContext{ProfileID: "fixture", ToolName: "read", SessionID: "session", ActionID: "child", NativeOperation: &read})
	testutil.FailErr(t, "evaluate native deny", err)
	if !decision.Blocked || decision.RejectCode != "COMMAND_OPERATION_NOT_ALLOWED" {
		t.Fatalf("native exposure bypassed explicit deny: %+v", decision)
	}
}
