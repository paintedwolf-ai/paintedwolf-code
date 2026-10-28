package commandsurface

import (
	"mvdan.cc/sh/v3/syntax"
)

// ArithmeticPart is $(( expression )) over signed 64-bit integers. It never
// assigns: assignment, increment and decrement operators are refused.
type ArithmeticPart struct {
	SyntaxLocation
	Expression ArithmeticNode
}

func (ArithmeticPart) wordNode() {}

// Words lists the operand words of the expression in source order.
func (p ArithmeticPart) Words() []DeferredWord {
	return arithmeticWords(p.Expression)
}

// Words lists the operand words of the command expression in source order.
func (c ArithmeticCommand) Words() []DeferredWord {
	return arithmeticWords(c.Expression)
}

func arithmeticWords(root ArithmeticNode) []DeferredWord {
	var out []DeferredWord
	var visit func(ArithmeticNode)
	visit = func(n ArithmeticNode) {
		switch n := n.(type) {
		case ArithmeticOperand:
			out = append(out, n.Word)
		case ArithmeticUnary:
			visit(n.Operand)
		case ArithmeticBinary:
			visit(n.Left)
			visit(n.Right)
		}
	}
	visit(root)
	return out
}

// ArithmeticNode is an operand, unary or binary arithmetic node. The ternary
// a ? b : c is a binary "?" whose right side is a binary ":".
type ArithmeticNode interface{ arithmeticNode() }

// ArithmeticOperand is an integer literal, a variable name, or a word that
// expands to either.
type ArithmeticOperand struct {
	SyntaxLocation
	Word DeferredWord
}

type ArithmeticUnary struct {
	SyntaxLocation
	Operator string
	Postfix  bool
	Operand  ArithmeticNode
}

type ArithmeticBinary struct {
	SyntaxLocation
	Operator    string
	Left, Right ArithmeticNode
}

func (ArithmeticOperand) arithmeticNode() {}
func (ArithmeticUnary) arithmeticNode()   {}
func (ArithmeticBinary) arithmeticNode()  {}

const arithmeticForms = "$(( )) or (( )) with integer operands and + - * / % ** << >> & | ^ ~ ! comparisons && || ?: and parentheses"

var arithmeticBinaryOperators = map[string]bool{
	"+": true, "-": true, "*": true, "/": true, "%": true, "**": true, "<<": true, ">>": true,
	"&": true, "|": true, "^": true, "==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true,
	"&&": true, "||": true, "?": true, ":": true,
}

var arithmeticAssignOperators = map[string]bool{
	"=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true,
	"<<=": true, ">>=": true, "&=": true, "|=": true, "^=": true,
}

func (c *programCompiler) validateArithmetic(n syntax.Node) {
	switch v := n.(type) {
	case *syntax.ArithmExp:
		if v.Bracket || v.Unsigned {
			c.unsupported(n, arithmeticForms)
			return
		}
		c.validateArithmeticExpr(v.X, false)
	case *syntax.ArithmCmd:
		if v.Unsigned {
			c.unsupported(n, arithmeticForms)
			return
		}
		c.validateArithmeticExpr(v.X, true)
	}
}

