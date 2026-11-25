package pongoplain

import "strings"

// Atoms lists e's Boolean leaves in source order. not, and and or are
// connectives; every other node is one atom.
func (e *Expr) Atoms() []*Expr {
	switch e.Kind {
	case ExprNot:
		return e.Left.Atoms()
	case ExprAnd, ExprOr:
		return append(e.Left.Atoms(), e.Right.Atoms()...)
	}
	return []*Expr{e}
}

// Source prints e as Pongo expression text with compound operands
// parenthesised.
func (e *Expr) Source() string {
	switch e.Kind {
	case ExprLiteral:
		return e.Literal
	case ExprNot:
		return "not " + e.Left.operand()
	case ExprAnd, ExprOr, ExprCompare:
		return e.Left.operand() + " " + e.Op + " " + e.Right.operand()
	case ExprFilter:
		var b strings.Builder
		b.WriteString(e.Left.operand())
		b.WriteString("|" + e.FilterName)
		if e.FilterArg != nil {
			b.WriteString(":" + e.FilterArg.operand())
		}
		return b.String()
	case ExprTernary:
		out := e.Left.operand() + " if " + e.Cond.operand()
		if e.Right != nil {
			out += " else " + e.Right.operand()
		}
		return out
	default: // ExprIdent
		return e.Ident
	}
}

func (e *Expr) operand() string {
	switch e.Kind {
	case ExprIdent, ExprLiteral, ExprFilter:
		return e.Source()
	}
	return "(" + e.Source() + ")"
}

// Decide evaluates e's Boolean skeleton with each atom's truth read from
// values by its Source.
func (e *Expr) Decide(values map[string]bool) bool {
	switch e.Kind {
	case ExprNot:
		return !e.Left.Decide(values)
	case ExprAnd:
		return e.Left.Decide(values) && e.Right.Decide(values)
	case ExprOr:
		return e.Left.Decide(values) || e.Right.Decide(values)
	}
	return values[e.Source()]
}
