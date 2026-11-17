package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var authorities = []laws.Rule{{
	// Loopback decided from a host string has one authority, internal/egress.
	// Deciding it from a resolved netip.Addr is the floor and needs none.
	Name:    "loopback-from-a-name-has-one-authority",
	Anchors: []laws.Anchor{laws.Func("internal/egress", "SyntacticLoopback"), laws.Func("internal/egress", "LoopbackLiteral")},
	Exempt: []laws.Exemption{
		{Key: "internal/hostproxy/policy.go", Reason: "proxy bypass also treats the RFC 6761 *.localhost names as local, which suits skipping a proxy and not deciding what to bind"},
		{Key: "internal/projectstack/doclinks.go", Reason: "asks whether a documentation URL is public, covering private, unspecified and link-local ranges too"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/egress") || !fileDecidesLoopbackFromAString(f.AST) || r.Exempt(f.Rel) {
				continue
			}
			r.Report("%s decides loopback from a host string: use egress.SyntacticLoopback for a name, egress.LoopbackLiteral for an address literal", f.Rel)
		}
	},
}, {
	// A file that consults a classification authority resolves paths through
	// fspath.CanonicalPath: EvalSymlinks cannot see the /System/Volumes/Data
	// firmlink or a case-insensitive volume, so one location keeps two names.
	Name:    "path-identity-has-one-authority",
	Anchors: []laws.Anchor{laws.Func("internal/fspath", "CanonicalPath")},
	Exempt: []laws.Exemption{
		{Key: "internal/projectsource/source_lifecycle.go", Reason: "lifecycle writes reject dangling parent links and retain the selected directory entry"},
		{Key: "internal/fseffect/transaction_windows.go", Reason: "atomic replace refuses symlink components rather than following them"},
	},
	Check: func(r *laws.Run) {
		classifiers := []string{
			"github.com/lycaon/lycaon/internal/sensitivepath",
			"github.com/lycaon/lycaon/internal/settingsoverlay",
			"github.com/lycaon/lycaon/internal/grantedpath",
		}
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/fspath") || !callsFilepath(f, f.AST, "EvalSymlinks") || r.Exempt(f.Rel) {
				continue
			}
			for _, p := range classifiers {
				if f.Imports(p) {
					r.Report("%s consults %s and resolves paths itself; use fspath.CanonicalPath", f.Rel, p)
					break
				}
			}
			if resolvesByClimbing(f) {
				r.Report("%s resolves a missing path tail by climbing parents; use fspath.CanonicalPath", f.Rel)
			}
		}
	},
}}

// callsFilepath reports a path/filepath.name call below node.
func callsFilepath(f *laws.GoFile, node ast.Node, name string) bool {
	found := false
	f.Calls(node, func(_ *ast.CallExpr, pkg, fn string) {
		found = found || pkg == "path/filepath" && fn == name
	})
	return found
}

// resolvesByClimbing finds a loop that both resolves symlinks and takes
// filepath.Dir.
func resolvesByClimbing(f *laws.GoFile) bool {
	found := false
	ast.Inspect(f.AST, func(n ast.Node) bool {
		var body ast.Node
		switch loop := n.(type) {
		case *ast.ForStmt:
			body = loop.Body
		case *ast.RangeStmt:
			body = loop.Body
		default:
			return !found
		}
		found = found || callsFilepath(f, body, "EvalSymlinks") && callsFilepath(f, body, "Dir")
		return !found
	})
	return found
}

// fileDecidesLoopbackFromAString finds predicates that infer loopback from a name.
// Predicates over resolved addresses are outside this rule.
func fileDecidesLoopbackFromAString(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !takesAName(fn) || !returnsBool(fn) {
			return true
		}
		if bodyDecidesLoopback(fn.Body) {
			found = true
			return false
		}
		return true
	})
	return found
}

// takesAName reports a parameter that is a spelling rather than a resolved
// address: a bare string, or a URL, which is a string with structure.
func takesAName(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		switch t := field.Type.(type) {
		case *ast.Ident:
			if t.Name == "string" {
				return true
			}
		case *ast.StarExpr:
			if sel, ok := t.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "URL" {
				return true
			}
		}
	}
	return false
}

func returnsBool(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "bool" {
			return true
		}
	}
	return false
}

// bodyDecidesLoopback looks for the reserved name used in a comparison, or a
// parse-then-IsLoopback pair. A bare mention of the word is not enough.
func bodyDecidesLoopback(body *ast.BlockStmt) bool {
	comparesName, parses, checksLoopback := false, false, false
	isLocalhost := func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		return ok && strings.EqualFold(strings.Trim(lit.Value, `\"`), "localhost")
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if node.Op.String() == "==" && (isLocalhost(node.X) || isLocalhost(node.Y)) {
				comparesName = true
			}
		case *ast.CaseClause:
			for _, expr := range node.List {
				if isLocalhost(expr) {
					comparesName = true
				}
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "ParseIP", "ParseAddr":
					parses = true
				case "IsLoopback":
					checksLoopback = true
				case "EqualFold":
					for _, arg := range node.Args {
						if isLocalhost(arg) {
							comparesName = true
						}
					}
				}
			}
		}
		return true
	})
	return comparesName || (parses && checksLoopback)
}
