package oar

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func nativeOperationIdentities(t *testing.T) map[string]bool {
	t.Helper()
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "load frozen language catalog", err)
	out := map[string]bool{}
	for _, disposition := range catalog.Tools {
		if len(disposition.CommandForms) > 0 {
			out[disposition.Tool] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("frozen catalog exposes no native operations")
	}
	return out
}

// Each bundled unit selects a native operation through the operation selector or
// names none. Prose selection by tool name is refused.
func TestBundledPolicyDispositionsForNativeOperations(t *testing.T) {
	native := nativeOperationIdentities(t)
	rs := loadStockRules(t)
	counts := map[string]int{}
	for _, rule := range rs.All() {
		var named []string
		for _, tool := range rule.Selector["tool"] {
			if native[tool] {
				named = append(named, tool)
			}
		}
		for op := range native {
			for _, spelling := range []string{`tool == "` + op + `"`, `tool=="` + op + `"`} {
				if strings.Contains(rule.When, spelling) {
					t.Errorf("%s selects native operation %s by tool comparison; use the paintedwolf.operation selector", rule.ID, op)
				}
			}
		}
		switch {
		case len(named) > 0:
			t.Errorf("%s selects native operations %v by structured tool only; translate it to the paintedwolf.operation selector", rule.ID, named)
		case len(rule.Selector["paintedwolf.operation"]) > 0:
			for _, op := range rule.Selector["paintedwolf.operation"] {
				if _, known := toolcontract.Lookup(op); !known {
					t.Errorf("%s operation selector names unknown operation %q", rule.ID, op)
				}
			}
			counts["translated"]++
		default:
			counts["preserved"]++
		}
	}
	if counts["translated"] == 0 || counts["preserved"] == 0 {
		t.Fatalf("disposition inventory is empty: %v", counts)
	}
	t.Logf("bundled unit dispositions: %v", counts)
}

// Operation selection uses bound identity and explicit selector alternatives.
func TestOperationSelectorsReachBoundNativeOperations(t *testing.T) {
	native := nativeOperationIdentities(t)
	operations := make([]string, 0, len(native))
	for op := range native {
		operations = append(operations, op)
	}
	sort.Strings(operations)
	bound := map[string]toolcontract.BoundOperation{}
	for _, op := range operations {
		contract, ok := toolcontract.Lookup(op)
		if !ok {
			t.Fatalf("%s contract absent", op)
		}
		definition, err := toolcontract.ToolOperation(op, json.RawMessage(`{"type":"object"}`), contract)
		testutil.FailErr(t, "freeze "+op, err)
		command := definition.Facts().Commands[0]
		mode := ""
		if len(command.Effects) > 0 {
			mode = command.Effects[0].Mode
		}
		inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, ActorID: "fixture-person", SessionID: "fixture-session", Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: op + "-child"}, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, CatalogDigest: toolcontract.LanguageCatalogDigest(), SourceUnitID: "submitted", Span: toolcontract.SourceSpan{EndByte: 8, Line: 1, Column: 1}}
		operation, err := toolcontract.BindOperation(definition, inv, command.Name, mode, nil)
		testutil.FailErr(t, "bind "+op, err)
		bound[op] = operation
	}
	observe := func(t *testing.T, tool, operation string) *GuardContext {
		t.Helper()
		gc := NewGuardContext()
		gc.ObserveToolCall(tool, map[string]any{"command": "fixture"})
		if operation != "" {
			testutil.FailErr(t, "observe "+operation, gc.ObserveNativeOperation(bound[operation]))
		}
		return gc
	}
	translated := 0
	for _, rule := range loadStockRules(t).All() {
		selected := rule.Selector["paintedwolf.operation"]
		if len(selected) == 0 {
			continue
		}
		translated++
		other := ""
		for _, op := range operations {
			if !slices.Contains(selected, op) {
				other = op
				break
			}
		}
		for _, op := range selected {
			matches := func(gc *GuardContext) bool {
				facts := selectorFacts{context: gc}
				ok, err := facts.match(rule)
				testutil.FailErr(t, "match "+rule.ID, err)
				return ok
			}
			if !matches(observe(t, op, "")) {
				t.Errorf("%s no longer selects structured %s", rule.ID, op)
			}
			if !native[op] {
				continue
			}
			gc := observe(t, "command", op)
			if !matches(gc) || gc.Invocation.Tool != "command" {
				t.Errorf("%s does not select native %s beneath the actual command call", rule.ID, op)
			}
			spoof := observe(t, "command", "")
			spoof.PutRejectData("FIXTURE", map[string]any{"native_operation": op, "operation": op})
			if matches(spoof) != slices.Contains(selected, "command") {
				t.Errorf("%s selected an unbound command from diagnostic data", rule.ID)
			}
			if other != "" && matches(observe(t, "command", other)) {
				t.Errorf("%s selected native %s for %s", rule.ID, other, op)
			}
		}
	}
	if translated == 0 {
		t.Fatal("no translated operation selectors")
	}
}
