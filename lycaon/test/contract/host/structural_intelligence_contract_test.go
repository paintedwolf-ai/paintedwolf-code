package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestStructuralIntelligenceStructuralHintsPointAtStructuralTools verifies the
// shared STRUCTURAL_* hints route through the structural guard and cover both
// surfaces: grep (structural search) and code_rewrite (codemod).
func TestStructuralIntelligenceStructuralHintsPointAtStructuralTools(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for _, code := range []string{"STRUCTURAL_PATTERN_INVALID", "STRUCTURAL_LANG_UNKNOWN"} {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("missing shipped hint %q", code)
		}
		if entry.Emit != "guard:structural" {
			t.Fatalf("hint %q emit=%q want guard:structural", code, entry.Emit)
		}
		want := map[string]bool{"grep": true, "code_rewrite": true}
		if len(entry.Tools) != len(want) {
			t.Fatalf("hint %q tools=%v want %v", code, entry.Tools, want)
		}
		for _, tool := range entry.Tools {
			if !want[tool] {
				t.Fatalf("hint %q lists unexpected tool %q (want grep + code_rewrite)", code, tool)
			}
		}
	}
}

func TestStructuralIntelligenceSchemaExtensions(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)

	codeRewrite, ok := schemas.Tools["code_rewrite"]
	if !ok {
		t.Fatal("tools/schemas missing code_rewrite")
	}
	crProps, _ := codeRewrite.Schema["properties"].(map[string]any)
	for _, key := range []string{"path", "pattern", "paths", "recursive", "rewrite", "lang", "dry_run"} {
		if _, ok := crProps[key]; !ok {
			t.Fatalf("code_rewrite schema missing property %q", key)
		}
	}

	grep, ok := schemas.Tools["grep"]
	if !ok {
		t.Fatal("tools/schemas missing grep")
	}
	grepProps, _ := grep.Schema["properties"].(map[string]any)
	for _, key := range []string{"structural", "lang"} {
		if _, ok := grepProps[key]; !ok {
			t.Fatalf("grep schema missing structural property %q", key)
		}
	}

	readEntry := schemas.Tools["read"]
	readProps, _ := readEntry.Schema["properties"].(map[string]any)
	for _, key := range []string{"symbol", "kind"} {
		if _, ok := readProps[key]; !ok {
			t.Fatalf("read schema missing property %q", key)
		}
	}

	editEntry := schemas.Tools["edit"]
	editProps, _ := editEntry.Schema["properties"].(map[string]any)
	for _, key := range []string{"path", "old_string", "new_string"} {
		if _, ok := editProps[key]; !ok {
			t.Fatalf("edit schema missing property %q", key)
		}
	}
	for _, key := range []string{"symbol", "kind", "insert_before", "insert_after"} {
		if _, ok := editProps[key]; ok {
			t.Fatalf("edit schema must not publish %q", key)
		}
	}
}
