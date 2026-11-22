package toolreach_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
)

func TestCallTextPreservesExactTypesAndRoundTripsWaitAgainstProjectedSchema(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog", err)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "schemas", err)
	catalog, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	testutil.FailErr(t, "vocab catalog", err)

	waitMeta, ok := schemas.ToolMeta("wait")
	if !ok {
		t.Fatal("missing stock wait schema")
	}
	projectedWait := toolschema.ProjectToolArgsSchema("wait", waitMeta.ArgsSchema, toolschema.ProjectOptions{
		PruneWaitConditions: true,
		WaitConditions:      []string{"process_done", "http_ready"},
	})

	origWaitArgs := map[string]any{
		"capability_request": map[string]any{
			"loopback_connect": map[string]any{
				"ports": []any{int64(8080)},
			},
		},
		"conditions": []any{
			map[string]any{
				"handles": []any{"cmd-docker-1", "cmd-docker-2"},
				"kind":    "process_done",
			},
			map[string]any{
				"kind":       "http_ready",
				"method":     "GET",
				"port":       int64(8080),
				"status_max": int64(299),
				"status_min": int64(200),
				"url":        "http://127.0.0.1:8080/health",
			},
		},
		"reason":         "awaiting \"api\"\nstage 1\\2\t`ok`",
		"resume":         false,
		"timeout_ms":     int64(1800000),
		"until_complete": true,
	}

	completeWait := toolreach.Call{Tool: "wait", Args: origWaitArgs}
	rendered := completeWait.Text()
	wantText := "`wait(" +
		"capability_request.loopback_connect.ports=[8080], " +
		"conditions=[{\"handles\": [\"cmd-docker-1\", \"cmd-docker-2\"], \"kind\": \"process_done\"}, {\"kind\": \"http_ready\", \"method\": \"GET\", \"port\": 8080, \"status_max\": 299, \"status_min\": 200, \"url\": \"http://127.0.0.1:8080/health\"}], " +
		"reason=\"awaiting \\\"api\\\"\\nstage 1\\\\2\\t\\u0060ok\\u0060\", " +
		"resume=false, " +
		"timeout_ms=1800000, " +
		"until_complete=true" +
		")`"
	if rendered != wantText {
		t.Fatalf("Call.Text() =\n%s\nwant:\n%s", rendered, wantText)
	}

	parsed := toolvocabtest.ParseCallExamples(catalog, rendered)
	if len(parsed) != 1 {
		t.Fatalf("expected 1 parsed call example, got %d (%+v)", len(parsed), parsed)
	}
	ex := parsed[0]
	if ex.HasEllipsis || ex.HasPlaceholder {
		t.Fatalf("complete call example unexpectedly marked partial/placeholder: %+v", ex)
	}
	if !reflect.DeepEqual(ex.Args, origWaitArgs) {
		t.Fatalf("parsed args =\n%#v\nwant:\n%#v", ex.Args, origWaitArgs)
	}
	if issues := toolvocabtest.ValidateCallExample(ex, projectedWait); len(issues) > 0 {
		t.Fatalf("ValidateCallExample issues: %+v", issues)
	}
	if err := tools.ValidateToolArgs(projectedWait, ex.Args); err != nil {
		t.Fatalf("ValidateToolArgs failed on round-tripped wait args: %v", err)
	}

	req := toolreach.Offered([]string{"wait"}).WithSchemas(map[string]map[string]any{"wait": projectedWait})
	if _, ok := req.Sequence(completeWait); !ok {
		t.Fatal("expected admitted wait condition call to succeed in Sequence")
	}
	prunedCondCall := toolreach.Call{
		Tool: "wait",
		Args: map[string]any{"conditions": []any{map[string]any{"kind": "port_ready", "port": 8080}}},
	}
	if _, ok := req.Sequence(prunedCondCall); ok {
		t.Fatal("expected pruned port_ready condition kind to fail closed in Sequence")
	}
}

