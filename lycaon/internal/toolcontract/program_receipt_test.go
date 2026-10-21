package toolcontract

import (
	"encoding/json"
	"strings"
	"testing"
)

func receiptVersion() ProgramVersion {
	return ProgramVersion{ProgramID: "program", ParentReceiptID: "receipt", RootExecutionID: "root", LanguageVersion: LanguageVersion, CatalogDigest: LanguageCatalogDigest(), SourceUnitID: "unit", SourceDigest: strings.Repeat("a", 64), SourceReference: "source-ref"}
}

func TestProgramVersionBindsFrozenCatalogAndExactSource(t *testing.T) {
	if err := receiptVersion().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProgramVersion){
		"parent":        func(v *ProgramVersion) { v.ParentReceiptID = "" },
		"language":      func(v *ProgramVersion) { v.LanguageVersion++ },
		"catalog":       func(v *ProgramVersion) { v.CatalogDigest = "other" },
		"digest length": func(v *ProgramVersion) { v.SourceDigest = "abc" },
		"digest hex":    func(v *ProgramVersion) { v.SourceDigest = strings.Repeat("z", 64) },
	} {
		version := receiptVersion()
		mutate(&version)
		if version.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProgramChildStartRequiresFrozenDefinitionAndInputs(t *testing.T) {
	memory, err := NewProgramMemory(ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	inputs, release, err := NewReviewedOperationInputs(t.Context(), memory, map[string]any{"path": "a"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	contract, _ := Lookup("read")
	definition, err := ToolOperation("read", json.RawMessage(`{"type":"object"}`), contract)
	if err != nil {
		t.Fatal(err)
	}
	invocation := OperationInvocation{Kind: ProgramChildInvocation, Identity: ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "execution"}, ActorID: "actor", SessionID: "session", Scope: OperationScope{Kind: ProjectOperationScope, ProjectID: "project"}, SourceUnitID: "unit", Span: SourceSpan{Line: 1, Column: 1}, CatalogDigest: LanguageCatalogDigest()}
	valid := ProgramChildStart{ParentReceiptID: "receipt", Invocation: invocation, Definition: definition, ArgumentsReference: "args", ExpandedInputs: inputs}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProgramChildStart){
		"inputs":     func(c *ProgramChildStart) { c.ExpandedInputs = ReviewedOperationInputs{} },
		"parent":     func(c *ProgramChildStart) { c.ParentReceiptID = "" },
		"kind":       func(c *ProgramChildStart) { c.Invocation.Kind = StructuredToolInvocation },
		"invocation": func(c *ProgramChildStart) { c.Invocation.ActorID = "" },
		"definition": func(c *ProgramChildStart) { c.Definition = OperationDefinition{} },
		"reference":  func(c *ProgramChildStart) { c.ArgumentsReference = "" },
	} {
		child := valid
		mutate(&child)
		if child.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProgramSettlementsKeepOwnerFacts(t *testing.T) {
	completed := ProducerResult{Status: 0, Terminal: ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: NoEffect}
	if err := (ProgramChildSettlement{Result: completed, OutputPublishable: true, DetailReference: "detail"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, settlement := range map[string]ProgramChildSettlement{
		"invalid result":   {Result: ProducerResult{Status: -1}, DetailReference: "detail"},
		"stopped publish":  {Result: completed, OutputPublishable: true, HostStopCode: "APPROVAL_DENIED", DetailReference: "detail"},
		"missing detail":   {Result: completed},
		"ineligible bytes": {Result: ProducerResult{Status: 1, Terminal: ProducerFailed, EffectCertainty: NoEffect}, OutputPublishable: true, DetailReference: "detail"},
	} {
		if settlement.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	status := 3
	if err := (ProgramSettlement{State: "failed", Status: &status, DetailReference: "detail"}).Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := 256
	for name, settlement := range map[string]ProgramSettlement{
		"running": {State: "running", DetailReference: "detail"},
		"status":  {State: "completed", Status: &invalid, DetailReference: "detail"},
		"detail":  {State: "interrupted"},
	} {
		if settlement.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
