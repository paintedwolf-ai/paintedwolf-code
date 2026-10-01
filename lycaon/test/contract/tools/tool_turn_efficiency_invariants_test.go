package contract

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Batch opt-ins are explicit through catalog, schema, and runtime contracts.
func TestToolBatchOptInsStayExplicitThroughTheRuntime(t *testing.T) {
	t.Parallel()
	cfg, schemas := loadToolEfficiencyCatalog(t)
	if err := cfg.ValidateToolContracts(); err != nil {
		t.Fatalf("ValidateToolContracts: %v", err)
	}

	tools := sortedMapKeys(cfg.BatchOptInArg)
	if len(tools) == 0 {
		t.Fatal("native catalog has no batch opt-ins — this contract is checking nothing")
	}
	for _, tool := range tools {
		optInArg := cfg.BatchOptInArg[tool]
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			meta := requireToolSchema(t, schemas, tool)
			optIn := requireSchemaProperty(t, meta.ArgsSchema, tool, optInArg)
			if got := optIn["type"]; got != "boolean" {
				t.Fatalf("%s.%s type = %v, want boolean", tool, optInArg, got)
			}
			if value, present := optIn["default"]; !present || value != false {
				t.Fatalf("%s.%s default = %v, present=%v; opt-in batching must default false", tool, optInArg, value, present)
			}
			if description, _ := optIn["description"].(string); strings.TrimSpace(description) == "" {
				t.Fatalf("%s.%s has no model-facing description", tool, optInArg)
			}
			if schemaRequires(meta.ArgsSchema, optInArg) {
				t.Fatalf("%s.%s is required; omission must preserve serial execution", tool, optInArg)
			}
			if !slices.Contains(cfg.GrantIdentityNeutralArgs[tool], optInArg) {
				t.Fatalf("%s.%s changes scheduling, not authority; add it to grant_identity_neutral_args", tool, optInArg)
			}

			compiled, ok := toolcontract.Lookup(tool)
			if !ok {
				t.Fatalf("%s has no compiled tool contract — run ./task codegen:native-tool-contracts", tool)
			}
			if compiled.Batch != toolcontract.BatchSameTool || compiled.BatchOptInArg != optInArg {
				t.Fatalf("%s compiled batch contract = (%v, %q), want (same_tool, %q)", tool, compiled.Batch, compiled.BatchOptInArg, optInArg)
			}
			if compiled.BatchConcurrencyLimit != cfg.BatchConcurrencyLimit[tool] || compiled.BatchSerialWhenArg != cfg.BatchSerialWhenArg[tool] {
				t.Fatalf("%s compiled batch bounds drifted from the catalog — run ./task codegen:native-tool-contracts", tool)
			}
			if compiled.ConcurrentFor(nil) || compiled.ConcurrentFor(map[string]any{optInArg: false}) {
				t.Fatalf("%s executes siblings concurrently without an explicit true opt-in", tool)
			}
			optedIn := map[string]any{optInArg: true}
			if !compiled.ConcurrentFor(optedIn) {
				t.Fatalf("%s ignores its explicit batch opt-in", tool)
			}
			if !toolcontract.BatchGroupableCalls(tool, optedIn, compiled, tool, optedIn, compiled) {
				t.Fatalf("%s opted-in siblings are not groupable", tool)
			}
			if toolcontract.BatchGroupableCalls(tool, optedIn, compiled, tool, nil, compiled) {
				t.Fatalf("%s groups an opted-in call with a serial sibling", tool)
			}

			if serialArg := cfg.BatchSerialWhenArg[tool]; serialArg != "" {
				requireSchemaProperty(t, meta.ArgsSchema, tool, serialArg)
				withSerialArg := map[string]any{optInArg: true, serialArg: nil}
				if compiled.ConcurrentFor(withSerialArg) {
					t.Fatalf("%s.%s presence must override the batch opt-in", tool, serialArg)
				}
			}
		})
	}
	for i, leftName := range tools {
		left, _ := toolcontract.Lookup(leftName)
		leftArgs := map[string]any{cfg.BatchOptInArg[leftName]: true}
		for _, rightName := range tools[i+1:] {
			right, _ := toolcontract.Lookup(rightName)
			rightArgs := map[string]any{cfg.BatchOptInArg[rightName]: true}
			if toolcontract.BatchGroupableCalls(leftName, leftArgs, left, rightName, rightArgs, right) {
				t.Errorf("opt-in same-tool batches crossed tool identities: %s with %s", leftName, rightName)
			}
		}
	}
}

