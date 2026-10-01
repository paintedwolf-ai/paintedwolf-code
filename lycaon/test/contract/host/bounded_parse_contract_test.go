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

const (
	gotreesitterPath = "github.com/odvcencio/gotreesitter"
	// boundedParsePackage applies the parser budget.
	boundedParsePackage = "internal/tsparse"
)

// parsingTaggerMethods parse internally, so they bypass the budget the same way
// a raw parser does. TagTree is absent: it tags a tree the caller
// already parsed, which is how a bounded caller reaches the tagger.
var parsingTaggerMethods = map[string]bool{
	"Tag":                      true,
	"TagUTF16":                 true,
	"TagUTF16Bytes":            true,
	"TagIncremental":           true,
	"TagIncrementalUTF16":      true,
	"TagIncrementalUTF16Bytes": true,
}

// TestParsesGoThroughBoundedSeam: every parse goes through internal/tsparse, which
// applies the parse budget. GLR error recovery is superlinear after the first
// error, so an unbounded parse of a few-KB file can hold a worker for minutes.
func TestParsesGoThroughBoundedSeam(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var findings []string

	walk := func(scanRoot string) error {
		return filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if strings.Contains(filepath.ToSlash(rel), boundedParsePackage) {
				return nil
			}
			f, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			findings = append(findings, scanUnboundedParse(fset, f, filepath.ToSlash(rel))...)
			return nil
		})
	}

	// The module's Go source roots. Other trees under lycaon/ hold vendored
	// scanner-rule fixtures that are not valid Go.
	for _, srcRoot := range []string{"internal", "pkg", "cmd"} {
		contractcheck.FailErr(t, "walk "+srcRoot+" for unbounded parses", walk(filepath.Join(root, "lycaon", srcRoot)))
	}

	if len(findings) > 0 {
		t.Fatalf("Anti-drift: parse outside %s is unbounded:\n  %s\n"+
			"Use tsparse.Parse (then Tagger.TagTree for tags) and make no whole-file claim when it reports incomplete.",
			boundedParsePackage, strings.Join(findings, "\n  "))
	}
}

// scanUnboundedParse reports unbounded parse entry points in f: a
// gotreesitter.NewParser call (resolving the import's local name so an alias
// cannot slip past) or a tagger method that parses internally.
func scanUnboundedParse(fset *token.FileSet, f *ast.File, rel string) []string {
	local := gotreesitterLocalName(f)
	if local == "" {
		return nil
	}
	var findings []string
	report := func(pos token.Pos, what string) {
		findings = append(findings, rel+":"+strconv.Itoa(fset.Position(pos).Line)+" "+what)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		if sel.Sel.Name == "NewParser" {
			if ident, isIdent := sel.X.(*ast.Ident); isIdent && ident.Name == local {
				report(call.Pos(), local+".NewParser")
			}
			return true
		}
		if parsingTaggerMethods[sel.Sel.Name] {
			report(call.Pos(), "Tagger."+sel.Sel.Name)
		}
		return true
	})
	return findings
}

// gotreesitterLocalName returns the name gotreesitter is bound to in f, or ""
// when the file does not import it.
func gotreesitterLocalName(f *ast.File) string {
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != gotreesitterPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "gotreesitter"
	}
	return ""
}
