package commandsurface

import (
	"strconv"

	"mvdan.cc/sh/v3/syntax"
)

func (c *programCompiler) statements(stmts []*syntax.Stmt) []Statement {
	out := make([]Statement, 0, len(stmts))
	for _, s := range stmts {
		if c.err != nil {
			break
		}
		out = append(out, c.statement(s))
	}
	return out
}
func (c *programCompiler) statement(s *syntax.Stmt) Statement {
	out := Statement{SyntaxLocation: c.location(s), Negated: s.Negated, Background: s.Background}
	if s.Cmd != nil {
		out.Command = c.command(s.Cmd)
	}
	for _, r := range s.Redirs {
		out.Redirects = append(out.Redirects, c.redirect(r))
	}
	return out
}
func (c *programCompiler) command(cmd syntax.Command) CommandNode {
	loc := c.location(cmd)
	switch v := cmd.(type) {
	case *syntax.CallExpr:
		return SimpleCommand{SyntaxLocation: loc, Assignments: c.assignments(v.Assigns), Words: c.words(v.Args)}
	case *syntax.DeclClause:
		result := EnvironmentDeclaration{SyntaxLocation: loc}
		for _, a := range v.Args {
			if a.Naked {
				if a.Value != nil {
					result.Words = append(result.Words, c.word(a.Value))
				} else {
					result.Words = append(result.Words, DeferredWord{SyntaxLocation: c.location(a), Parts: []WordNode{LiteralPart{SyntaxLocation: c.location(a.Name), Value: a.Name.Value}}})
				}
			} else {
				result.Assignments = append(result.Assignments, c.assignment(a))
			}
		}
		return result
	case *syntax.Block:
		return StatementGroup{SyntaxLocation: loc, Statements: c.statements(v.Stmts)}
	case *syntax.Subshell:
		return StatementGroup{SyntaxLocation: loc, Isolated: true, Statements: c.statements(v.Stmts)}
	case *syntax.BinaryCmd:
		return BinaryCommand{SyntaxLocation: loc, Operator: v.Op.String(), Left: c.statement(v.X), Right: c.statement(v.Y)}
	case *syntax.IfClause:
		return c.conditional(v, loc)
	case *syntax.WhileClause:
		return WhileLoop{SyntaxLocation: loc, Condition: c.statements(v.Cond), Body: c.statements(v.Do)}
	case *syntax.ForClause:
		iter := v.Loop.(*syntax.WordIter)
		return ForLoop{SyntaxLocation: loc, Variable: iter.Name.Value, Positional: !iter.InPos.IsValid(), Items: c.words(iter.Items), Body: c.statements(v.Do)}
	case *syntax.FuncDecl:
		return FunctionDeclaration{SyntaxLocation: loc, Name: v.Name.Value, Body: c.statement(v.Body)}
	case *syntax.TestClause:
		return TestExpression{SyntaxLocation: loc, Expression: c.test(v.X)}
	}
	c.unsupported(cmd, "supported command syntax")
	return nil
}
func (c *programCompiler) conditional(v *syntax.IfClause, loc SyntaxLocation) Conditional {
	out := Conditional{SyntaxLocation: loc, Condition: c.statements(v.Cond), Then: c.statements(v.Then)}
	if v.Else != nil {
		next := c.conditional(v.Else, c.location(v.Else))
		out.Else = &next
	}
	return out
}
func (c *programCompiler) assignments(items []*syntax.Assign) []Assignment {
	out := make([]Assignment, 0, len(items))
	for _, a := range items {
		out = append(out, c.assignment(a))
	}
	return out
}
func (c *programCompiler) assignment(a *syntax.Assign) Assignment {
	out := Assignment{SyntaxLocation: c.location(a), Name: a.Name.Value}
	if a.Value != nil {
		out.Value = c.word(a.Value)
	}
	return out
}
func (c *programCompiler) redirect(r *syntax.Redirect) ProgramRedirect {
	fd := -1
	if r.N != nil {
		fd, _ = strconv.Atoi(r.N.Value)
	}
	out := ProgramRedirect{SyntaxLocation: c.location(r), Operator: r.Op.String(), Descriptor: fd, Target: c.word(r.Word)}
	if r.Hdoc != nil {
		word := c.word(r.Hdoc)
		out.HereDocument = &word
	}
	return out
}
func (c *programCompiler) words(items []*syntax.Word) []DeferredWord {
	out := make([]DeferredWord, 0, len(items))
	for _, w := range items {
		out = append(out, c.word(w))
	}
	return out
}
func (c *programCompiler) word(w *syntax.Word) DeferredWord {
	if w == nil {
		return DeferredWord{}
	}
	return DeferredWord{SyntaxLocation: c.location(w), Parts: c.parts(w.Parts)}
}
func (c *programCompiler) parts(parts []syntax.WordPart) []WordNode {
	out := make([]WordNode, 0, len(parts))
	for _, part := range parts {
		loc := c.location(part)
		switch p := part.(type) {
		case *syntax.Lit:
			out = append(out, LiteralPart{SyntaxLocation: loc, Value: p.Value})
		case *syntax.SglQuoted:
			out = append(out, SingleQuotedPart{SyntaxLocation: loc, Value: p.Value})
		case *syntax.DblQuoted:
			out = append(out, DoubleQuotedPart{SyntaxLocation: loc, Parts: c.parts(p.Parts)})
		case *syntax.ParamExp:
			out = append(out, ParameterPart{SyntaxLocation: loc, Name: p.Param.Value, Length: p.Length})
		case *syntax.CmdSubst:
			out = append(out, SubstitutionPart{SyntaxLocation: loc, Backquotes: p.Backquotes, Statements: c.statements(p.Stmts)})
		}
	}
	return out
}
func (c *programCompiler) test(t syntax.TestExpr) TestNode {
	switch v := t.(type) {
	case *syntax.Word:
		return TestWord{Word: c.word(v)}
	case *syntax.UnaryTest:
		return UnaryPredicate{SyntaxLocation: c.location(v), Operator: v.Op.String(), Operand: c.test(v.X)}
	case *syntax.BinaryTest:
		return BinaryPredicate{SyntaxLocation: c.location(v), Operator: v.Op.String(), Left: c.test(v.X), Right: c.test(v.Y)}
	case *syntax.ParenTest:
		return GroupedPredicate{SyntaxLocation: c.location(v), Expression: c.test(v.X)}
	}
	c.unsupported(t, "a supported test expression")
	return nil
}
