package detectionpack

import (
	"fmt"
	"strings"
	"unicode"
)

type conditionNode interface {
	eval(sels map[string]selection, lookup func(string) (any, bool)) bool
}

type nodeSelection struct {
	name string
}

func (n nodeSelection) eval(sels map[string]selection, lookup func(string) (any, bool)) bool {
	s, ok := sels[n.name]
	if !ok {
		return false
	}
	return s.match(lookup)
}

type nodeAnd struct {
	left, right conditionNode
}

func (n nodeAnd) eval(sels map[string]selection, lookup func(string) (any, bool)) bool {
	return n.left.eval(sels, lookup) && n.right.eval(sels, lookup)
}

type nodeOr struct {
	left, right conditionNode
}

func (n nodeOr) eval(sels map[string]selection, lookup func(string) (any, bool)) bool {
	return n.left.eval(sels, lookup) || n.right.eval(sels, lookup)
}

type nodeNot struct {
	inner conditionNode
}

func (n nodeNot) eval(sels map[string]selection, lookup func(string) (any, bool)) bool {
	return !n.inner.eval(sels, lookup)
}

type nodeOneOf struct {
	names []string
}

func (n nodeOneOf) eval(sels map[string]selection, lookup func(string) (any, bool)) bool {
	for _, name := range n.names {
		if s, ok := sels[name]; ok && s.match(lookup) {
			return true
		}
	}
	return false
}

type nodeAllOf struct {
	names []string
}

func (n nodeAllOf) eval(sels map[string]selection, lookup func(string) (any, bool)) bool {
	if len(n.names) == 0 {
		return false
	}
	for _, name := range n.names {
		s, ok := sels[name]
		if !ok || !s.match(lookup) {
			return false
		}
	}
	return true
}

type condTokenKind int

const (
	tokIdent condTokenKind = iota
	tokIdentStar
	tokLParen
	tokRParen
	tokAnd
	tokOr
	tokNot
	tokOneOf
	tokAllOf
	tokThem
	tokEOF
)

type condToken struct {
	kind condTokenKind
	text string
}

type condParser struct {
	tokens []condToken
	pos    int
	sels   map[string]selection
}

func parseCondition(expr string, sels map[string]selection) (conditionNode, error) {
	if err := rejectAggregations(expr); err != nil {
		return nil, err
	}
	tokens, err := tokenizeCondition(expr)
	if err != nil {
		return nil, fmt.Errorf("unparsable condition: %w", err)
	}
	p := &condParser{tokens: tokens, sels: sels}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("unparsable condition: unexpected token %q", p.peek().text)
	}
	return node, nil
}

func rejectAggregations(expr string) error {
	lower := strings.ToLower(expr)
	// Pipe aggregation, near, timeframe — rejected loudly.
	if strings.Contains(expr, "|") {
		return fmt.Errorf("aggregations are not supported")
	}
	for _, bad := range []string{" near ", " timeframe ", " near\t", "\tnear ", " timeframe\t", "\ttimeframe "} {
		if strings.Contains(lower, bad) {
			return fmt.Errorf("aggregations are not supported")
		}
	}
	// Also catch at word boundaries when the expression is just the keyword.
	fields := strings.Fields(lower)
	for _, f := range fields {
		if f == "near" || f == "timeframe" {
			return fmt.Errorf("aggregations are not supported")
		}
	}
	return nil
}