func TestCallTextDeterministicEmptyContainersNumericVariantsAndPartialCalls(t *testing.T) {
	customSchemas := toolschema.ConfigFromEntries(map[string]toolschema.Entry{
		"command": {
			Description: "Run command",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":            map[string]any{"type": "string"},
					"capability_request": map[string]any{"type": "object", "properties": map[string]any{"process_control": map[string]any{"type": "object"}}},
					"env":                map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
					"tags":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"timeout_ms":         map[string]any{"type": "integer"},
					"verification":       map[string]any{"type": "boolean"},
				},
				"required": []any{"command"},
			},
		},
	})
	projSchema := toolschema.ProjectArgsSchema(customSchemas.Tools["command"].Schema)
	cat, err := toolvocab.NewCatalog(customSchemas, []sandbox.ToolProfile{{ID: "p", Tools: map[string]bool{"command": true}}}, toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)

	completeEmptyContainers := toolreach.Call{
		Tool: "command",
		Args: map[string]any{
			"capability_request": map[string]any{"process_control": map[string]any{}},
			"command":            "ls \"a b\"",
			"env":                map[string]any{"Z_VAR": "last", "A_VAR": "first"},
			"tags":               []any{},
			"timeout_ms":         float64(1000000),
			"verification":       true,
		},
	}
	gotText := completeEmptyContainers.Text()
	wantText := "`command(capability_request.process_control={}, command=\"ls \\\"a b\\\"\", env={\"A_VAR\": \"first\", \"Z_VAR\": \"last\"}, tags=[], timeout_ms=1000000, verification=true)`"
	if gotText != wantText {
		t.Fatalf("Call.Text() =\n%s\nwant:\n%s", gotText, wantText)
	}

	parsed := toolvocabtest.ParseCallExamples(cat, gotText)
	if len(parsed) != 1 || parsed[0].HasEllipsis {
		t.Fatalf("parsed = %+v, want 1 complete call", parsed)
	}
	wantNormalized := map[string]any{
		"capability_request": map[string]any{"process_control": map[string]any{}},
		"command":            "ls \"a b\"",
		"env":                map[string]any{"A_VAR": "first", "Z_VAR": "last"},
		"tags":               []any{},
		"timeout_ms":         int64(1000000),
		"verification":       true,
	}
	if !reflect.DeepEqual(parsed[0].Args, wantNormalized) {
		t.Fatalf("round-tripped args = %#v, want %#v", parsed[0].Args, wantNormalized)
	}
	if issues := toolvocabtest.ValidateCallExample(parsed[0], projSchema); len(issues) > 0 {
		t.Fatalf("ValidateCallExample issues: %+v", issues)
	}

	typedVariants := toolreach.Call{
		Tool:    "command",
		Partial: true,
		Args: map[string]any{
			"a_int":     42,
			"b_uint":    uint64(99),
			"c_num":     json.Number("12345"),
			"d_float":   1.5,
			"e_strs":    []string{"x", "y"},
			"f_ints":    []int{1, 2},
			"g_str_map": map[string]string{"b": "2", "a": "1"},
			"h_nil":     nil,
		},
	}
	partialText := typedVariants.Text()
	wantPartial := "`command(a_int=42, b_uint=99, c_num=12345, d_float=1.5, e_strs=[\"x\", \"y\"], f_ints=[1, 2], g_str_map={\"a\": \"1\", \"b\": \"2\"}, h_nil=null, …)`"
	if partialText != wantPartial {
		t.Fatalf("partial Call.Text() =\n%s\nwant:\n%s", partialText, wantPartial)
	}
	parsedPartial := toolvocabtest.ParseCallExamples(cat, partialText)
	if len(parsedPartial) != 1 || !parsedPartial[0].HasEllipsis {
		t.Fatalf("expected partial call to preserve ellipsis marker, got %+v", parsedPartial)
	}

	barePartial := (toolreach.Call{Tool: "command", Partial: true}).Text()
	if barePartial != "`command(…)`" || !strings.Contains(barePartial, "…") {
		t.Fatalf("bare partial = %q, want `command(…)`", barePartial)
	}
}
