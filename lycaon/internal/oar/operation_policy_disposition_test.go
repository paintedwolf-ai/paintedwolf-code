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

// structuredOnlyOperationRules keep a structured tool selector for an operation
// that also has native command forms. Every other operation-specific unit is
// translated to the paintedwolf.operation selector; units naming no native
// operation are preserved unchanged because their selection never depended on
// a replaced schema.
var structuredOnlyOperationRules = map[string]string{
	"CHOWN_GROUP_UNKNOWN":         "native chown accepts numeric UID[:GID] only; the owner cannot emit an account lookup failure there",
	"CHOWN_USER_UNKNOWN":          "native chown accepts numeric UID[:GID] only; the owner cannot emit an account lookup failure there",
	"CHOWN_RECURSIVE_DENIED":      "native chown has no recursive option; the structured request remains the only producer",
	"HTTP_REQUEST_WEB_PAGE":       "post-invoke advisory from the structured result observation; native children publish no such observation",
	"SOURCE_ANALYSIS_UNAVAILABLE": "post-invoke advisory from the structured result observation; native children publish no such observation",
	"EDITORCONFIG_MISMATCH":       "post-invoke advisory from the structured result observation; native children publish no such observation",
	"SYNTAX_CHECK_OVERRIDDEN":     "post-invoke advisory from the structured result observation; native children publish no such observation",
}

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

// Each bundled unit has one mechanical disposition: translated through the
// operation selector, structured-only by recorded reason, or preserved because
// it names no native operation. Prose selection by tool name is refused.
func TestBundledPolicyDispositionsForNativeOperations(t *testing.T) {
	native := nativeOperationIdentities(t)
	rs := loadStockRules(t)
	counts := map[string]int{}
	unused := map[string]bool{}
	for id := range structuredOnlyOperationRules {
		unused[id] = true
	}
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
		_, structuredOnly := structuredOnlyOperationRules[rule.ID]
		delete(unused, rule.ID)
		switch {
		case len(named) > 0 && !structuredOnly:
			t.Errorf("%s selects native operations %v by structured tool only; translate it to the paintedwolf.operation selector or record why it stays structured", rule.ID, named)
		case structuredOnly:
			if len(named) == 0 {
				t.Errorf("%s is recorded structured-only but selects no native operation", rule.ID)
			}
			counts["structured-only"]++
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
	for id := range unused {
		t.Errorf("structured-only record %s names no bundled unit", id)
	}
	if counts["translated"] == 0 || counts["preserved"] == 0 {
		t.Fatalf("disposition inventory is empty: %v", counts)
	}
	t.Logf("bundled unit dispositions: %v", counts)
}

// Translated selection reaches the bound native operation beneath the real
// command call, keeps structured calls selected, and refuses other operations,
// unbound command calls and diagnostic data that names an operation.
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
			if matches(spoof) {
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
