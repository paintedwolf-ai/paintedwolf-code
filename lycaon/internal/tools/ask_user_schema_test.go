package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestChoiceSchemaRequiresOptionsUnlessArtifactsSupplyThem(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load schemas", err)
	meta, ok := cfg.ToolMeta("ask_user")
	if !ok {
		t.Fatal("missing ask_user")
	}
	for _, kind := range []string{"single_choice", "multi_choice"} {
		for _, options := range [][]any{nil, {"one"}, {"one", "two"}} {
			args := map[string]any{"prompt": "Choose", "response_type": kind}
			if options != nil {
				args["options"] = options
			}
			err := ValidateToolArgs(meta.ArgsSchema, args)
			if (err == nil) != (len(options) >= 2) {
				t.Fatalf("%s options=%v error=%v", kind, options, err)
			}
		}
	}
	for _, purpose := range []string{"review", "compare"} {
		testutil.FailErr(t, "artifact choice", ValidateToolArgs(meta.ArgsSchema, map[string]any{"prompt": "Choose", "response_type": "single_choice", "purpose": purpose, "artifacts": []any{"a", "b"}}))
	}
}
