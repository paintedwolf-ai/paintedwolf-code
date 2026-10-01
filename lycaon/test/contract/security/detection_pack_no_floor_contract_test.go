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

// These settings functions may inspect command arguments.
var allowedCommandPatternFns = map[string]bool{
	"MatchCommandPattern": true,
	"matchCommandRules":   true,
}

// TestDetectionPackNoMatcherInSettings rejects new command matchers in settings.
func TestDetectionPackNoMatcherInSettings(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "settings")
	fset := token.NewFileSet()
	var findings []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(contractcheck.RepoRoot(t), path)
		findings = append(findings, scanSettingsCommandMatchers(fset, f, rel)...)
		return nil
	})
	contractcheck.FailErr(t, "walk settings", err)
	if len(findings) > 0 {
		t.Fatalf("new command/argv pattern matching under internal/settings:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

func scanSettingsCommandMatchers(fset *token.FileSet, f *ast.File, rel string) []string {
	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name == nil {
			continue
		}
		fnName := fn.Name.Name
		if allowedCommandPatternFns[fnName] {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			kind, ok := stringsContainsHasPrefixOrRegexp(call)
			if !ok {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			if argLooksLikeCommandOrArgv(call.Args[0]) {
				pos := fset.Position(call.Pos())
				out = append(out, rel+":"+strconv.Itoa(pos.Line)+": "+fnName+" uses "+kind+" on command/argv")
			}
			return true
		})
	}
	return out
}

func stringsContainsHasPrefixOrRegexp(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch pkg.Name {
	case "strings":
		switch sel.Sel.Name {
		case "Contains", "HasPrefix", "ContainsAny", "HasSuffix":
			return "strings." + sel.Sel.Name, true
		}
	case "regexp":
		switch sel.Sel.Name {
		case "MatchString", "Match", "Compile", "MustCompile":
			return "regexp." + sel.Sel.Name, true
		}
	}
	return "", false
}

func argLooksLikeCommandOrArgv(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		switch x.Name {
		case "command", "cmd", "normalized", "argv", "token", "tok", "arg", "args":
			return true
		}
	case *ast.SelectorExpr:
		if x.Sel != nil {
			switch x.Sel.Name {
			case "Command", "Argv", "Args":
				return true
			}
		}
	case *ast.CallExpr:
		// strings.Fields(command), strings.TrimSpace(command), etc.
		if len(x.Args) > 0 {
			return argLooksLikeCommandOrArgv(x.Args[0])
		}
	}
	return false
}

// TestDetectionPackSettingsDoesNotImportDetectionpack guards import inversion.
func TestDetectionPackSettingsDoesNotImportDetectionpack(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "settings")
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			pathLit, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if pathLit == "github.com/lycaon/lycaon/internal/detectionpack" ||
				strings.HasSuffix(pathLit, "/internal/detectionpack") {
				rel, _ := filepath.Rel(contractcheck.RepoRoot(t), path)
				t.Errorf("%s imports internal/detectionpack", rel)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk settings imports", err)
}
