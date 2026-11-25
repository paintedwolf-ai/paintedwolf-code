package pongoplain

import "strings"

// Atoms lists e's Boolean leaves in source order. and, or and a not at
// Boolean level are connectives; every other node, including a not inside a
// comparison or arithmetic operand, is one atom.
func (e *Expr) Atoms() []*Expr {
	switch e.Kind {
	case ExprNot:
		return e.Left.Atoms()
	case ExprAnd, ExprOr:
		return append(e.Left.Atoms(), e.Right.Atoms()...)
	default:
	}
	return []*Expr{e}
}

// Grammar levels, loosest first, following pongo2's parser functions.
const (
	levelExpression = iota
	levelRelational
	levelSimple
	levelTerm
	levelPower
	levelFactor
)

func (e *Expr) level() int {
	switch e.Kind {
	case ExprAnd, ExprOr:
		return levelExpression
	case ExprCompare:
		return levelRelational
	case ExprNot, ExprNeg:
		return levelSimple
	case ExprArith:
		switch e.Op {
		case "+", "-":
			return levelSimple
		case "^":
			return levelPower
		}
		return levelTerm
	default:
	}
	return levelFactor
}

// Source prints e as Pongo expression text that pongo2 parses into the same
// tree, parenthesising an operand only where its grammar position requires.
func (e *Expr) Source() string {
	switch e.Kind {
	case ExprLiteral:
		return e.Literal
	case ExprIdent:
		return e.Ident
	case ExprNot:
		return "not " + e.Left.at(levelTerm)
	case ExprNeg:
		// pongo2 reads the sign before not, so -not x needs no parentheses.
		if e.Left.Kind == ExprNot {
			return "-" + e.Left.Source()
		}
		return "-" + e.Left.at(levelTerm)
	case ExprAnd, ExprOr:
		return e.Left.at(levelRelational) + " " + e.Op + " " + e.Right.at(levelExpression)
	case ExprCompare:
		if e.Op == "in" {
			return e.Left.at(levelSimple) + " in " + e.Right.at(levelSimple)
		}
		return e.Left.at(levelSimple) + " " + e.Op + " " + e.Right.at(levelRelational)
	case ExprArith:
		switch e.level() {
		case levelSimple:
			return e.Left.at(levelSimple) + " " + e.Op + " " + e.Right.at(levelTerm)
		case levelTerm:
			return e.Left.at(levelTerm) + " " + e.Op + " " + e.Right.at(levelPower)
		}
		return e.Left.at(levelFactor) + " ^ " + e.Right.at(levelPower)
	default:
	}
	var b strings.Builder
	b.WriteString(e.Left.Source())
	b.WriteString("|" + e.FilterName)
	if e.FilterArg != nil {
		b.WriteString(":" + e.FilterArg.Source())
	}
	return b.String()
}

// at prints e for a grammar position that accepts level min or tighter.
func (e *Expr) at(min int) string {
	if e.level() < min {
		return "(" + e.Source() + ")"
	}
	return e.Source()
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
	default:
	}
	return values[e.Source()]
}
