package oar

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestNativeOperationObservationKeepsRealToolAndSelectorConjunction(t *testing.T) {
	contract, ok := toolcontract.Lookup("read")
	if !ok {
		t.Fatal("read contract absent")
	}
	definition, err := toolcontract.ToolOperation("read", json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "freeze actual operation catalog entry", err)
	inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, ActorID: "fixture-person", SessionID: "fixture-session", Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "iteration-two", ParentExecutionID: "loop"}, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, CatalogDigest: toolcontract.LanguageCatalogDigest(), SourceUnitID: "submitted", Span: toolcontract.SourceSpan{EndByte: 9, Line: 1, Column: 1}}
	operation, err := toolcontract.BindOperation(definition, inv, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind actual native operation", err)
	gc := NewGuardContext()
	args := map[string]any{"command": "cat input"}
	gc.ObserveToolCall("command", args)
	testutil.FailErr(t, "observe reached operation", gc.ObserveNativeOperation(operation))
	gc.PutRejectData("READ_PATH_DENIED", map[string]any{"tool": "read", "native_operation": "write"})
	facts := activation(gc)
	if facts["tool"] != "command" || facts["paintedwolf.native_operation"] != "read" || facts["tool_args"].(map[string]any)["command"] != "cat input" {
		t.Fatalf("presentation data replaced actual call or operation: %+v", facts)
	}
	if !selectorMatches(Selector{"tool": {"command"}, "paintedwolf.native_operation": {"read"}}, gc) {
		t.Fatal("explicit command and operation selector did not match")
	}
	if selectorMatches(Selector{"tool": {"read"}}, gc) || selectorMatches(Selector{"tool": {"write"}, "paintedwolf.native_operation": {"read"}}, gc) {
		t.Fatal("native operation impersonated old tool or changed selector AND semantics")
	}
	gc.ObserveToolCall("read", map[string]any{"path": "input"})
	if activation(gc)["paintedwolf.native_operation"] != "" {
		t.Fatal("subsequent actual structured call inherited a native operation")
	}
	if err := gc.ObserveNativeOperation(operation); err == nil {
		t.Fatal("structured call accepted a fabricated command observation")
	}
}

// This qualifies bundled rule selection and copy against bound operation facts.
// The metadata owner and connected command runtime qualify their effects apart.
func TestNativeChmodBundledPoliciesKeepActualToolAttribution(t *testing.T) {
	testNativeMetadataPolicies(t, "chmod", []string{"CHMOD_BULK_DENIED", "CHMOD_MODE_DENIED", "CHMOD_NOT_FOUND", "CHMOD_PATH_DENIED", "CHMOD_SPECIAL_BIT_DENIED", "CHMOD_SYMLINK_ESCAPE"}, true)
}

func TestNativeChownBundledPoliciesKeepNumericIdentityContract(t *testing.T) {
	testNativeMetadataPolicies(t, "chown", []string{"CHOWN_BULK_DENIED", "CHOWN_NOT_FOUND", "CHOWN_PATH_DENIED", "CHOWN_ROOT_DENIED", "CHOWN_SYMLINK_ESCAPE", "CHOWN_TARGET_DENIED", "CHOWN_UNSUPPORTED"}, true)
}

func TestChownNameLookupPoliciesRemainStructuredOnly(t *testing.T) {
	testNativeMetadataPolicies(t, "chown", []string{"CHOWN_GROUP_UNKNOWN", "CHOWN_USER_UNKNOWN", "CHOWN_RECURSIVE_DENIED"}, false)
}

func testNativeMetadataPolicies(t *testing.T, operationName string, codes []string, nativeEligible bool) {
	t.Helper()
	rs := loadStockRules(t)
	contract, ok := toolcontract.Lookup(operationName)
	if !ok {
		t.Fatal("chmod contract absent")
	}
	definition, err := toolcontract.ToolOperation(operationName, json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "freeze chmod definition", err)
	inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, ActorID: "fixture-person", SessionID: "fixture-session", Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "chmod-child"}, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, CatalogDigest: toolcontract.LanguageCatalogDigest(), SourceUnitID: "submitted", Span: toolcontract.SourceSpan{EndByte: 16, Line: 1, Column: 1}}
	operation, err := toolcontract.BindOperation(definition, inv, operationName, definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind chmod", err)
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			rule, ok := rs.Get(code)
			if !ok {
				t.Fatal("bundled chmod policy absent")
			}
			pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), nil, NewCounterStore())
			pipeline.EnableAnchor(AnchorToolRejected)
			for _, native := range []bool{false, true} {
				gc := NewGuardContext()
				if native {
					gc.ObserveToolCall("command", map[string]any{"command": operationName + " 1001 input"})
					testutil.FailErr(t, "observe bound chmod", gc.ObserveNativeOperation(operation))
				} else {
					gc.ObserveToolCall(operationName, map[string]any{"paths": []string{"input"}, "mode": "0644"})
				}
				gc.ObservedRejectCode = code
				gc.PutRejectData(code, map[string]any{"path": "input", "mode": "0644", "requested": 9, "max_paths": 8, "bulk_denied": true, "mode_denied": true, "chmod_allowed_modes": []string{"0600", "0644"}, "ownership_uid": 1001, "ownership_gid": 1001, "ownership_target": "1002"})
				result, err := pipeline.EvaluateBlock(t.Context(), AnchorToolRejected, gc)
				testutil.FailErr(t, "evaluate bundled chmod policy", err)
				if native && !nativeEligible {
					if result.Decision != nil {
						t.Fatal("native numeric-only grammar selected legacy account lookup or recursive request policy")
					}
					continue
				}
				if result.Decision == nil || result.Decision.Code != code || result.Decision.Copy == nil {
					t.Fatalf("native=%v lost bundled rejection: %+v", native, result)
				}
				if native && gc.Invocation.Tool != "command" {
					t.Fatal("policy selection changed actual tool identity")
				}
			}
			// A command name or rejection data alone cannot impersonate a bound
			// operation, and the right operation cannot override another code.
			gc := NewGuardContext()
			gc.ObserveToolCall("command", map[string]any{"command": operationName + " 1001 input"})
			gc.ObservedRejectCode = code
			gc.PutRejectData(code, map[string]any{"native_operation": operationName})
			result, err := pipeline.EvaluateBlock(t.Context(), AnchorToolRejected, gc)
			testutil.FailErr(t, "evaluate unbound command", err)
			if result.Decision != nil {
				t.Fatal("command text or diagnostic data selected chmod policy")
			}
			testutil.FailErr(t, "observe actual chmod", gc.ObserveNativeOperation(operation))
			gc.ObservedRejectCode = "OTHER_OWNER_FAILURE"
			gc.PutRejectData("OTHER_OWNER_FAILURE", nil)
			result, err = pipeline.EvaluateBlock(t.Context(), AnchorToolRejected, gc)
			testutil.FailErr(t, "evaluate different owner code", err)
			if result.Decision != nil {
				t.Fatal("operation attribution overrode owner rejection code")
			}
		})
	}
}
