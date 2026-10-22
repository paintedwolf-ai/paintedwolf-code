package parser

import (
	"github.com/benhoyt/goawk/internal/ast"
	"github.com/benhoyt/goawk/lexer"
)

// Construct is one program feature an embedding host may refuse before
// execution, at the start of the statement that contains it.
type Construct struct {
	// Kind is "system", "output-pipe", "input-pipe", "getline-file",
	// "output-file" or "regex".
	Kind string
	// Regex is the literal pattern text of a "regex" construct.
	Regex  string
	Line   int
	Column int
}

// Constructs lists every system() call, command pipe in either direction,
// getline from a file, print or printf redirection to a file, and regex
// literal, in source order. It inspects the parsed program only.
func (p *Program) Constructs() []Construct {
	v := &inspector{}
	for _, stmts := range p.Begin {
		ast.WalkStmtList(v, stmts)
	}
	for _, action := range p.Actions {
		ast.WalkExprList(v, action.Pattern)
		ast.WalkStmtList(v, action.Stmts)
	}
	for _, stmts := range p.End {
		ast.WalkStmtList(v, stmts)
	}
	for _, function := range p.Functions {
		v.at = function.Pos
		ast.WalkStmtList(v, function.Body)
	}
	return v.found
}

type inspector struct {
	at    lexer.Position
	found []Construct
}

func (v *inspector) add(kind, regex string) {
	v.found = append(v.found, Construct{Kind: kind, Regex: regex, Line: v.at.Line, Column: v.at.Column})
}

func (v *inspector) Visit(node ast.Node) ast.Visitor {
	if stmt, ok := node.(ast.Stmt); ok {
		v.at = stmt.StartPos()
	}
	switch n := node.(type) {
	case *ast.CallExpr:
		if n.Func == lexer.F_SYSTEM {
			v.add("system", "")
		}
	case *ast.GetlineExpr:
		if n.Command != nil {
			v.add("input-pipe", "")
		}
		if n.File != nil {
			v.add("getline-file", "")
		}
	case *ast.PrintStmt:
		v.redirect(n.Redirect, n.Dest)
	case *ast.PrintfStmt:
		v.redirect(n.Redirect, n.Dest)
	case *ast.RegExpr:
		v.add("regex", n.Regex)
	}
	return v
}

func (v *inspector) redirect(redirect lexer.Token, dest ast.Expr) {
	if dest == nil {
		return
	}
	if redirect == lexer.PIPE {
		v.add("output-pipe", "")
		return
	}
	v.add("output-file", "")
}
