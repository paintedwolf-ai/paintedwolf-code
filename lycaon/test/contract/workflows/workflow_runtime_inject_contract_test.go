package contract

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestInjectBuildersDoNotImportBlueprints(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	targets := []string{
		filepath.Join(root, "lycaon", "internal", "coordinator", "inject", "inject_dto.go"),
		filepath.Join(root, "lycaon", "internal", "coordinator", "inject", "active_workflow_inject.go"),
		filepath.Join(root, "lycaon", "internal", "workflow", "runtime", "coordinator_frames.go"),
		filepath.Join(root, "lycaon", "internal", "workflow", "runtime", "snapshots.go"),
	}
	for _, path := range targets {
		imports, err := goFileImports(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, imp := range imports {
			if strings.Contains(imp, "/internal/blueprint") {
				t.Fatalf("%s imports blueprint package %q", path, imp)
			}
		}
	}
}

func goFileImports(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out, nil
}
