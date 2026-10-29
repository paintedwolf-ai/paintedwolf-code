package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestCommandCaptureSchemaRejectsIncompatibleInput(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load command schema", err)
	meta, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}
	for field, value := range map[string]any{
		"stdin": "input", "stdin_from": "input.txt", "stdout_to": "out.txt",
		"stderr_to": "err.txt", "pipeline": []any{"producer", "consumer"}, "background": true,
	} {
		t.Run(field, func(t *testing.T) {
			args := map[string]any{"command": "program", field: value}
			if field == "pipeline" {
				delete(args, "command")
			}
			testutil.FailErr(t, "ordinary command supports input", ValidateToolArgs(meta.ArgsSchema, args))
			args["terminal_capture"] = map[string]any{}
			if ValidateToolArgs(meta.ArgsSchema, args) == nil {
				t.Fatalf("capture accepted %s before execution approval", field)
			}
		})
	}
	testutil.FailErr(t, "sealed foreground capture", ValidateToolArgs(meta.ArgsSchema, map[string]any{
		"command": "program", "terminal_capture": map[string]any{}, "background": false,
	}))
}
