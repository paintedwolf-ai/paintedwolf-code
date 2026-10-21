package toolcontract

import (
	"encoding/json"
	"testing"
)

func TestOperationDefinitionIsDetachedAndUsesExistingContract(t *testing.T) {
	contract, _ := Lookup("write")
	contract.SecretReferenceArgs = []string{"content"}
	schema := json.RawMessage(`{"type":"object","required":["path","content"]}`)
	definition, err := ToolOperation("write", schema, contract)
	if err != nil {
		t.Fatalf("compile write: %v", err)
	}
	contract.SecretReferenceArgs[0] = "changed"
	schema[2] = 'X'
	facts := definition.Facts()
	if facts.Contract.Owner != "filesystem" || facts.Contract.Lifecycle != contract.Lifecycle || facts.Contract.SecretReferenceArgs[0] != "content" || definition.Digest() == "" {
		t.Fatalf("lost owner contract: %+v", facts.Contract)
	}
	facts.Resources[0].Maximum = -1
	facts.Streams.Statuses[0] = "changed"
	if again := definition.Facts(); again.Resources[0].Maximum < 0 || again.Streams.Statuses[0] == "changed" {
		t.Fatal("definition shares mutable data")
	}
	for _, invalid := range []OperationFacts{{}, {Identity: "write", Version: 1, Contract: contract, Arguments: json.RawMessage(`{"type":"string"}`), ProgressGate: ProgressExistingAdmission, RejectionContract: "write"}} {
		if _, err := NewOperationDefinition(invalid); err == nil {
			t.Fatal("invalid definition admitted")
		}
	}
	for _, schema := range []string{`{"type":"object","required":"path"}`, `{"type":"object","properties":{"path":17}}`} {
		invalid := definition.Facts()
		invalid.Arguments = json.RawMessage(schema)
		if _, err := NewOperationDefinition(invalid); err == nil {
			t.Fatalf("invalid schema admitted: %s", schema)
		}
	}
}

func TestProducerPublicationRequiresCompleteEligibleOutcome(t *testing.T) {
	completeNoMatch := ProducerResult{Status: 1, Terminal: ProducerCompleted, Complete: true, PublicationEligible: true, PublicationBasis: NativeComparisonPublication, EffectCertainty: NoEffect}
	if err := completeNoMatch.Validate(); err != nil {
		t.Fatalf("status one complete result: %v", err)
	}
	if (ProducerResult{Terminal: ProducerCompleted, EffectCertainty: "invented"}).Validate() == nil {
		t.Fatal("unclassified effect certainty admitted")
	}
	for _, result := range []ProducerResult{
		{Status: 0, Terminal: ProducerCompleted, PublicationEligible: true},
		{Status: 0, Terminal: ProducerInterrupted, Complete: true, PublicationEligible: true},
		{Status: 0, Terminal: ProducerCompleted, Complete: true, PublicationEligible: true, StopCode: "COMMAND_LIMIT_EXCEEDED"},
		{Status: 1, Terminal: ProducerCompleted, Complete: true, PublicationEligible: true},
		{Status: -1, Terminal: ProducerCompleted}, {Terminal: "invented"},
	} {
		result.EffectCertainty = NoEffect
		if result.Validate() == nil {
			t.Fatalf("invalid producer admitted: %+v", result)
		}
	}
}

func TestRetainedOperationCatalogClosesOverTypedCommandReferences(t *testing.T) {
	catalog, err := LanguageV1()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	for _, row := range catalog.Tools {
		contract, ok := Lookup(row.Tool)
		if !ok {
			t.Fatalf("missing owner declaration: %s", row.Tool)
		}
		definition, err := ToolOperation(row.Tool, json.RawMessage(`{"type":"object"}`), contract)
		if err != nil {
			t.Fatalf("compile %s: %v", row.Tool, err)
		}
		facts := definition.Facts()
		if facts.Contract.Owner != row.Owner || facts.RejectionContract != row.Tool || facts.Identity != row.Tool || facts.CatalogDigest != LanguageCatalogDigest() || len(facts.Commands) != len(row.CommandForms) {
			t.Fatalf("operation closure lost for %s", row.Tool)
		}
	}
}

func TestInvocationScopeAdmitsExplicitProjectlessSession(t *testing.T) {
	invocation := OperationInvocation{Kind: ProgramChildInvocation, Identity: ExecutionIdentity{ProgramID: "p", NodeID: "n", ExecutionID: "e"}, ActorID: "actor", SessionID: "s", Scope: OperationScope{Kind: SessionOperationScope}, SourceUnitID: "source", Span: SourceSpan{Line: 1, Column: 1}, CatalogDigest: LanguageCatalogDigest()}
	if err := invocation.Validate(); err != nil {
		t.Fatalf("projectless explicit session refused: %v", err)
	}
	for _, scope := range []OperationScope{{}, {Kind: "invented"}, {Kind: ProjectOperationScope}, {Kind: SessionOperationScope, ProjectID: "project"}, {Kind: SessionOperationScope, RootID: "root"}, {Kind: SessionOperationScope, BranchID: "branch"}} {
		invocation.Scope = scope
		if invocation.Validate() == nil {
			t.Fatalf("contradictory scope admitted: %+v", scope)
		}
	}
}

func TestStructuredInvocationUsesActualCallWithoutProgramIdentity(t *testing.T) {
	in := OperationInvocation{Kind: StructuredToolInvocation, ModelToolCallID: "actual-model-call", ActorID: "actor", SessionID: "session", Scope: OperationScope{Kind: SessionOperationScope}, CatalogDigest: LanguageCatalogDigest()}
	if err := in.Validate(); err != nil {
		t.Fatalf("retained structured context refused: %v", err)
	}
	for _, mutate := range []func(*OperationInvocation){
		func(i *OperationInvocation) { i.ModelToolCallID = "" },
		func(i *OperationInvocation) { i.Identity.ProgramID = "invented" },
		func(i *OperationInvocation) { i.SourceUnitID = "invented" },
		func(i *OperationInvocation) { i.Span.Line = 1 },
		func(i *OperationInvocation) { i.Kind = ProgramChildInvocation },
	} {
		invalid := in
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatalf("contradictory invocation admitted: %+v", invalid)
		}
	}
}
