package boolexpr

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Parse parses a when-expression: identifiers, and, or, not, parentheses, and
// whitelisted calls name("arg").
func Parse(src string) (Node, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, fmt.Errorf("empty expression")
	}
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := parser{tokens: toks}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().typ != tokEOF {
		return nil, fmt.Errorf("unexpected token %q at position %d", p.peek().lit, p.peek().pos)
	}
	return n, nil
}

// CollectIdents returns every identifier in the tree — the exact keys Eval will
// look up, so a validator resolves what evaluation resolves.
func CollectIdents(n Node) []string {
	var out []string
	walk(n, func(node Node) {
		if v, ok := node.(Ident); ok {
			out = append(out, v.Name)
		}
	})
	return out
}

func walk(n Node, fn func(Node)) {
	if n == nil {
		return
	}
	fn(n)
	switch v := n.(type) {
	case Not:
		walk(v.Expr, fn)
	case And:
		walk(v.Left, fn)
		walk(v.Right, fn)
	case Or:
		walk(v.Left, fn)
		walk(v.Right, fn)
	}
}

// ValidateWhitelist rejects AST node kinds outside the allowed sandbox.
func ValidateWhitelist(n Node) error {
	var bad string
	walk(n, func(node Node) {
		if bad != "" {
			return
		}
		switch node.(type) {
		case Ident, Not, And, Or:
		default:
			bad = fmt.Sprintf("%T", node)
		}
	})
	if bad != "" {
		return fmt.Errorf("forbidden node type %s", bad)
	}
	return nil
}

// ValidateSimpleIdents restricts identifiers to letters, digits, underscore and
// colon. Callers whose environment is a registry do not need it — an unregistered
// id is refused by name. Callers resolving against a fixed map do: there an
// attribute walk or template filter reads as a missing key and evaluates false.
func ValidateSimpleIdents(n Node) error {
	var bad string
	walk(n, func(node Node) {
		if bad != "" {
			return
		}
		v, ok := node.(Ident)
		if !ok {
			return
		}
		if !isSimpleIdent(v.Name) {
			bad = v.Name
		}
	})
	if bad != "" {
		return fmt.Errorf("identifier %q is outside the plain name grammar", bad)
	}
	return nil
}

func isSimpleIdent(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// Eval evaluates the tree using a primitive lookup. Unknown identifiers are false (fail closed).
func Eval(n Node, env func(string) bool) bool {
	switch v := n.(type) {
	case Ident:
		if env == nil {
			return false
		}
		return env(v.Name)
	case Not:
		return !Eval(v.Expr, env)
	case And:
		return Eval(v.Left, env) && Eval(v.Right, env)
	case Or:
		return Eval(v.Left, env) || Eval(v.Right, env)
	default:
		return false
	}
}

type tokenType int

const (
	tokEOF tokenType = iota
	tokIdent
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen
)

type token struct {
	typ tokenType
	lit string
	pos int
}

type parser struct {
	tokens []token
	i      int
}

func (p *parser) peek() token {
	if p.i >= len(p.tokens) {
		return token{typ: tokEOF}
	}
	return p.tokens[p.i]
}

func (p *parser) advance() token {
	t := p.peek()
	if p.i < len(p.tokens) {
		p.i++
	}
	return t
}

func (p *parser) expect(typ tokenType) (token, error) {
	t := p.advance()
	if t.typ != typ {
		return t, fmt.Errorf("expected %v, got %q at %d", typ, t.lit, t.pos)
	}
	return t, nil
}

func (p *parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tokOr {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = Or{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tokAnd {
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = And{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (Node, error) {
	if p.peek().typ == tokNot {
		p.advance()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return Not{Expr: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Node, error) {
	switch p.peek().typ {
	case tokLParen:
		p.advance()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen); err != nil {
			return nil, err
		}
		return inner, nil
	case tokIdent:
		return Ident{Name: p.advance().lit}, nil
	default:
		t := p.peek()
		return nil, fmt.Errorf("unexpected token %q at %d", t.lit, t.pos)
	}
}

// isDelimiter reports whether a rune ends an identifier run. Everything that is
// not structure is identifier text, so a parameterized condition id keeps
// whatever its value contains: var_equals:plan.status,draft, a path, a glob.
func isDelimiter(r rune) bool {
	switch r {
	case '(', ')', '"':
		return true
	}
	return unicode.IsSpace(r)
}

func tokenize(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		r := rune(src[i])
		if unicode.IsSpace(r) {
			i++
			continue
		}
		pos := i
		switch src[i] {
		case '(':
			toks = append(toks, token{typ: tokLParen, lit: "(", pos: pos})
			i++
			continue
		case ')':
			toks = append(toks, token{typ: tokRParen, lit: ")", pos: pos})
			i++
			continue
		}
		j := i
		for j < len(src) {
			r, size := utf8.DecodeRuneInString(src[j:])
			if isDelimiter(r) {
				break
			}
			j += size
		}
		if j == i {
			return nil, fmt.Errorf("invalid character %q at %d", src[i], pos)
		}
		lit := src[i:j]
		i = j
		switch lit {
		case "and":
			toks = append(toks, token{typ: tokAnd, lit: lit, pos: pos})
		case "or":
			toks = append(toks, token{typ: tokOr, lit: lit, pos: pos})
		case "not":
			toks = append(toks, token{typ: tokNot, lit: lit, pos: pos})
		default:
			toks = append(toks, token{typ: tokIdent, lit: lit, pos: pos})
		}
	}
	toks = append(toks, token{typ: tokEOF})
	return toks, nil
}
