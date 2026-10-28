package commandsurface

import (
	"strconv"
	"strings"

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
		if span, ok := c.trapSpans[v]; ok {
			out := TrapCommand{SyntaxLocation: loc}
			if file := c.traps[v]; file != nil {
				prev := c.spanOverride
				c.spanOverride = &span
				out.Statements = c.statements(file.Stmts)
				c.spanOverride = prev
			}
			return out
		}
		return SimpleCommand{SyntaxLocation: loc, Assignments: c.assignments(v.Assigns), Words: c.words(v.Args)}
	case *syntax.DeclClause:
		result := EnvironmentDeclaration{SyntaxLocation: loc}
		for _, a := range v.Args {
			if a.Naked {
				if a.Value != nil {
					result.Words = append(result.Words, c.word(a.Value))
				} else {
					result.Words = append(result.Words, DeferredWord{SyntaxLocation: c.location(a), Parts: []WordNode{LiteralPart{SyntaxLocation: c.location(a.Name), Value: strings.Clone(a.Name.Value)}}})
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
		return WhileLoop{SyntaxLocation: loc, Condition: c.statements(v.Cond), Body: c.statements(v.Do), Until: v.Until}
	case *syntax.ForClause:
		iter := v.Loop.(*syntax.WordIter)
		return ForLoop{SyntaxLocation: loc, Variable: strings.Clone(iter.Name.Value), Positional: !iter.InPos.IsValid(), Items: c.words(iter.Items), Body: c.statements(v.Do)}
	case *syntax.CaseClause:
		out := CaseStatement{SyntaxLocation: loc, Word: c.word(v.Word)}
		for _, item := range v.Items {
			out.Items = append(out.Items, CaseItem{SyntaxLocation: c.location(item), Patterns: c.words(item.Patterns), Body: c.statements(item.Stmts)})
		}
		return out
	case *syntax.FuncDecl:
		return FunctionDeclaration{SyntaxLocation: loc, Name: strings.Clone(v.Name.Value), Body: c.statement(v.Body)}
	case *syntax.TestClause:
		return TestExpression{SyntaxLocation: loc, Expression: c.test(v.X)}
	case *syntax.ArithmCmd:
		return ArithmeticCommand{SyntaxLocation: loc, Expression: c.arithmetic(v.X)}
	case *syntax.TimeClause:
		return TimedCommand{SyntaxLocation: loc, POSIX: v.PosixFormat, Pipeline: c.statements(timedStatements(v))}
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
	out := Assignment{SyntaxLocation: c.location(a), Name: strings.Clone(a.Name.Value), Append: a.Append}
	if a.Index != nil {
		out.Index = c.arithmetic(a.Index)
	}
	if a.Value != nil {
		out.Value = c.word(a.Value)
	}
	if a.Array != nil {
		out.Array = c.words(elementWords(a.Array.Elems))
	}
	return out
}

// elementWords are an array literal's element values in order.
func elementWords(elems []*syntax.ArrayElem) []*syntax.Word {
	out := make([]*syntax.Word, len(elems))
	for i, elem := range elems {
		out[i] = elem.Value
	}
	return out
}

// timedStatements is the pipeline after time; a bare time has none.
func timedStatements(v *syntax.TimeClause) []*syntax.Stmt {
	if v.Stmt == nil {
		return nil
	}
	return []*syntax.Stmt{v.Stmt}
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
	loc := c.location(w)
	split := c.braces.split[w]
	if split == nil {
		return DeferredWord{SyntaxLocation: loc, Parts: c.parts(w.Parts)}
	}
	// Split literals keep their whole original literal's offsets, so every
	// node of a brace word carries the word's span.
	prev := c.spanOverride
	c.spanOverride = &loc.Span
	defer func() { c.spanOverride = prev }()
	return DeferredWord{SyntaxLocation: loc, Parts: c.parts(nonEmptyParts(split.Parts))}
}
func (c *programCompiler) parts(parts []syntax.WordPart) []WordNode {
	out := make([]WordNode, 0, len(parts))
	for _, part := range parts {
		loc := c.location(part)
		switch p := part.(type) {
		case *syntax.Lit:
			out = append(out, LiteralPart{SyntaxLocation: loc, Value: strings.Clone(p.Value)})
		case *syntax.SglQuoted:
			out = append(out, SingleQuotedPart{SyntaxLocation: loc, Value: strings.Clone(p.Value)})
		case *syntax.DblQuoted:
			out = append(out, DoubleQuotedPart{SyntaxLocation: loc, Parts: c.parts(p.Parts)})
		case *syntax.ParamExp:
			out = append(out, c.parameter(p, loc))
		case *syntax.ArithmExp:
			out = append(out, ArithmeticPart{SyntaxLocation: loc, Expression: c.arithmetic(p.X)})
		case *syntax.CmdSubst:
			out = append(out, SubstitutionPart{SyntaxLocation: loc, Backquotes: p.Backquotes, Statements: c.statements(p.Stmts)})
		case *syntax.ProcSubst:
			out = append(out, SubstitutionPart{SyntaxLocation: loc, Operator: p.Op.String(), Statements: c.statements(p.Stmts)})
		case *syntax.BraceExp:
			out = append(out, c.brace(p, loc))
		}
	}
	return out
}
func (c *programCompiler) parameter(p *syntax.ParamExp, loc SyntaxLocation) ParameterPart {
	out := ParameterPart{SyntaxLocation: loc, Name: strings.Clone(p.Param.Value), Length: p.Length}
	if p.Index != nil {
		if sym := subscriptAllSymbol(p.Index); sym != "" {
			out.Subscript = sym
		} else {
			out.Index = c.arithmetic(p.Index)
		}
	}
	operand := func(w *syntax.Word) *DeferredWord {
		word := c.word(w)
		if w == nil {
			word.SyntaxLocation = loc
		}
		return &word
	}
	switch {
	case p.Slice != nil:
		out.Slice = &ParameterSlice{
			Offset: c.arithmetic(p.Slice.Offset),
			Length: c.arithmetic(p.Slice.Length),
		}
	case p.Exp != nil:
		out.Operator = p.Exp.Op.String()
		if p.Exp.Word != nil || parameterNeedsWord(out.Operator) {
			out.Argument = operand(p.Exp.Word)
		}
	case p.Repl != nil:
		out.Operator = "/"
		if p.Repl.All {
			out.Operator = "//"
		}
		pattern := operand(p.Repl.Orig)
		if anchor, rest, ok := replacementAnchor(pattern.Parts); ok && !p.Repl.All {
			out.Operator += anchor
			pattern.Parts = rest
		}
		out.Argument, out.Replacement = pattern, operand(p.Repl.With)
	}
	return out
}

// parameterNeedsWord reports operators whose absent word is an empty word.
func parameterNeedsWord(operator string) bool {
	switch operator {
	case "^", "^^", ",", ",,":
		return false
	}
	return true
}

// replacementAnchor splits an unquoted leading # or % from a replacement
// pattern, which anchors the match at the start or end of the value.
func replacementAnchor(parts []WordNode) (string, []WordNode, bool) {
	if len(parts) == 0 {
		return "", parts, false
	}
	literal, ok := parts[0].(LiteralPart)
	if !ok || literal.Value == "" || (literal.Value[0] != '#' && literal.Value[0] != '%') {
		return "", parts, false
	}
	anchor := literal.Value[:1]
	rest := append([]WordNode(nil), parts...)
	if literal.Value == anchor {
		rest = rest[1:]
	} else {
		literal.Value = literal.Value[1:]
		rest[0] = literal
	}
	return anchor, rest, true
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
