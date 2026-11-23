package commandprogram

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func fixtureInvocation() toolcontract.OperationInvocation {
	return toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "execution"}, ActorID: "actor", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project"}, SourceUnitID: "source", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
}

func TestOfferedOperationsValidateDefinitionAndInvocation(t *testing.T) {
	contract, _ := toolcontract.Lookup("write")
	definition, err := toolcontract.ToolOperation("write", json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "compile operation", err)
	invocation := fixtureInvocation()
	err = Offer().Validate(definition, invocation)
	var rejected *AdmissionError
	if !errors.As(err, &rejected) || rejected.Code != "TOOL_NOT_OFFERED" || rejected.Operation != "write" {
		t.Fatalf("unoffered operation error = %v", err)
	}
	offered := Offer(definition)
	testutil.FailErr(t, "validate offered operation", offered.Validate(definition, invocation))

	invalid := invocation
	invalid.ActorID = ""
	if err := offered.Validate(definition, invalid); err == nil {
		t.Fatal("accepted invocation without an actor")
	}
	wrongCatalog := invocation
	wrongCatalog.CatalogDigest = "different-catalog"
	if err := offered.Validate(definition, wrongCatalog); err == nil {
		t.Fatal("accepted invocation from a different catalog")
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
