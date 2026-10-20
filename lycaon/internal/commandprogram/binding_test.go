package commandprogram

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func fixtureInvocation() toolcontract.OperationInvocation {
	return toolcontract.OperationInvocation{Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "execution"}, ActorID: "actor", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project"}, SourceUnitID: "source", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
}

func TestUnofferedDirectBindingDoesNotExecute(t *testing.T) {
	contract, _ := toolcontract.Lookup("write")
	definition, err := toolcontract.ToolOperation("write", json.RawMessage(`{"type":"object"}`), contract)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	called := 0
	binding := Binding[string, string]{Definition: definition, InvokeOwner: func(context.Context, toolcontract.OperationInvocation, string) (string, error) {
		called++
		return "ok", nil
	}}
	_, err = Invoke(t.Context(), Offer(), binding, fixtureInvocation(), "input")
	var rejected *AdmissionError
	if !errors.As(err, &rejected) || rejected.Code != "TOOL_NOT_OFFERED" || called != 0 {
		t.Fatalf("unoffered execution: %v count=%d", err, called)
	}
	if got, err := Invoke(t.Context(), Offer(definition), binding, fixtureInvocation(), "input"); err != nil || got != "ok" || called != 1 {
		t.Fatalf("offered result=%s err=%v count=%d", got, err, called)
	}
	invalid := fixtureInvocation()
	invalid.ActorID = ""
	if _, err := Invoke(t.Context(), Offer(definition), binding, invalid, "input"); err == nil || called != 1 {
		t.Fatal("missing actor reached owner")
	}
	wrongCatalog := fixtureInvocation()
	wrongCatalog.CatalogDigest = "different-catalog"
	if _, err := Invoke(t.Context(), Offer(definition), binding, wrongCatalog, "input"); err == nil || called != 1 {
		t.Fatal("mismatched catalog reached owner")
	}
}

func TestRepeatedNodeHasFreshExecutionAndHeldResumeRetainsIdentity(t *testing.T) {
	parent := fixtureInvocation()
	first := NextExecution(parent, "loop-node", parent.Span)
	second := NextExecution(parent, "loop-node", parent.Span)
	if first.Identity.NodeID != second.Identity.NodeID || first.Identity.ExecutionID == second.Identity.ExecutionID || first.Identity.ParentExecutionID != parent.Identity.ExecutionID {
		t.Fatalf("dynamic identity collision: %+v %+v", first, second)
	}
	second.Scope.Directory = "different"
	if parent.Scope.Directory != "" || first.Scope.Directory != "" {
		t.Fatal("invocations alias mutable scope")
	}
}
