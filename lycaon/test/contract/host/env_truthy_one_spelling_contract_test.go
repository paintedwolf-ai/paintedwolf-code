package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// envTruthyExemptions covers booleans outside the host environment grammar.
var envTruthyExemptions = map[string]string{
	"lycaon/internal/gitexec/signing.go:gitConfigTrue": "git's own config boolean, matched to git",
}

func TestEnvTruthyHasOneSpelling(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	owner := filepath.Join(root, "configdir")

	var findings []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, owner+string(os.PathSeparator)) {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !returnsOnlyBool(fn) {
				return true
			}
			if !literals(fn.Body)["1"] || !literals(fn.Body)["true"] {
				return true
			}
			rel, _ := filepath.Rel(contractcheck.RepoRoot(t), path)
			key := filepath.ToSlash(rel) + ":" + fn.Name.Name
			if _, exempt := envTruthyExemptions[key]; !exempt {
				findings = append(findings, key)
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "scan internal for env-truthiness predicates", err)

	if len(findings) != 0 {
		t.Fatalf("env-flag predicate outside internal/configdir — call configdir.EnvTruthy:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

func returnsOnlyBool(fn *ast.FuncDecl) bool {
	res := fn.Type.Results
	if res == nil || len(res.List) != 1 || len(res.List[0].Names) > 1 {
		return false
	}
	ident, ok := res.List[0].Type.(*ast.Ident)
	return ok && ident.Name == "bool"
}

func literals(body *ast.BlockStmt) map[string]bool {
	found := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil {
			found[v] = true
		}
		return true
	})
	return found
}
