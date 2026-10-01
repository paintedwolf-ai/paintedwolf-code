package contract

// AST scan for silent-invalidator invariant over the stable-prefix
// code surface defined in docs/session.md.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
)

type stablePrefixScan struct {
	Violations []string
}

// stablePrefixScanTargets maps repo-relative file paths to function names scanned
// within that file. A nil name list scans every func in the file.
var stablePrefixScanTargets = map[string][]string{
	"internal/coordinator/assembly/tripartite_prompt.go": nil,
	"internal/coordinator/assembly/engine.go": {
		"prependTransitionInject",
		"markPromptCacheBreakpoints",
	},
	"internal/coordinator/assembly/pre_history_injects.go": {
		"appendPreHistorySystemInjects",
	},
	"internal/coordinator/inject/execution_mode_prompt.go": nil,
}

func scanStablePrefixSurface(lycaonRoot string) (*stablePrefixScan, error) {
	out := &stablePrefixScan{}
	fset := token.NewFileSet()
	for rel, funcNames := range stablePrefixScanTargets {
		path := filepath.Join(lycaonRoot, filepath.FromSlash(rel))
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		allowed := make(map[string]bool, len(funcNames))
		for _, name := range funcNames {
			allowed[name] = true
		}
		scanAll := len(funcNames) == 0
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !scanAll && !allowed[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					recordStablePrefixBannedCall(out, fset, rel, fn.Name.Name, call)
				}
				return true
			})
		}
	}
	return out, nil
}

func recordStablePrefixBannedCall(out *stablePrefixScan, fset *token.FileSet, rel, funcName string, call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg := selectorPackage(sel.X)
	if pkg == "rand" {
		out.Violations = append(out.Violations, siteLabel(fset, rel, funcName, call.Pos()))
		return
	}
	switch pkg + "." + sel.Sel.Name {
	case "time.Now", "time.Since":
		out.Violations = append(out.Violations, siteLabel(fset, rel, funcName, call.Pos()))
	case "uuid.New", "uuid.NewString", "uuid.NewRandom":
		out.Violations = append(out.Violations, siteLabel(fset, rel, funcName, call.Pos()))
	}
}

func selectorPackage(x ast.Expr) string {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return selectorPackage(v.X) + "." + v.Sel.Name
	default:
		return ""
	}
}

func siteLabel(fset *token.FileSet, rel, funcName string, pos token.Pos) string {
	line := fset.Position(pos).Line
	return rel + ":" + strconv.Itoa(line) + " (" + funcName + ")"
}
