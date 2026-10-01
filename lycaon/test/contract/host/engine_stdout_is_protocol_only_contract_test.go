package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// These packages expose stdout as their output protocol.
var engineStdoutOwners = []string{
	filepath.Join("internal", "startupprotocol"),
	filepath.Join("internal", "logscli"),
	filepath.Join("internal", "promptattach", "docext"),
	filepath.Join("internal", "sshproxy"),
}

// The shell closes stdout after startup; later writes can terminate the engine.
func TestEngineServingPathNeverWritesStdout(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var findings []string

	for _, tree := range []string{"internal", "pkg"} {
		base := filepath.Join(root, "lycaon", tree)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(filepath.Join(root, "lycaon"), path)
			if relErr != nil {
				return relErr
			}
			for _, owner := range engineStdoutOwners {
				if strings.HasPrefix(rel, owner+string(filepath.Separator)) {
					return nil
				}
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case pkg.Name == "os" && sel.Sel.Name == "Stdout":
					findings = append(findings, rel+": writes to os.Stdout")
				case pkg.Name == "middleware" && sel.Sel.Name == "Logger":
					// This logger binds stdout at initialization.
					findings = append(findings, rel+": chi middleware.Logger logs to os.Stdout — use requestLogger()")
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}

	if len(findings) > 0 {
		t.Fatalf("engine stdout belongs to the startup protocol:\n  %s", strings.Join(findings, "\n  "))
	}
}
