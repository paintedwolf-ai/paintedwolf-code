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
