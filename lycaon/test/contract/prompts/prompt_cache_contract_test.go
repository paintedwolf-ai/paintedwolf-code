package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPromptCacheProviderFeaturesWire(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeOf(api.ProviderFeatures{}).FieldByName("PromptCache")
	if !ok {
		t.Fatal("ProviderFeatures missing PromptCache field")
	}
	if got := field.Tag.Get("json"); got != "prompt_cache" {
		t.Fatalf("PromptCache json tag = %q, want prompt_cache", got)
	}
}

func TestPromptCacheSessionBoundary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "assembly", "session_prompt_cache.go")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read session_prompt_cache.go", err)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, 0)
	contractcheck.FailErr(t, "parse session_prompt_cache.go", err)
	foundLoadStable := false
	foundStoreStable := false
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		switch fn.Name.Name {
		case "LoadStable":
			foundLoadStable = true
		case "StoreStable":
			foundStoreStable = true
		}
		return true
	})
	if !foundLoadStable || !foundStoreStable {
		t.Fatal("SessionPromptCache must retain LoadStable/StoreStable boundary API unchanged")
	}
}
