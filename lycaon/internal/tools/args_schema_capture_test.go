package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestCommandCaptureSchemaNamesConflictingField(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load command schema", err)
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}
	for field, value := range map[string]any{
		"pipeline": []any{"producer", "consumer"}, "stdin": "private-input",
		"stdin_from": "input", "stdout_to": "output", "stderr_to": "errors", "background": true,
	} {
		t.Run(field, func(t *testing.T) {
			args := map[string]any{"command": "consumer", "terminal_capture": map[string]any{}, field: value}
			if field == "pipeline" {
				delete(args, "command")
			}
			err := ValidateToolArgs(meta.ArgsSchema, args)
			if err == nil || !strings.Contains(err.Error(), "/"+field) {
				t.Fatalf("conflict must identify argument path /%s: %v", field, err)
			}
			if strings.Contains(err.Error(), "private-input") {
				t.Fatal("field conflict disclosed argument value")
			}
			delete(args, "terminal_capture")
			testutil.FailErr(t, "ordinary command accepts field", ValidateToolArgs(meta.ArgsSchema, args))
		})
	}
	testutil.FailErr(t, "capture without incompatible fields", ValidateToolArgs(meta.ArgsSchema, map[string]any{
		"command": "consumer", "terminal_capture": map[string]any{}, "background": false,
	}))
}
