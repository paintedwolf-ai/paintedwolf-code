package tools

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// catalogContract freezes a catalog contract the way dispatch does.
func catalogContract(t *testing.T, tool string) toolcontract.Contract {
	t.Helper()
	contract, ok := toolcontract.Lookup(tool)
	if !ok {
		t.Fatalf("%s has no catalog contract", tool)
	}
	return contract
}

func invokedAs(contract toolcontract.Contract) ToolContext {
	return ToolContext{Invocation: Invocation{Contract: contract}}
}

func TestSecretReferenceResolutionFollowsTheDeclaredSurface(t *testing.T) {
	t.Parallel()
	const reference = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"
	accepting := toolcontract.SecretReferenceTools()
	if len(accepting) == 0 {
		t.Fatal("no catalog tool declares secret_reference_surface")
	}
	mcp := toolcontract.External("mcp:fixture")
	mcp.SecretReferenceSurface = toolcontract.SecretSurfaceMCP
	contracts := map[string]toolcontract.Contract{"mcp_fixture_post": mcp, "undeclared_dynamic": toolcontract.External("fixture")}
	for _, tool := range []string{"read", "wait", "secret_generate", "secret_list", "secret_revoke"} {
		contracts[tool] = catalogContract(t, tool)
	}
	for _, tool := range accepting {
		contracts[tool] = catalogContract(t, tool)
	}
	for tool, contract := range contracts {
		args := map[string]any{"value": reference}
		if len(contract.SecretReferenceArgs) > 0 {
			args = map[string]any{contract.SecretReferenceArgs[0]: reference}
		}
		_, err := (&DefaultToolExecutor{}).resolveSecretReferences(t.Context(), tool, args, invokedAs(contract))
		if contract.AcceptsSecretReferences() != errors.Is(err, errSecretReferenceUnavailable) {
			t.Errorf("%s: declared surface %q, resolution error %v", tool, contract.SecretReferenceSurface, err)
		}
	}
	for _, tool := range []string{"read", "wait", "secret_generate", "secret_list", "secret_revoke"} {
		if contracts[tool].AcceptsSecretReferences() {
			t.Errorf("%s must keep references as literal text", tool)
		}
	}
}

// A damaged token is refused before resolution on every accepting tool, and
// stays literal text on tools that never resolve references.
func TestMalformedSecretReferenceIsRefusedWhereReferencesResolve(t *testing.T) {
	t.Parallel()
	const damaged = "{{paintedwolf-secret:[REDACTED]}}"
	for _, tool := range toolcontract.SecretReferenceTools() {
		contract := catalogContract(t, tool)
		args := map[string]any{"env": map[string]any{"KEYCLOAK_CLIENT_SECRET": damaged}}
		if len(contract.SecretReferenceArgs) > 0 {
			args = map[string]any{contract.SecretReferenceArgs[0]: damaged}
		}
		_, err := (&DefaultToolExecutor{}).resolveSecretReferences(t.Context(), tool,
			args, invokedAs(contract))
		if !errors.Is(err, errSecretReferenceMalformed) {
			t.Errorf("%s: resolution error %v, want malformed", tool, err)
			continue
		}
		if reject := secretReferenceReject(err); reject.Code != "SECRET_REFERENCE_MALFORMED" || !reject.Retryable {
			t.Errorf("%s: reject = %+v", tool, reject)
		}
	}
	args := map[string]any{"content": damaged, "resolve_secret_references": false}
	got, err := (&DefaultToolExecutor{}).resolveSecretReferences(t.Context(), "write", args, invokedAs(catalogContract(t, "write")))
	testutil.FailErr(t, "write with resolve_secret_references: false keeps reference text literal", err)
	if !reflect.DeepEqual(got.Arguments, args) {
		t.Fatalf("write arguments changed: %#v", got.Arguments)
	}
}

func TestSecretSurfacesAreScreenSurfaces(t *testing.T) {
	t.Parallel()
	for declared, screen := range map[toolcontract.SecretSurface]secretmatch.ScreenSurface{
		toolcontract.SecretSurfaceCommand:     secretmatch.SurfaceCommand,
		toolcontract.SecretSurfaceTerminal:    secretmatch.SurfaceTerminal,
		toolcontract.SecretSurfaceHTTPRequest: secretmatch.SurfaceHTTPRequest,
		toolcontract.SecretSurfaceMCP:         secretmatch.SurfaceMCP,
		toolcontract.SecretSurfaceFile:        secretmatch.SurfaceFile,
	} {
		if secretmatch.ScreenSurface(declared) != screen {
			t.Errorf("declared surface %q names screen %q", declared, screen)
		}
	}
}

func TestSecretRevokeKeepsReferenceUnresolved(t *testing.T) {
	t.Parallel()
	const reference = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"
	args := map[string]any{"reference": reference}
	got, err := (&DefaultToolExecutor{}).resolveSecretReferences(
		context.Background(), "secret_revoke", args, invokedAs(catalogContract(t, "secret_revoke")),
	)
	if err != nil {
		t.Fatalf("resolve secret_revoke reference: %v", err)
	}
	if got.Arguments["reference"] != reference {
		t.Fatalf("reference = %v, want unresolved identity", got.Arguments["reference"])
	}
}

