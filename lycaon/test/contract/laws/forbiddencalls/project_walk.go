package forbiddencalls

import (
	"errors"
	"go/ast"
	"go/token"
	"strconv"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// ecosystemDirectoryNames must not decide project traversal.
var ecosystemDirectoryNames = map[string]bool{
	"node_modules": true, "bower_components": true, "vendor": true,
	"dist": true, "build": true, "out": true, "target": true, "bin": true,
	"obj": true, ".next": true, ".nuxt": true, ".turbo": true, ".parcel-cache": true,
	".cache": true, "__pycache__": true, ".venv": true, "venv": true,
	"site-packages": true, ".gradle": true, ".tox": true, "Pods": true,
	"coverage": true, ".pytest_cache": true, ".mypy_cache": true,
}

var projectWalk = []laws.Rule{{
	// Walk eligibility is decided by path facts: sandbox.ShouldSkipDir prunes
	// VCS and engine metadata by path segment and budgets bound the rest.
	Name:    "project-walk-names-no-ecosystem-directories",
	Anchors: []laws.Anchor{laws.Func("internal/sandbox", "ShouldSkipDir")},
	Check: func(r *laws.Run) {
		dirs := []string{"internal/repochange", "internal/sourcefeed", "internal/session", "internal/sandbox", "internal/workspace", "internal/project"}
		for _, f := range r.GoFiles(laws.Production, dirs...) {
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
	Anchors: []laws.Anchor{laws.AnchorFunc("a workflows field in internal/session", func(c *laws.Corpus) error {
		for _, f := range c.Go().Files() {
			if f.IsTest || !laws.Under(f, "internal/session") {
				continue
			}
			found := false
			ast.Inspect(f.AST, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				found = found || ok && sel.Sel.Name == "workflows"
				return !found
			})
			if found {
				return nil
			}
		}
		return errors.New("no selector named workflows")
	})},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/session") {
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
func ecosystemLiteralSubjects(r *laws.Run, f *laws.GoFile) map[token.Pos]string {
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
