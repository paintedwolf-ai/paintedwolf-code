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
	visit(p.Expression)
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

const arithmeticForms = "$(( )) with integer operands and + - * / % ** << >> & | ^ ~ ! comparisons && || ?: and parentheses, without assignment"

var arithmeticBinaryOperators = map[string]bool{
	"+": true, "-": true, "*": true, "/": true, "%": true, "**": true, "<<": true, ">>": true,
	"&": true, "|": true, "^": true, "==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true,
	"&&": true, "||": true, "?": true, ":": true,
}

func (c *programCompiler) validateArithmetic(n syntax.Node) {
	switch v := n.(type) {
	case *syntax.ArithmExp:
		if v.Bracket || v.Unsigned {
			c.unsupported(n, arithmeticForms)
		}
		if word, ok := v.X.(*syntax.Word); ok {
			c.markArithmeticWord(word)
		}
	case *syntax.ParenArithm:
		if word, ok := v.X.(*syntax.Word); ok {
			c.markArithmeticWord(word)
		}
	case *syntax.UnaryArithm:
		if op := v.Op.String(); v.Post || (op != "+" && op != "-" && op != "!" && op != "~") {
			c.unsupported(n, arithmeticForms)
		}
		if word, ok := v.X.(*syntax.Word); ok {
			c.markArithmeticWord(word)
		}
	case *syntax.BinaryArithm:
		if !arithmeticBinaryOperators[v.Op.String()] {
			c.unsupported(n, arithmeticForms)
		}
		for _, side := range []syntax.ArithmExpr{v.X, v.Y} {
			if word, ok := side.(*syntax.Word); ok {
				c.markArithmeticWord(word)
			}
		}
	}
}

// Case words and patterns never undergo brace expansion.
func (c *programCompiler) markPatternWord(word *syntax.Word) { c.markArithmeticWord(word) }

// Arithmetic operand words never undergo brace expansion.
func (c *programCompiler) markArithmeticWord(word *syntax.Word) {
	if !c.reserveHeap(256, word) {
		return
	}
	if c.testWords == nil {
		c.testWords = make(map[*syntax.Word]bool)
	}
	c.testWords[word] = true
}

func (c *programCompiler) arithmetic(expr syntax.ArithmExpr) ArithmeticNode {
	loc := c.location(expr)
	switch v := expr.(type) {
	case *syntax.Word:
		return ArithmeticOperand{SyntaxLocation: loc, Word: c.word(v)}
	case *syntax.ParenArithm:
		return c.arithmetic(v.X)
	case *syntax.UnaryArithm:
		return ArithmeticUnary{SyntaxLocation: loc, Operator: v.Op.String(), Operand: c.arithmetic(v.X)}
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
