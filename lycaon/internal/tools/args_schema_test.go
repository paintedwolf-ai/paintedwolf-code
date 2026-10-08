package tools

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestSchemaRequiresValidation(t *testing.T) {
	if schemaRequiresValidation(map[string]any{"type": "object"}) {
		t.Fatal("bare object should skip validation")
	}
	if !schemaRequiresValidation(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []any{"path"},
	}) {
		t.Fatal("schema with required property should validate")
	}
}

func TestValidateToolArgsRejectsMissingRequired(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
	err := ValidateToolArgs(schema, map[string]any{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateToolArgsAcceptsValid(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
	if err := ValidateToolArgs(schema, map[string]any{"path": "main.go"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateToolArgsSummarizeSchema(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("summarize")
	if !ok {
		t.Fatal("summarize schema missing")
	}
	schema := meta.ArgsSchema
	// The handler owns SUMMARIZE_NO_INPUT; the schema validates supplied fields.
	cases := []struct {
		name string
		args map[string]any
		ok   bool
	}{
		{"path only", map[string]any{"path": "pkg/a.go"}, true},
		{"path and task", map[string]any{"path": "pkg/a.go", "task": "explain"}, true},
		{"paths only", map[string]any{"paths": []any{"a.go", "b.go"}}, true},
		{"content only", map[string]any{"content": strings.Repeat("x", 200)}, true},
		{"task only (handler rejects, schema ok)", map[string]any{"task": "explain"}, true},
		{"path pattern", map[string]any{"path": "pkg", "pattern": "func"}, true},
		{"wrong type path", map[string]any{"path": 42}, false},
		{"unknown key", map[string]any{"path": "a.go", "nope": "x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateToolArgs(schema, tc.args)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateToolArgsCommandAcceptsDirectIPTrue(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}
	args := map[string]any{
		"command": "python3 ntp_health.py",
		"capability_request": map[string]any{
			"direct_ip": true,
		},
	}
	if err := ValidateToolArgs(meta.ArgsSchema, args); err != nil {
		t.Fatalf("direct_ip: true must be schema-valid: %v", err)
	}
	args["capability_request"] = map[string]any{
		"direct_ip": []any{"udp://time.nist.gov:123"},
	}
	if err := ValidateToolArgs(meta.ArgsSchema, args); err != nil {
		t.Fatalf("direct_ip destination list must be schema-valid: %v", err)
	}
}

func TestValidateToolArgsHTTPRequestRequiresStructuredJSON(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("http_request")
	if !ok {
		t.Fatal("http_request schema missing")
	}
	base := map[string]any{"url": "https://example.test", "method": "POST"}
	for _, body := range []any{map[string]any{"ok": true}, []any{"one", "two"}} {
		args := map[string]any{"url": base["url"], "method": base["method"], "body_json": body}
		if err := ValidateToolArgs(meta.ArgsSchema, args); err != nil {
			t.Fatalf("structured body_json rejected: %v", err)
		}
	}
	args := map[string]any{"url": base["url"], "method": base["method"], "body_json": `{"ok":true}`}
	if err := ValidateToolArgs(meta.ArgsSchema, args); err == nil {
		t.Fatal("encoded JSON text must use body_text")
	}
	projected := TrimCoordinatorToolMeta(ToolMeta{
		Name: meta.Name, Description: meta.Description, ArgsSchema: meta.ArgsSchema,
	})
	properties, ok := projected.ArgsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("provider schema properties = %#v", projected.ArgsSchema["properties"])
	}
	bodyJSON, ok := properties["body_json"].(map[string]any)
	if !ok {
		t.Fatalf("provider body_json schema = %#v", properties["body_json"])
	}
	branches, ok := bodyJSON["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("provider body_json oneOf = %#v", bodyJSON["oneOf"])
	}
	for i, want := range []string{"object", "array"} {
		branch, ok := branches[i].(map[string]any)
		if !ok || branch["type"] != want {
			t.Fatalf("provider body_json branch %d = %#v, want %s", i, branches[i], want)
		}
	}
}

func TestValidateToolArgsCommandOutputRequiresCursor(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command_output")
	if !ok {
		t.Fatal("command_output schema missing")
	}
	if err := ValidateToolArgs(meta.ArgsSchema, map[string]any{"handle": "cmd-1"}); err == nil {
		t.Fatal("command_output without cursor must be rejected")
	}
	if err := ValidateToolArgs(meta.ArgsSchema, map[string]any{"handle": "cmd-1", "cursor": 0}); err != nil {
		t.Fatalf("initial cursor must be accepted: %v", err)
	}
	if err := ValidateToolArgs(meta.ArgsSchema, map[string]any{"handle": "cmd-1", "cursor": -1}); err == nil {
		t.Fatal("negative cursor must be rejected")
	}
}

func TestInvokeRejectsInvalidArgsBeforeHandler(t *testing.T) {
	reg := NewDefaultRegistry()
	called := false
	registerTestDefinition(t, reg, "strict_tool", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		called = true
		return "ok", nil
	})
	def, _ := reg.Definition("strict_tool")
	def.Meta = ToolMeta{
		Name: "strict_tool",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		},
	}
	_ = reg.RegisterDefinition(def)

	exec := NewDefaultToolExecutor(nil, reg, "implement")
	_, err := exec.Invoke(t.Context(), "strict_tool", map[string]any{}, ToolContext{})
	if err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Fatal("handler should not run when args invalid")
	}
}

func TestInvokeToolArgsInvalidObservation(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	reg := NewDefaultRegistry()
	registerTestDefinition(t, reg, "strict_tool", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		return "ok", nil
	})
	def, _ := reg.Definition("strict_tool")
	def.Meta = ToolMeta{
		Name: "strict_tool",
		ArgsSchema: map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}
	_ = reg.RegisterDefinition(def)
	exec := NewDefaultToolExecutor(nil, reg, "implement")
	_, err := exec.Invoke(t.Context(), "strict_tool", map[string]any{}, ToolContext{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "TOOL_ARGS_INVALID") {
		t.Fatalf("error = %q", err.Error())
	}
	if AsToolReject(err) == nil {
		t.Fatalf("expected ToolReject observation, got %q", err.Error())
	}
}

func TestInvokeTruncatedArgsRejectsBeforeHandler(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	reg := NewDefaultRegistry()
	var called bool
	_ = reg.Register("write", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		called = true
		return "ok", nil
	})
	exec := NewDefaultToolExecutor(nil, reg, "implement")
	_, err := exec.Invoke(t.Context(), "write", nil, ToolContext{ArgsTruncated: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "TOOL_ARGS_TRUNCATED") {
		t.Fatalf("error = %q", err.Error())
	}
	if AsToolReject(err) == nil {
		t.Fatalf("expected ToolReject observation, got %q", err.Error())
	}
	if called {
		t.Fatal("handler must not run for truncated args")
	}
}

func TestInvokeMalformedArgsRejectsBeforeHandler(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	reg := NewDefaultRegistry()
	var called bool
	_ = reg.Register("summarize", func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
		called = true
		return "ok", nil
	})
	exec := NewDefaultToolExecutor(nil, reg, "implement")
	_, err := exec.Invoke(t.Context(), "summarize", nil, ToolContext{ArgsMalformed: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "TOOL_ARGS_MALFORMED") {
		t.Fatalf("error = %q", err.Error())
	}
	if AsToolReject(err) == nil {
		t.Fatalf("expected ToolReject observation, got %q", err.Error())
	}
	if called {
		t.Fatal("handler must not run for malformed args")
	}
}

func TestInvokeRejectsUnencodableArgsWithoutSchema(t *testing.T) {
	reg := NewDefaultRegistry()
	called := false
	registerTestDefinition(t, reg, "untyped_tool", func(context.Context, map[string]any, ToolContext) (string, error) {
		called = true
		return "ok", nil
	})
	executor := NewDefaultToolExecutor(nil, reg, "implement")
	args := map[string]any{"invalid": make(chan int)}
	if reject := executor.validateInvocation(t.Context(), "untyped_tool", "implement", args, &ToolContext{}); reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("unencodable arguments received wrong rejection: %+v", reject)
	}
	_, err := executor.Invoke(t.Context(), "untyped_tool", args, ToolContext{})
	if err == nil || called {
		t.Fatalf("unencodable invocation reached handler: called=%t err=%v", called, err)
	}
}

func TestValidateToolArgsEnforcesAdditionalProperties(t *testing.T) {
	for _, tc := range []struct {
		name       string
		additional any
		valid      map[string]any
		invalid    map[string]any
	}{
		{"closed object", false, map[string]any{}, map[string]any{"unexpected": true}},
		{"typed properties", map[string]any{"type": "string"}, map[string]any{"key": "value"}, map[string]any{"key": 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := map[string]any{"type": "object", "additionalProperties": tc.additional}
			if err := ValidateToolArgs(schema, tc.invalid); err == nil {
				t.Fatal("additionalProperties constraint was ignored")
			}
			if err := ValidateToolArgs(schema, tc.valid); err != nil {
				t.Fatalf("valid arguments rejected: %v", err)
			}
		})
	}
}

func TestInvalidHostSchemaDoesNotBlameArguments(t *testing.T) {
	reject := ValidateCallArguments("fixture", map[string]any{}, map[string]any{"type": "not-a-json-schema-type"}, ToolContext{})
	if reject == nil || reject.Code != ToolOwnerFailedCode || reject.ArgumentValidation {
		t.Fatalf("[OAR-PROF-3] invalid host schema was classified as an argument failure: %#v", reject)
	}
}

func TestValidateCallArgumentsIntrospectionMisnestedField(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	args := map[string]any{
		"command":        "colima start",
		"host_resources": []any{"colima"},
	}
	reject := ValidateCallArguments("command", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	assertMisplaced(t, reject.Data, []string{"host_resources"}, "", "capability_request")
	rep, ok := reject.Data["replacement_args"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing or not a map: %#v", reject.Data["replacement_args"])
	}
	if _, present := rep["host_resources"]; present {
		t.Errorf("replacement_args still has host_resources at root")
	}
	capReq, ok := rep["capability_request"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing capability_request map: %#v", rep)
	}
	res, ok := capReq["host_resources"].([]any)
	if !ok || len(res) != 1 || res[0] != "colima" {
		t.Errorf("replacement_args capability_request.host_resources = %#v, want [colima]", capReq["host_resources"])
	}
}

func TestValidateCallArgumentsIntrospectionStringifiedJSON(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	// Top-level host_resources passed as a stringified JSON array
	args := map[string]any{
		"command":        "colima start",
		"host_resources": "[\"colima\"]",
	}
	reject := ValidateCallArguments("command", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	if reject.Data["json_encoded"] != true || reject.Data["field"] != "host_resources" {
		t.Errorf("json_encoded = %v field = %v, want true host_resources", reject.Data["json_encoded"], reject.Data["field"])
	}
	assertMisplaced(t, reject.Data, []string{"host_resources"}, "", "capability_request")
	rep, ok := reject.Data["replacement_args"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing or not a map: %#v", reject.Data["replacement_args"])
	}
	capReq, ok := rep["capability_request"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing capability_request: %#v", rep)
	}
	res, ok := capReq["host_resources"].([]any)
	if !ok || len(res) != 1 || res[0] != "colima" {
		t.Errorf("replacement_args parsed stringified json = %#v, want [colima]", capReq["host_resources"])
	}
}

func TestValidateCallArgumentsIntrospectionFuzzyTypo(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	args := map[string]any{
		"command": "echo hi",
		"timeout": 5000,
	}
	reject := ValidateCallArguments("command", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	if reject.Data["did_you_mean"] != "timeout_ms" {
		t.Errorf("did_you_mean = %v, want timeout_ms", reject.Data["did_you_mean"])
	}
}

func TestValidateCallArgumentsIntrospectionConflictKeys(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("replace_lines")
	if !ok {
		t.Fatal("replace_lines schema missing")
	}

	args := map[string]any{
		"path":        "src/main.go",
		"start_line":  1,
		"end_line":    2,
		"new_content": "updated",
		"operations": []any{
			map[string]any{"kind": "replace", "start_line": 1, "end_line": 2, "new_content": "updated"},
		},
	}
	reject := ValidateCallArguments("replace_lines", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	conflicts, ok := reject.Data["conflict_keys"].([]string)
	if !ok || len(conflicts) < 2 {
		t.Fatalf("conflict_keys = %#v, want conflict keys", reject.Data["conflict_keys"])
	}
}

func TestValidateCallArgumentsIntrospectionMultipleMisnestedFields(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	// Multiple fields placed at root: host_resources and direct_ip
	args := map[string]any{
		"command":        "colima start",
		"host_resources": []any{"colima"},
		"direct_ip":      true,
	}
	reject := ValidateCallArguments("command", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	rep, ok := reject.Data["replacement_args"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing or not a map: %#v", reject.Data["replacement_args"])
	}
	if _, present := rep["host_resources"]; present {
		t.Errorf("replacement_args still has host_resources at root")
	}
	if _, present := rep["direct_ip"]; present {
		t.Errorf("replacement_args still has direct_ip at root")
	}
	capReq, ok := rep["capability_request"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing capability_request map: %#v", rep)
	}
	if capReq["direct_ip"] != true {
		t.Errorf("capability_request.direct_ip = %v, want true", capReq["direct_ip"])
	}
	res, ok := capReq["host_resources"].([]any)
	if !ok || len(res) != 1 || res[0] != "colima" {
		t.Errorf("capability_request.host_resources = %#v, want [colima]", capReq["host_resources"])
	}
}

func TestValidateCallArgumentsIntrospectionShortTypoNoFalsePositive(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	// Short unrelated key "cat" should NOT match "command"
	args := map[string]any{
		"command": "echo hi",
		"cat":     "dog",
	}
	reject := ValidateCallArguments("command", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	if didYouMean, ok := reject.Data["did_you_mean"]; ok && didYouMean != "" {
		t.Errorf("did_you_mean = %v, expected empty for unrelated short word", didYouMean)
	}
}

func TestValidateCallArgumentsIntrospectionUnknownJSONNotUnparsed(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	// An unknown key holding JSON text is not a structured slot.
	args := map[string]any{
		"command":     "echo hi",
		"custom_opts": "[\"flag1\", \"flag2\"]",
	}
	reject := ValidateCallArguments("command", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	if reject.Data["json_encoded"] == true || reject.Data["json_malformed"] == true {
		t.Errorf("unknown property custom_opts was flagged as JSON text")
	}
}

func TestValidateCallArgumentsIntrospectionNestedStringifiedJSON(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	// Stringified JSON inside existing capability_request object
	args := map[string]any{
		"command": "colima start",
		"capability_request": map[string]any{
			"host_resources": "[\"colima\"]",
		},
	}
	reject := ValidateCallArguments("command", args, meta.ArgsSchema, ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected TOOL_ARGS_INVALID, got: %#v", reject)
	}
	if reject.Data["json_encoded"] != true || reject.Data["field"] != "capability_request.host_resources" {
		t.Errorf("json_encoded = %v field = %v, want true capability_request.host_resources", reject.Data["json_encoded"], reject.Data["field"])
	}
	rep, ok := reject.Data["replacement_args"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing or not a map: %#v", reject.Data["replacement_args"])
	}
	capReq, ok := rep["capability_request"].(map[string]any)
	if !ok {
		t.Fatalf("replacement_args missing capability_request: %#v", rep)
	}
	res, ok := capReq["host_resources"].([]any)
	if !ok || len(res) != 1 || res[0] != "colima" {
		t.Errorf("repaired capability_request.host_resources = %#v, want [colima]", capReq["host_resources"])
	}
}

func assertMisplaced(t *testing.T, data map[string]any, fields []string, found, belongs string) {
	t.Helper()
	got, _ := data["misplaced_fields"].([]string)
	if !slices.Equal(got, fields) || data["found_under"] != found || data["belongs_under"] != belongs {
		t.Fatalf("misplaced = %v under %q → %q, want %v under %q → %q",
			got, data["found_under"], data["belongs_under"], fields, found, belongs)
	}
}
