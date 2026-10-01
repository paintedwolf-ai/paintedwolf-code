package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestContentMutationFactHasOneSource(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)

	catalog := cfg.ContentMutatingTools()
	if len(catalog) == 0 {
		t.Fatal("mutates_content axis is empty")
	}

	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)

	for _, tool := range catalog {
		if !toolcontract.MutatesContent(tool) {
			t.Errorf("catalog names %q on mutates_content but the compiled axis does not", tool)
		}

		ok, err := reg.Evaluate("tool_is_write", conditions.EvalContext{ToolName: tool})
		contractcheck.FailErr(t, "evaluate tool_is_write for "+tool, err)
		if !ok {
			t.Errorf("condition tool_is_write is false for %q, which the catalog says authors content", tool)
		}
	}

	// Writing a path is not authoring its bytes.
	for _, tool := range []string{"copy", "move", "extract_archive", "mkdir", "chmod", "chown", "delete"} {
		if toolcontract.MutatesContent(tool) {
			t.Errorf("%q moves or stamps bytes without authoring them — it must not be on mutates_content", tool)
		}
		ok, err := reg.Evaluate("tool_is_write", conditions.EvalContext{ToolName: tool})
		contractcheck.FailErr(t, "evaluate tool_is_write for "+tool, err)
		if ok {
			t.Errorf("condition tool_is_write is true for %q", tool)
		}
	}
}

func TestContentAxisIsContainedInPathAxis(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)

	pathMutating := cfg.PathMutatingTools()
	for _, tool := range cfg.ContentMutatingTools() {
		if !contractcheck.ContainsString(pathMutating, tool) {
			t.Errorf("%q authors file content but is absent from mutates_path", tool)
		}
	}
	if len(cfg.ContentMutatingTools()) >= len(pathMutating) {
		t.Fatal("mutates_content must stay strictly narrower than mutates_path")
	}
}
