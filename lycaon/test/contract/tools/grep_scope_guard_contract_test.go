package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/toolscope"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGrepScopeRequiredHintRegistered(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadValidatedHintConfig(extpacks.Bundled(hintregistry.DefaultDir))
	contractcheck.FailErr(t, "hint config", err)
	entry, ok := cfg.HintCodes["GREP_SCOPE_REQUIRED"]
	if !ok {
		t.Fatal("GREP_SCOPE_REQUIRED missing from hint registry")
	}
	if entry.Emit != "guard:grep" {
		t.Fatalf("emit = %q", entry.Emit)
	}
	if rel := config.PlatformPolicy.Join("GREP_SCOPE_REQUIRED.yaml"); !config.Has(rel) {
		t.Fatalf("hint yaml missing from the bundled tree: %s", rel)
	}
}

func TestToolsScopeYAMLRootThreshold(t *testing.T) {
	t.Parallel()
	cfg, err := toolscope.Load()
	contractcheck.FailErr(t, "tools-scope", err)
	if cfg.RootStructuralFileCount < 10_000 {
		t.Fatalf("root_structural_file_count = %d want at least 10000", cfg.RootStructuralFileCount)
	}
}

func TestGrepScopeGuardNoUserMessageHeuristics(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "native", "survey", "grep_scope_guard.go")
	src, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read grep_scope_guard", err)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	contractcheck.FailErr(t, "parse grep_scope_guard", err)
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Contains", "HasPrefix", "HasSuffix", "EqualFold", "MatchString":
			for _, a := range call.Args {
				if id, ok := a.(*ast.Ident); ok {
					switch id.Name {
					case "userPrompt", "userMessage", "message", "text", "prompt":
						violations = append(violations, sel.Sel.Name+"("+id.Name+")")
					}
				}
			}
		}
		return true
	})
	if len(violations) > 0 {
		t.Fatalf("grep scope guard must not match user-message text: %v", violations)
	}
	body := string(src)
	for _, banned := range []string{"userPrompt", "userMessage", "Contains(user"} {
		if strings.Contains(body, banned) {
			t.Fatalf("grep_scope_guard.go must not reference %q", banned)
		}
	}
}
