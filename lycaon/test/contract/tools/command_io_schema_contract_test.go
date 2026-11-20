package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The loaded command schema accepts its required arguments alone and rejects
// each retired argv-era argument with the typed argument-validation code.
func TestCommandToolSchemaRejectsRetiredIOArguments(t *testing.T) {
	t.Parallel()
	cfg, err := toolschema.LoadSchemaDirWithOperations(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load tool schemas", err)
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}
	required, _ := meta.ArgsSchema["required"].([]any)
	if len(required) == 0 {
		t.Fatalf("command schema declares no required arguments: %#v", meta.ArgsSchema["required"])
	}
	properties, _ := meta.ArgsSchema["properties"].(map[string]any)
	valid := map[string]any{}
	for _, name := range required {
		key, _ := name.(string)
		prop, _ := properties[key].(map[string]any)
		if prop["type"] != "string" {
			t.Fatalf("required argument %q has type %v; extend the fixture", key, prop["type"])
		}
		valid[key] = "true"
	}
	if reject := tools.ValidateCallArguments("command", valid, meta.ArgsSchema, tools.ToolContext{}); reject != nil {
		t.Fatalf("required arguments alone rejected: %#v", reject)
	}
	retired := map[string]any{
		"pipeline":         []any{map[string]any{"name": "true"}},
		"stdin":            "input",
		"stdin_from":       "input.txt",
		"env":              map[string]any{"KEY": "value"},
		"stdout_to":        "out.txt",
		"stderr_to":        "err.txt",
		"append":           true,
		"background":       true,
		"wait_ms":          1000,
		"allow_concurrent": true,
		"timeout_ms":       1000,
	}
	for name, value := range retired {
		args := map[string]any{name: value}
		for key, v := range valid {
			args[key] = v
		}
		reject := tools.ValidateCallArguments("command", args, meta.ArgsSchema, tools.ToolContext{})
		if reject == nil || reject.Code != "TOOL_ARGS_INVALID" || reject.Data["tool"] != "command" {
			t.Fatalf("retired argument %q: reject = %#v, want TOOL_ARGS_INVALID for command", name, reject)
		}
	}
}
