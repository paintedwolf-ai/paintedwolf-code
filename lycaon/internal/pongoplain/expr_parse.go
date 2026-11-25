package pongoplain

import (
	"fmt"
	"strconv"
	"strings"
)

// ExprKind classifies a parsed Pongo expression node.
type ExprKind string

const (
	ExprOr      ExprKind = "or"
	ExprAnd     ExprKind = "and"
	ExprNot     ExprKind = "not"
	ExprCompare ExprKind = "compare"
	ExprArith   ExprKind = "arith"
	ExprNeg     ExprKind = "neg"
	ExprFilter  ExprKind = "filter"
	ExprIdent   ExprKind = "ident"
	ExprLiteral ExprKind = "literal"
)

// Expr is a node in a parsed Pongo expression tree. Its shape is pongo2's:
// and/or share one precedence and group to the right, comparisons other than
// in group to the right, not and unary minus apply to the first term of an
// additive chain, and ^ groups to the right.
type Expr struct {
	Kind       ExprKind
	Op         string
	Ident      string
	Literal    string
	Left       *Expr
	Right      *Expr
	FilterName string
	FilterArg  *Expr
}

// Identifiers returns every variable or dotted selector in e.
func (e *Expr) Identifiers() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*Expr)
	walk = func(n *Expr) {
		if n == nil {
			return
		}
		if n.Kind == ExprIdent && !seen[n.Ident] {
			seen[n.Ident] = true
			out = append(out, n.Ident)
		}
		walk(n.Left)
		walk(n.Right)
		walk(n.FilterArg)
	}
	walk(e)
	return out
}

// HasLogicalOrCompare reports whether e applies a boolean, comparison or
// arithmetic operator or is a boolean literal.
func (e *Expr) HasLogicalOrCompare() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case ExprOr, ExprAnd, ExprNot, ExprCompare, ExprArith, ExprNeg:
		return true
	case ExprLiteral:
		if e.Literal == "true" || e.Literal == "false" {
			return true
		}
	}
	return e.Left.HasLogicalOrCompare() ||
		e.Right.HasLogicalOrCompare() ||
		e.FilterArg.HasLogicalOrCompare()
}

type tokKind uint8

const (
	tokEOF tokKind = iota
	tokIdent
	tokString
	tokNumber
	tokSymbol
)

type exprToken struct {
	kind tokKind
	val  string
	pos  int
	end  int
}

// exprKeywords are pongo2's keyword tokens; they never name a variable.
var exprKeywords = map[string]bool{
	"in": true, "and": true, "or": true, "not": true,
	"true": true, "false": true, "as": true, "export": true,
}

