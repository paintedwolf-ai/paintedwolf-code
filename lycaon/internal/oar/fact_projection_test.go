package oar

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func bindNativeOperation(t *testing.T, operation string) toolcontract.BoundOperation {
	t.Helper()
	contract, ok := toolcontract.Lookup(operation)
	if !ok {
		t.Fatalf("%s contract absent", operation)
	}
	definition, err := toolcontract.ToolOperation(operation, json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "freeze "+operation, err)
	command := definition.Facts().Commands[0]
	mode := ""
	if len(command.Effects) > 0 {
		mode = command.Effects[0].Mode
	}
	inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, ActorID: "fixture-person", SessionID: "fixture-session", Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: operation + "-child"}, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, CatalogDigest: toolcontract.LanguageCatalogDigest(), SourceUnitID: "submitted", Span: toolcontract.SourceSpan{EndByte: 8, Line: 1, Column: 1}}
	bound, err := toolcontract.BindOperation(definition, inv, command.Name, mode, nil)
	testutil.FailErr(t, "bind "+operation, err)
	return bound
}

// TestCommandFormFollowsCanonicalNativeOperation pins the catalog-derived
// command spelling fact: it follows the observed native operation, is absent
// on tool legs and multi-form operations, and cannot be invented by
// diagnostic data.
func TestCommandFormFollowsCanonicalNativeOperation(t *testing.T) {
	gc := NewGuardContext()
	gc.ObserveToolCall("command", map[string]any{"command": "repo-map --bundle ssot_drift"})
	testutil.FailErr(t, "observe survey_repo", gc.ObserveNativeOperation(bindNativeOperation(t, "survey_repo")))
	facts := activation(gc)
	if facts["paintedwolf.command_form"] != "repo-map" {
		t.Fatalf("command_form = %#v", facts["paintedwolf.command_form"])
	}

	toolLeg := NewGuardContext()
	toolLeg.ObserveToolCall("survey_repo", map[string]any{"bundle": "ssot_drift"})
	toolFacts := activation(toolLeg)
	if _, present := toolFacts["paintedwolf.command_form"]; present {
		t.Fatal("tool leg invented a command form")
	}

	multiForm := NewGuardContext()
	multiForm.ObserveToolCall("command", map[string]any{"command": "tee out.txt"})
	testutil.FailErr(t, "observe write", multiForm.ObserveNativeOperation(bindNativeOperation(t, "write")))
	multiFacts := activation(multiForm)
	if _, present := multiFacts["paintedwolf.command_form"]; present {
		t.Fatal("multi-form operation invented a canonical command spelling")
	}

	spoof := NewGuardContext()
	spoof.ObserveToolCall("command", map[string]any{"command": "fixture"})
	spoof.PutRejectData("FIXTURE", map[string]any{"native_operation": "survey_repo"})
	spoofFacts := activation(spoof)
	if _, present := spoofFacts["paintedwolf.command_form"]; present {
		t.Fatal("diagnostic data invented a command form")
	}
}