// Catalog batch caps fit within the process-wide ceiling.
func TestToolBatchCapsStayWithinTheHostCeiling(t *testing.T) {
	t.Parallel()
	cfg, _ := loadToolEfficiencyCatalog(t)
	tools := sortedMapKeys(cfg.BatchConcurrencyLimit)
	if len(tools) == 0 {
		t.Fatal("native catalog has no batch caps — this contract is checking nothing")
	}
	for _, tool := range tools {
		limit := cfg.BatchConcurrencyLimit[tool]
		if limit < 2 || limit > spawn.MaxConcurrentToolCalls {
			t.Errorf("%s batch_concurrency_limit = %d, want 2..%d", tool, limit, spawn.MaxConcurrentToolCalls)
		}
		compiled, ok := toolcontract.Lookup(tool)
		if !ok {
			t.Errorf("%s has no compiled tool contract — run ./task codegen:native-tool-contracts", tool)
			continue
		}
		if compiled.BatchConcurrencyLimit != limit {
			t.Errorf("%s compiled batch cap = %d, catalog = %d — run ./task codegen:native-tool-contracts", tool, compiled.BatchConcurrencyLimit, limit)
		}
	}
}

// Durable job summary modes are optional and bounded.
func TestDurableJobSummaryModesStayOptionalAndBounded(t *testing.T) {
	t.Parallel()
	cfg, schemas := loadToolEfficiencyCatalog(t)
	var checked int
	for _, row := range cfg.ToolContractRows() {
		if row.Lifecycle != nativemanifest.LifecycleDurableJob {
			continue
		}
		meta, ok := schemas.ToolMeta(row.Tool)
		if !ok {
			continue
		}
		completion, ok := schemaProperty(meta.ArgsSchema, "completion")
		if !ok || !schemaEnumContains(completion, "enqueue") || !schemaEnumContains(completion, "summary") {
			continue
		}
		checked++
		if got := completion["default"]; got != "enqueue" {
			t.Errorf("%s.completion default = %v, want enqueue", row.Tool, got)
		}
		if schemaRequires(meta.ArgsSchema, "completion") {
			t.Errorf("%s.completion is required; durable jobs must remain non-blocking by omission", row.Tool)
		}

		timeout := requireSchemaProperty(t, meta.ArgsSchema, row.Tool, "timeout_ms")
		minimum, minOK := timeout["minimum"].(int)
		maximum, maxOK := timeout["maximum"].(int)
		defaultValue, defaultOK := timeout["default"].(int)
		if timeout["type"] != "integer" || !minOK || !maxOK || !defaultOK || minimum < 1 || maximum < minimum || defaultValue < minimum || defaultValue > maximum {
			t.Errorf("%s.timeout_ms must be a positive bounded integer with an in-range default; got %v", row.Tool, timeout)
		}
		if schemaRequires(meta.ArgsSchema, "timeout_ms") {
			t.Errorf("%s.timeout_ms is required; the host default must remain usable", row.Tool)
		}
		for _, arg := range []string{"completion", "timeout_ms"} {
			if !slices.Contains(cfg.GrantIdentityNeutralArgs[row.Tool], arg) {
				t.Errorf("%s.%s changes waiting or presentation, not authority; add it to grant_identity_neutral_args", row.Tool, arg)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no durable job exposes enqueue/summary completion modes — this contract is checking nothing")
	}
}

func loadToolEfficiencyCatalog(t *testing.T) (nativemanifest.Config, *toolschema.Config) {
	t.Helper()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native tool manifest", err)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(
		contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load native tool schemas", err)
	return cfg, schemas
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func requireToolSchema(t *testing.T, schemas *toolschema.Config, tool string) toolschema.Meta {
	t.Helper()
	meta, ok := schemas.ToolMeta(tool)
	if !ok {
		t.Fatalf("%s has invocation metadata but no model-facing schema", tool)
	}
	return meta
}

func requireSchemaProperty(t *testing.T, schema map[string]any, tool, property string) map[string]any {
	t.Helper()
	value, ok := schemaProperty(schema, property)
	if !ok {
		t.Fatalf("%s invocation metadata names absent schema property %q", tool, property)
	}
	return value
}

func schemaProperty(schema map[string]any, property string) (map[string]any, bool) {
	properties, _ := schema["properties"].(map[string]any)
	value, ok := properties[property].(map[string]any)
	return value, ok
}

func schemaRequires(schema map[string]any, property string) bool {
	required, _ := schema["required"].([]any)
	for _, value := range required {
		if value == property {
			return true
		}
	}
	return false
}

func schemaEnumContains(property map[string]any, want string) bool {
	values, _ := property["enum"].([]any)
	return slices.Contains(values, any(want))
}