func TestLiteralReferenceTextDoesNotNeedAResolver(t *testing.T) {
	const reference = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"
	for _, tool := range []string{"read", "wait", "secret_revoke"} {
		args := map[string]any{"content": reference, reference: "literal key"}
		got, err := (&DefaultToolExecutor{}).resolveSecretReferences(t.Context(), tool, args, invokedAs(catalogContract(t, tool)))
		testutil.FailErr(t, "carry literal reference through "+tool, err)
		if !reflect.DeepEqual(got.Arguments, args) {
			t.Fatalf("%s changed literal reference text", tool)
		}
	}
	// File-writing tools carry literal reference text when resolve_secret_references is false.
	for _, tool := range []string{"write", "edit", "replace_lines", "jq_edit"} {
		args := map[string]any{"content": reference, "old_string": reference, "new_string": reference, "resolve_secret_references": false}
		got, err := (&DefaultToolExecutor{}).resolveSecretReferences(t.Context(), tool, args, invokedAs(catalogContract(t, tool)))
		testutil.FailErr(t, "carry literal reference through "+tool+" with resolve_secret_references: false", err)
		if !reflect.DeepEqual(got.Arguments, args) {
			t.Fatalf("%s changed literal reference text", tool)
		}
	}
	mcp := toolcontract.External("mcp:test")
	mcp.SecretReferenceSurface = toolcontract.SecretSurfaceMCP
	outbound := map[string]toolcontract.Contract{"mcp_test_post": mcp}
	for _, tool := range toolcontract.SecretReferenceTools() {
		outbound[tool] = catalogContract(t, tool)
	}
	for tool, contract := range outbound {
		args := map[string]any{"value": "{{secret:example}}", reference: "literal key"}
		if len(contract.SecretReferenceArgs) > 0 {
			args = map[string]any{contract.SecretReferenceArgs[0]: "{{secret:example}}", reference: "literal key"}
		}
		got, err := (&DefaultToolExecutor{}).resolveSecretReferences(t.Context(), tool, args, invokedAs(contract))
		testutil.FailErr(t, "carry other markup through "+tool, err)
		if !reflect.DeepEqual(got.Arguments, args) {
			t.Fatalf("%s changed literal markup", tool)
		}
		// The reference marker is reserved where references resolve.
		for _, damaged := range []string{"{{paintedwolf-secret:", "{{paintedwolf-secret:example}}"} {
			damagedArgs := map[string]any{"value": damaged}
			if len(contract.SecretReferenceArgs) > 0 {
				damagedArgs = map[string]any{contract.SecretReferenceArgs[0]: damaged}
			}
			_, err := (&DefaultToolExecutor{}).resolveSecretReferences(t.Context(), tool, damagedArgs, invokedAs(contract))
			if !errors.Is(err, errSecretReferenceMalformed) {
				t.Fatalf("%s: %q resolution error %v, want malformed", tool, damaged, err)
			}
		}
	}
}

func TestOutboundReferenceResolutionKeepsPolicyAndExecutionCopiesSeparate(t *testing.T) {
	const name = "mcp_test_post"
	const reference = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"
	args := map[string]any{"body": "Bearer " + reference}
	policy := &capturePolicy{}
	outboundContract := toolcontract.External("mcp:test")
	outboundContract.SecretReferenceSurface = toolcontract.SecretSurfaceMCP
	registry := NewDefaultRegistry()
	var handled bool
	testutil.FailErr(t, "register outbound handler", registry.RegisterDefinition(Definition{
		Meta:     ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object"}},
		Contract: outboundContract,
		Handler: func(_ context.Context, execution map[string]any, tc ToolContext) (string, error) {
			handled = true
			if execution["body"] != "Bearer protected-value" {
				t.Fatal("handler did not receive resolved execution arguments")
			}
			if tc.CanonicalArgs["body"] != args["body"] || policy.eval.ToolArgs["body"] != args["body"] {
				t.Fatal("protected bytes reached canonical or policy arguments")
			}
			return "ok", nil
		},
	}))
	executor := NewDefaultToolExecutor(policy, registry, "implement")
	executor.secretResolver = func(_ context.Context, canonical map[string]any, access secretcap.ResolveContext) (*secretcap.Resolution, error) {
		if policy.eval.ToolName != "" {
			t.Fatal("secret snapshot must precede the composed policy review")
		}
		if canonical["body"] != args["body"] || access.ProjectID != "project" || access.ChatSessionID != "root-chat" || access.SessionID != "worker" {
			t.Fatalf("resolution lost canonical arguments or invocation scope: %+v", access)
		}
		return &secretcap.Resolution{Arguments: map[string]any{"body": "Bearer protected-value"}}, nil
	}
	_, err := executor.Invoke(t.Context(), name, args, ToolContext{ProjectID: "project", SessionID: "worker", ParentSessionID: "root-chat", ToolCallID: "call"})
	testutil.FailErr(t, "invoke outbound tool", err)
	if !handled {
		t.Fatal("outbound handler was not invoked")
	}
	if args["body"] != "Bearer "+reference {
		t.Fatal("caller arguments changed")
	}
}