// exprSymbols is pongo2's symbol table in match order, without the tag
// delimiters.
var exprSymbols = []string{
	"==", ">=", "<=", "&&", "||", "!=", "<>",
	"(", ")", "+", "-", "*", "<", ">", "/", "^", ",", ".", "!", "|", ":", "=", "%", "[", "]",
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// lexExpr tokenizes src as pongo2's lexer does inside a tag: a run of word
// characters is a number when every character is a digit and an identifier
// otherwise, and strings decode only \" and \\.
func lexExpr(src string) ([]exprToken, error) {
	var out []exprToken
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			return nil, fmt.Errorf("newline in expression %q", src)
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '"' || c == '\'':
			start := i
			i++
			for {
				if i >= len(src) {
					return nil, fmt.Errorf("unterminated string literal at offset %d", start)
				}
				ch := src[i]
				if ch == c {
					break
				}
				switch ch {
				case '\\':
					if i+1 >= len(src) || (src[i+1] != '"' && src[i+1] != '\\') {
						return nil, fmt.Errorf("unknown escape sequence in string literal at offset %d", i)
					}
					i += 2
					continue
				case '\n':
					return nil, fmt.Errorf("newline in string literal at offset %d", start)
				}
				i++
			}
			val := strings.ReplaceAll(src[start+1:i], `\"`, `"`)
			val = strings.ReplaceAll(val, `\\`, `\`)
			i++
			out = append(out, exprToken{kind: tokString, val: val, pos: start, end: i})
			continue
		case isWordByte(c):
			start := i
			digits := true
			for i < len(src) && isWordByte(src[i]) {
				if src[i] < '0' || src[i] > '9' {
					digits = false
				}
				i++
			}
			kind := tokIdent
			if digits {
				kind = tokNumber
			}
			out = append(out, exprToken{kind: kind, val: src[start:i], pos: start, end: i})
			continue
		}
		matched := false
		for _, sym := range exprSymbols {
			if strings.HasPrefix(src[i:], sym) {
				out = append(out, exprToken{kind: tokSymbol, val: sym, pos: i, end: i + len(sym)})
				i += len(sym)
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("unexpected character %q in expression %q", c, src)
		}
	}
	return out, nil
}

type exprParser struct {
	toks []exprToken
	pos  int
}

func (p *exprParser) peek() exprToken {
	if p.pos >= len(p.toks) {
		return exprToken{kind: tokEOF}
	}
	return p.toks[p.pos]
}

func (p *exprParser) next() exprToken {
	tk := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return tk
}

// match consumes the next token when it has kind and one of vals.
func (p *exprParser) match(kind tokKind, vals ...string) (string, bool) {
	tk := p.peek()
	if tk.kind != kind {
		return "", false
	}
	for _, v := range vals {
		if tk.val == v {
			p.pos++
			return v, true
		}
	}
	return "", false
}

func (p *exprParser) keyword(vals ...string) (string, bool) {
	return p.match(tokIdent, vals...)
}

func (p *exprParser) symbol(vals ...string) (string, bool) {
	return p.match(tokSymbol, vals...)
}

// ParseExpression parses raw into pongo2's expression tree, failing closed on
// any form pongo2 refuses, on subscripts and function calls, and on trailing
// tokens.
func ParseExpression(raw string) (*Expr, error) {
	raw = strings.TrimSpace(raw)
	toks, err := lexExpr(raw)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("empty expression")
	}
	p := &exprParser{toks: toks}
	node, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if rem := p.peek(); rem.kind != tokEOF {
		return nil, fmt.Errorf("unexpected token %q in %q", rem.val, raw)
	}
	return node, nil
}

func (p *exprParser) parseExpression() (*Expr, error) {
	left, err := p.parseRelational()
	if err != nil {
		return nil, err
	}
	op, ok := p.keyword("and", "or")
	if !ok {
		op, ok = p.symbol("&&", "||")
	}
	if !ok {
		return left, nil
	}
	right, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if op == "&&" || op == "and" {
		return &Expr{Kind: ExprAnd, Op: "and", Left: left, Right: right}, nil
	}
	return &Expr{Kind: ExprOr, Op: "or", Left: left, Right: right}, nil
}

func (p *exprParser) parseRelational() (*Expr, error) {
	left, err := p.parseSimple()
	if err != nil {
		return nil, err
	}
	if op, ok := p.symbol("==", "<=", ">=", "!=", "<>", ">", "<"); ok {
		right, err := p.parseRelational()
		if err != nil {
			return nil, err
		}
		if op == "<>" {
			op = "!="
		}
		return &Expr{Kind: ExprCompare, Op: op, Left: left, Right: right}, nil
	}
	if _, ok := p.keyword("in"); ok {
		right, err := p.parseSimple()
		if err != nil {
			return nil, err
		}
		return &Expr{Kind: ExprCompare, Op: "in", Left: left, Right: right}, nil
	}
	return left, nil
}

func (p *exprParser) parseSimple() (*Expr, error) {
	sign, _ := p.symbol("+", "-")
	_, negate := p.symbol("!")
	if !negate {
		_, negate = p.keyword("not")
	}
	node, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	if negate {
		node = &Expr{Kind: ExprNot, Op: "not", Left: node}
	}
	if sign == "-" {
		node = &Expr{Kind: ExprNeg, Op: "-", Left: node}
	}
	for {
		op, ok := p.symbol("+", "-")
		if !ok {
			return node, nil
		}
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		node = &Expr{Kind: ExprArith, Op: op, Left: node, Right: right}
	}
}

func (p *exprParser) parseTerm() (*Expr, error) {
	node, err := p.parsePower()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.symbol("*", "/", "%")
		if !ok {
			return node, nil
		}
		right, err := p.parsePower()
		if err != nil {
			return nil, err
		}
		node = &Expr{Kind: ExprArith, Op: op, Left: node, Right: right}
	}
}

func (p *exprParser) parsePower() (*Expr, error) {
	base, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	if _, ok := p.symbol("^"); !ok {
		return base, nil
	}
	exp, err := p.parsePower()
	if err != nil {
		return nil, err
	}
	return &Expr{Kind: ExprArith, Op: "^", Left: base, Right: exp}, nil
}

func (p *exprParser) parseFactor() (*Expr, error) {
	if _, ok := p.symbol("("); !ok {
		return p.parseFiltered()
	}
	inner, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if _, ok := p.symbol(")"); !ok {
		return nil, fmt.Errorf("expected ')', got %q", p.peek().val)
	}
	return inner, nil
}

func (p *exprParser) parseFiltered() (*Expr, error) {
	node, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.symbol("|"); !ok {
			return node, nil
		}
		name := p.next()
		if name.kind != tokIdent || exprKeywords[name.val] {
			return nil, fmt.Errorf("expected filter name after '|', got %q", name.val)
		}
		var arg *Expr
		if _, ok := p.symbol(":"); ok {
			if arg, err = p.parseOperand(); err != nil {
				return nil, err
			}
		}
		node = &Expr{Kind: ExprFilter, Left: node, FilterName: name.val, FilterArg: arg}
	}
}

// parseOperand reads a variable or a literal: pongo2's filter base and filter
// argument.
func (p *exprParser) parseOperand() (*Expr, error) {
	tk := p.next()
	switch tk.kind {
	case tokEOF:
		return nil, fmt.Errorf("unexpected end of expression")
	case tokString:
		return &Expr{Kind: ExprLiteral, Literal: quoteLiteral(tk.val)}, nil
	case tokNumber:
		if _, ok := p.symbol("."); ok {
			frac := p.next()
			if frac.kind != tokNumber {
				return nil, fmt.Errorf("expected a number after '%s.'", tk.val)
			}
			return &Expr{Kind: ExprLiteral, Literal: tk.val + "." + frac.val}, nil
		}
		if _, err := strconv.Atoi(tk.val); err != nil {
			return nil, fmt.Errorf("integer literal %s: %w", tk.val, err)
		}
		return &Expr{Kind: ExprLiteral, Literal: tk.val}, nil
	case tokIdent:
		if tk.val == "true" || tk.val == "false" {
			return &Expr{Kind: ExprLiteral, Literal: tk.val}, nil
		}
		if exprKeywords[tk.val] {
			return nil, fmt.Errorf("unexpected keyword %q in expression", tk.val)
		}
		return p.parseSelector(tk.val)
	}
	return nil, fmt.Errorf("unexpected token %q", tk.val)
}

func (p *exprParser) parseSelector(root string) (*Expr, error) {
	var b strings.Builder
	b.WriteString(root)
	for {
		if _, ok := p.symbol("."); ok {
			part := p.next()
			if (part.kind != tokIdent && part.kind != tokNumber) || exprKeywords[part.val] {
				return nil, fmt.Errorf("unexpected token %q after '.' in a variable name", part.val)
			}
			b.WriteString("." + part.val)
			continue
		}
		if tk := p.peek(); tk.kind == tokSymbol && (tk.val == "[" || tk.val == "(") {
			return nil, fmt.Errorf("unsupported subscript or call %q after %s", tk.val, b.String())
		}
		return &Expr{Kind: ExprIdent, Ident: b.String()}, nil
	}
}

// quoteLiteral writes s as a pongo2 string literal.
func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