func tokenizeCondition(expr string) ([]condToken, error) {
	var out []condToken
	i := 0
	for i < len(expr) {
		for i < len(expr) && unicode.IsSpace(rune(expr[i])) {
			i++
		}
		if i >= len(expr) {
			break
		}
		c := expr[i]
		switch c {
		case '(':
			out = append(out, condToken{kind: tokLParen, text: "("})
			i++
			continue
		case ')':
			out = append(out, condToken{kind: tokRParen, text: ")"})
			i++
			continue
		}
		// Read a word.
		j := i
		for j < len(expr) {
			r := rune(expr[j])
			if unicode.IsSpace(r) || r == '(' || r == ')' {
				break
			}
			j++
		}
		word := expr[i:j]
		i = j
		lower := strings.ToLower(word)
		switch lower {
		case "and":
			out = append(out, condToken{kind: tokAnd, text: word})
		case "or":
			out = append(out, condToken{kind: tokOr, text: word})
		case "not":
			out = append(out, condToken{kind: tokNot, text: word})
		case "them":
			out = append(out, condToken{kind: tokThem, text: word})
		case "1":
			// Expect "of"
			for i < len(expr) && unicode.IsSpace(rune(expr[i])) {
				i++
			}
			if !hasKeywordAt(expr, i, "of") {
				return nil, fmt.Errorf("expected 'of' after '1'")
			}
			i += 2
			out = append(out, condToken{kind: tokOneOf, text: "1 of"})
		case "all":
			for i < len(expr) && unicode.IsSpace(rune(expr[i])) {
				i++
			}
			if hasKeywordAt(expr, i, "of") {
				i += 2
				out = append(out, condToken{kind: tokAllOf, text: "all of"})
			} else {
				// "all" alone is not valid as a selection name typically, but treat as ident.
				out = append(out, identToken(word))
			}
		default:
			out = append(out, identToken(word))
		}
	}
	out = append(out, condToken{kind: tokEOF, text: ""})
	return out, nil
}

func hasKeywordAt(expr string, i int, kw string) bool {
	if i+len(kw) > len(expr) {
		return false
	}
	if !strings.EqualFold(expr[i:i+len(kw)], kw) {
		return false
	}
	end := i + len(kw)
	if end < len(expr) {
		r := rune(expr[end])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return false
		}
	}
	return true
}

func identToken(word string) condToken {
	if strings.HasSuffix(word, "*") {
		base := word[:len(word)-1]
		return condToken{kind: tokIdentStar, text: base}
	}
	return condToken{kind: tokIdent, text: word}
}

func (p *condParser) peek() condToken {
	if p.pos >= len(p.tokens) {
		return condToken{kind: tokEOF}
	}
	return p.tokens[p.pos]
}

func (p *condParser) next() condToken {
	tok := p.peek()
	if tok.kind != tokEOF {
		p.pos++
	}
	return tok
}

func (p *condParser) parseOr() (conditionNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = nodeOr{left: left, right: right}
	}
	return left, nil
}

func (p *condParser) parseAnd() (conditionNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokAnd {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = nodeAnd{left: left, right: right}
	}
	return left, nil
}

func (p *condParser) parseNot() (conditionNode, error) {
	if p.peek().kind == tokNot {
		p.next()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return nodeNot{inner: inner}, nil
	}
	return p.parsePrimary()
}

func (p *condParser) parsePrimary() (conditionNode, error) {
	tok := p.peek()
	switch tok.kind {
	case tokLParen:
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, fmt.Errorf("unparsable condition: expected ')'")
		}
		p.next()
		return inner, nil
	case tokOneOf:
		p.next()
		names, err := p.parseSelectorGlob()
		if err != nil {
			return nil, err
		}
		return nodeOneOf{names: names}, nil
	case tokAllOf:
		p.next()
		names, err := p.parseSelectorGlob()
		if err != nil {
			return nil, err
		}
		return nodeAllOf{names: names}, nil
	case tokIdent:
		p.next()
		if _, ok := p.sels[tok.text]; !ok {
			return nil, fmt.Errorf("unknown selection: %s", tok.text)
		}
		return nodeSelection{name: tok.text}, nil
	default:
		return nil, fmt.Errorf("unparsable condition: unexpected token %q", tok.text)
	}
}

func (p *condParser) parseSelectorGlob() ([]string, error) {
	tok := p.peek()
	switch tok.kind {
	case tokThem:
		p.next()
		names := make([]string, 0, len(p.sels))
		for name := range p.sels {
			names = append(names, name)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("selector matched no selections: them")
		}
		return names, nil
	case tokIdentStar:
		p.next()
		prefix := tok.text
		var names []string
		for name := range p.sels {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("selector matched no selections: %s*", prefix)
		}
		return names, nil
	default:
		return nil, fmt.Errorf("unparsable condition: expected selector after of")
	}
}
