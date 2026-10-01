package search

import (
	"fmt"
	"strings"
)

type parseState struct {
	tokens []token
	i      int
}

// ParseQuery parses the search DSL into an AST. An operator with nothing to
// join is dropped and an unclosed group runs to the end of the query.
func ParseQuery(query string) (Node, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, newParseError(0, ParseErrSyntax, "", "empty query")
	}
	tokens, err := lex(query)
	if err != nil {
		return nil, err
	}
	p := &parseState{tokens: tokens}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, newParseError(0, ParseErrSyntax, "", "query contains no searchable terms")
	}
	if p.peek().kind != tokEOF {
		t := p.peek()
		return nil, newParseError(t.pos, ParseErrSyntax, "", fmt.Sprintf("unexpected token %q", t.text))
	}
	return node, nil
}

func (p *parseState) peek() token {
	if p.i >= len(p.tokens) {
		return token{kind: tokEOF}
	}
	return p.tokens[p.i]
}

func (p *parseState) advance() token {
	t := p.peek()
	if p.i < len(p.tokens) {
		p.i++
	}
	return t
}

func (p *parseState) parseOr() (Node, error) {
	var exprs []Node
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	if left != nil {
		exprs = append(exprs, left)
	}
	for p.peek().kind == tokOr {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if right != nil {
			exprs = append(exprs, right)
		}
	}
	switch len(exprs) {
	case 0:
		return nil, nil
	case 1:
		return exprs[0], nil
	}
	return OrExpr{Exprs: exprs}, nil
}

func (p *parseState) parseAnd() (Node, error) {
	var exprs []Node
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if left != nil {
		exprs = append(exprs, left)
	}
	for p.canImplicitAnd() {
		if p.peek().kind == tokAnd {
			p.advance()
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if right != nil {
			exprs = append(exprs, right)
		}
	}
	switch len(exprs) {
	case 0:
		return nil, nil
	case 1:
		return exprs[0], nil
	}
	return AndExpr{Exprs: exprs}, nil
}

func (p *parseState) canImplicitAnd() bool {
	switch p.peek().kind {
	case tokAnd, tokNot, tokLParen, tokFilter, tokText:
		return true
	default:
		return false
	}
}

func (p *parseState) parseUnary() (Node, error) {
	if p.peek().kind == tokNot {
		p.advance()
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if inner == nil {
			return nil, nil
		}
		return NotExpr{Expr: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parseState) parsePrimary() (Node, error) {
	switch p.peek().kind {
	case tokLParen:
		p.advance()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind == tokRParen {
			p.advance()
		}
		return inner, nil
	case tokFilter:
		t := p.advance()
		return FilterExpr{Field: t.field, Value: t.text, Offset: t.pos}, nil
	case tokText:
		t := p.advance()
		return TextExpr{Text: t.text, Offset: t.pos, Phrase: t.quoted}, nil
	default:
		// An operator with nothing to join, a closing parenthesis, or the end.
		return nil, nil
	}
}