func (c *programCompiler) validateArithmeticExpr(expr syntax.ArithmExpr, allowMutation bool) {
	if expr == nil {
		return
	}
	switch v := expr.(type) {
	case *syntax.Word:
		c.markBraces(false, v)
		if allowMutation {
			for _, node := range v.Parts {
				part, ok := node.(*syntax.ParamExp)
				if !ok || !part.Short || part.Dollar.IsValid() || part.Index == nil {
					continue
				}
				if !c.reserveHeap(128, part) {
					return
				}
				if c.arithmeticIndexes == nil {
					c.arithmeticIndexes = make(map[*syntax.ParamExp]bool)
				}
				c.arithmeticIndexes[part] = true
			}
		}
	case *syntax.ParenArithm:
		c.validateArithmeticExpr(v.X, allowMutation)
	case *syntax.UnaryArithm:
		op := v.Op.String()
		if op == "++" || op == "--" {
			if !allowMutation || !validArithmeticTarget(v.X) {
				c.unsupported(v, arithmeticForms)
				return
			}
			c.validateArithmeticExpr(v.X, allowMutation)
			return
		}
		if v.Post || (op != "+" && op != "-" && op != "!" && op != "~") {
			c.unsupported(v, arithmeticForms)
			return
		}
		c.validateArithmeticExpr(v.X, allowMutation)
	case *syntax.BinaryArithm:
		op := v.Op.String()
		switch {
		case arithmeticBinaryOperators[op] || (allowMutation && op == ","):
			c.validateArithmeticExpr(v.X, allowMutation)
			c.validateArithmeticExpr(v.Y, allowMutation)
		case allowMutation && arithmeticAssignOperators[op] && validArithmeticTarget(v.X):
			c.validateArithmeticExpr(v.X, allowMutation)
			c.validateArithmeticExpr(v.Y, allowMutation)
		default:
			c.unsupported(v, arithmeticForms)
		}
	default:
		c.unsupported(expr, arithmeticForms)
	}
}

func validArithmeticTarget(expr syntax.ArithmExpr) bool {
	w, ok := expr.(*syntax.Word)
	if !ok || len(w.Parts) != 1 {
		return false
	}
	switch part := w.Parts[0].(type) {
	case *syntax.Lit:
		return validWritableVariable(part.Value)
	case *syntax.ParamExp:
		return part.Short && !part.Dollar.IsValid() && part.Index != nil && part.Param != nil && validWritableVariable(part.Param.Value) && subscriptAllSymbol(part.Index) == ""
	default:
		return false
	}
}

func validWritableVariable(name string) bool {
	if name == "" || name == "IFS" || name == "PIPESTATUS" {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if i == 0 {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && ch != '_' {
				return false
			}
			continue
		}
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' {
			return false
		}
	}
	return true
}

func subscriptAllSymbol(expr syntax.ArithmExpr) string {
	w, ok := expr.(*syntax.Word)
	if !ok || len(w.Parts) != 1 {
		return ""
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok || (lit.Value != "@" && lit.Value != "*") {
		return ""
	}
	return lit.Value
}

func (c *programCompiler) arithmetic(expr syntax.ArithmExpr) ArithmeticNode {
	if expr == nil {
		return nil
	}
	loc := c.location(expr)
	switch v := expr.(type) {
	case *syntax.Word:
		return ArithmeticOperand{SyntaxLocation: loc, Word: c.word(v)}
	case *syntax.ParenArithm:
		return c.arithmetic(v.X)
	case *syntax.UnaryArithm:
		return ArithmeticUnary{SyntaxLocation: loc, Operator: v.Op.String(), Postfix: v.Post, Operand: c.arithmetic(v.X)}
	case *syntax.BinaryArithm:
		return ArithmeticBinary{SyntaxLocation: loc, Operator: v.Op.String(), Left: c.arithmetic(v.X), Right: c.arithmetic(v.Y)}
	}
	c.unsupported(expr, arithmeticForms)
	return ArithmeticOperand{SyntaxLocation: loc, Word: DeferredWord{SyntaxLocation: loc, Parts: []WordNode{LiteralPart{SyntaxLocation: loc, Value: "0"}}}}
}

func cloneArithmetic(n ArithmeticNode) ArithmeticNode {
	switch n := n.(type) {
	case ArithmeticOperand:
		n.Word = cloneWord(n.Word)
		return n
	case ArithmeticUnary:
		n.Operand = cloneArithmetic(n.Operand)
		return n
	case ArithmeticBinary:
		n.Left, n.Right = cloneArithmetic(n.Left), cloneArithmetic(n.Right)
		return n
	}
	return n
}
