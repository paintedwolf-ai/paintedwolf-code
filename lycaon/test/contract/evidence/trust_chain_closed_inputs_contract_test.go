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

// Closed inputs between DefaultConfinement and syscall.Exec:
//   1. Roots (Confinement) — control plane
//   2. Profile bytes — inherited pipe (--profile-fd), never a staging file
//   3. Launcher binary — os.Executable() / self re-exec
//
// Any other external input, such as an env or file-path profile, an extra helper
// flag, or another inherited fd, fails these tests.

var trustChainAllowedHelperFlags = map[string]bool{
	"__confine-exec": true,
	"--profile-fd":   true,
	"--":             true,
}

func TestTrustChainClosedInputs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	files := []string{
		filepath.Join(root, "lycaon", "internal", "confine", "confine.go"),
		filepath.Join(root, "lycaon", "internal", "confine", "helper.go"),
	}
	fset := token.NewFileSet()
	var findings []string

	for _, path := range files {
		src, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+path, err)
		f, err := parser.ParseFile(fset, path, src, 0)
		contractcheck.FailErr(t, "parse "+path, err)
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				return true
			}
			name := fn.Name.Name
			if name != "Command" && name != "runHelper" {
				return true
			}
			findings = append(findings, scanTrustChainFunc(fset, fn, rel)...)
			return false
		})
	}

	if len(findings) > 0 {
		t.Fatalf("unexpected pipeline inputs in Command/runHelper:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

func scanTrustChainFunc(fset *token.FileSet, fn *ast.FuncDecl, rel string) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		pkg, _ := sel.X.(*ast.Ident)
		pkgName := ""
		if pkg != nil {
			pkgName = pkg.Name
		}
		name := sel.Sel.Name
		pos := fset.Position(call.Pos())
		loc := rel + ":" + strconv.Itoa(pos.Line)

		switch {
		case pkgName == "os" && (name == "Getenv" || name == "LookupEnv"):
			if argMentionsProfile(call) {
				out = append(out, loc+": env-sourced profile (fourth input)")
			}
		case pkgName == "os" && (name == "Open" || name == "OpenFile" || name == "ReadFile" || name == "Readlink"):
			if argMentionsProfile(call) || argLooksLikeStagingPath(call) {
				out = append(out, loc+": file-path profile read (fourth input; use pipe)")
			}
		}
		return true
	})

	// Walk string literals for disallowed helper flags / staging suffixes.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		raw, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		pos := fset.Position(bl.Pos())
		loc := rel + ":" + strconv.Itoa(pos.Line)
		if strings.HasPrefix(raw, "--") && raw != "--" && !trustChainAllowedHelperFlags[raw] {
			if strings.Contains(raw, "profile") || strings.Contains(raw, "sandbox") {
				out = append(out, loc+": unknown helper flag "+raw+" (fourth input)")
			}
		}
		if strings.Contains(raw, "lycaon-sbpl-") || strings.HasSuffix(raw, ".sb") {
			out = append(out, loc+": profile staging path literal (fourth input)")
		}
		return true
	})
	return out
}

func argMentionsProfile(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			raw, _ := strconv.Unquote(bl.Value)
			lower := strings.ToLower(raw)
			if strings.Contains(lower, "profile") || strings.Contains(lower, "sandbox") || strings.Contains(lower, "sbpl") {
				return true
			}
		}
	}
	return false
}

func argLooksLikeStagingPath(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			raw, _ := strconv.Unquote(bl.Value)
			if strings.HasSuffix(raw, ".sb") || strings.Contains(raw, "lycaon-sbpl-") {
				return true
			}
		}
	}
	return false
}
