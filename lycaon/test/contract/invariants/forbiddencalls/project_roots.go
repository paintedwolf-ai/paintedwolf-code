package forbiddencalls

import (
	"errors"
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var projectRoots = []invariants.Invariant{{
	// Tools address files through the resolver hub, which joins every
	// project root with a validated relative path.
	Name:    "tools-join-no-single-root",
	Anchors: []invariants.Anchor{invariants.GoPath("internal/tools/projectpaths/projectpaths.go"), invariants.Func("internal/tools", "ToolContext.ActiveRootPath")},
	Exempt: []invariants.Exemption{
		{Key: "internal/tools/native/gitcmd/file_entries.go:entryAbs", Reason: "git index entries are relative to the repository resolved from the reached directory, -C included"},
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/tools") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Join" || len(call.Args) < 2 {
					return
				}
				base := exprString(call.Args[0])
				switch {
				case strings.Contains(base, "projectDir") || strings.Contains(base, "ProjectDir") || base == "root":
					if singleReturnOf(f, call, "entryAbs") && r.Exempt(f.Site(call.Pos())) {
						return
					}
					r.ReportAt(call.Pos(), "joins the single root %s; resolve through projectpaths", base)
				case strings.Contains(base, "ActiveRootPath"):
					r.ReportAt(call.Pos(), "joins ActiveRootPath; resolve through projectpaths")
				}
			})
		}
	},
}}

// exprString spells an identifier, selector or method-call chain.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
			return exprString(sel.X) + "." + sel.Sel.Name + "()"
		}
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	}
	return ""
}

// singleReturnOf reports call as the whole body of function name in f:
// `return call`.
func singleReturnOf(f *invariants.GoFile, call *ast.CallExpr, name string) bool {
	fn := f.EnclosingFunc(call.Pos())
	if fn == nil || fn.Name.Name != name || len(fn.Body.List) != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	return ok && len(ret.Results) == 1 && ret.Results[0] == call
}

var evidenceSubstrate = []invariants.Invariant{{
	// Runtime evidence has one record type: rows carrying observed handles
	// or typed gate outcomes.
	Name:    "one-runtime-evidence-record",
	Anchors: []invariants.Anchor{invariants.Decl("internal/evidence", "Record"), invariants.Func("internal/evidence", "Resolve")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			if invariants.Under(f, "internal/logoutline") || f.Rel == "internal/evidence/record.go" {
				continue
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if spec, ok := n.(*ast.TypeSpec); ok && RuntimeEvidenceRecord(spec) {
					r.ReportAt(spec.Pos(), "a second runtime evidence Record; use internal/evidence")
				}
				return true
			})
		}
	},
}}

// RuntimeEvidenceRecord reports a Record struct carrying an observed handle
// or a typed gate outcome. A manifest persistence Record shares only the name.
func RuntimeEvidenceRecord(spec *ast.TypeSpec) bool {
	st, ok := spec.Type.(*ast.StructType)
	if spec.Name.Name != "Record" || !ok {
		return false
	}
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if name.Name == "Handle" || name.Name == "GateType" {
				return true
			}
		}
	}
	return false
}

// ecosystemDirectoryNames must not decide project traversal.
var ecosystemDirectoryNames = map[string]bool{
	"node_modules": true, "bower_components": true, "vendor": true,
	"dist": true, "build": true, "out": true, "target": true, "bin": true,
	"obj": true, ".next": true, ".nuxt": true, ".turbo": true, ".parcel-cache": true,
	".cache": true, "__pycache__": true, ".venv": true, "venv": true,
	"site-packages": true, ".gradle": true, ".tox": true, "Pods": true,
	"coverage": true, ".pytest_cache": true, ".mypy_cache": true,
}

var projectWalk = []invariants.Invariant{{
	// Walk eligibility is decided by path facts: sandbox.ShouldSkipDir prunes
	// VCS and engine metadata by path segment and budgets bound the rest.
	Name:    "project-walk-names-no-ecosystem-directories",
	Anchors: []invariants.Anchor{invariants.Func("internal/sandbox", "ShouldSkipDir")},
	Check: func(r *invariants.Run) {
		dirs := []string{"internal/repochange", "internal/sourcefeed", "internal/session", "internal/sandbox", "internal/workspace", "internal/project"}
		for _, f := range r.GoFiles(invariants.Production, dirs...) {
			subjects := ecosystemLiteralSubjects(r, f)
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := ecosystemLiteral(n)
				if !ok {
					return true
				}
				subject, compared := subjects[lit.Pos()]
				if compared {
					r.ReportAt(lit.Pos(), "compares %s against %s; prune with sandbox.ShouldSkipDir", lit.Value, subject)
				} else {
					r.ReportAt(lit.Pos(), "names %s; prune with sandbox.ShouldSkipDir and bound the rest by budget", lit.Value)
				}
				return true
			})
		}
	},
}, {
	// Session code reaches workflow capabilities through their typed port.
	Name: "session-has-no-workflow-type-assertions",
	Anchors: []invariants.Anchor{invariants.AnchorFunc("a workflows field in internal/session", func(c *invariants.Corpus) error {
		return anyProductionSelector(c, "internal/session", "workflows")
	})},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/session") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if assertion, ok := n.(*ast.TypeAssertExpr); ok {
					if field, ok := assertion.X.(*ast.SelectorExpr); ok && field.Sel.Name == "workflows" {
						r.ReportAt(assertion.Pos(), "type-asserts workflows; use the typed port")
					}
				}
				return true
			})
		}
	},
}}

// ecosystemLiteralSubjects maps each ecosystem literal in a comparison to
// the source of what it is compared against.
func ecosystemLiteralSubjects(r *invariants.Run, f *invariants.GoFile) map[token.Pos]string {
	render := func(node ast.Node) string {
		if node == nil {
			return ""
		}
		return f.Source(r.Go(), node)
	}
	out := map[token.Pos]string{}
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SwitchStmt:
			for _, stmt := range node.Body.List {
				for _, expr := range stmt.(*ast.CaseClause).List {
					if lit, ok := ecosystemLiteral(expr); ok {
						out[lit.Pos()] = render(node.Tag)
					}
				}
			}
		case *ast.BinaryExpr:
			if node.Op != token.EQL && node.Op != token.NEQ {
				return true
			}
			if lit, ok := ecosystemLiteral(node.X); ok {
				out[lit.Pos()] = render(node.Y)
			}
			if lit, ok := ecosystemLiteral(node.Y); ok {
				out[lit.Pos()] = render(node.X)
			}
		}
		return true
	})
	return out
}

func ecosystemLiteral(node ast.Node) (*ast.BasicLit, bool) {
	lit, ok := node.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil, false
	}
	value, err := strconv.Unquote(lit.Value)
	return lit, err == nil && ecosystemDirectoryNames[value]
}

// anyProductionSelector anchors on a selector name used below dir.
func anyProductionSelector(c *invariants.Corpus, dir, name string) error {
	for _, f := range c.Go().Files() {
		if f.IsTest || !invariants.Under(f, dir) {
			continue
		}
		found := false
		ast.Inspect(f.AST, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			found = found || ok && sel.Sel.Name == name
			return !found
		})
		if found {
			return nil
		}
	}
	return errors.New("no selector named " + name + " below " + dir)
}
