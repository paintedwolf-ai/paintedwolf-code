package oarcore

// Appendix A, section A.1: the frozen grammar of the condition language.
//
// The parser accepts exactly what the EBNF derives ([OAR-EXPR-1]).

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SyntaxError is a condition the grammar cannot derive. It names the offending
// construct and its offset in the source, as [OAR-EXPR-1] requires.
type SyntaxError struct {
	Message string
	Offset  int
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s at offset %d", e.Message, e.Offset)
}

func syntaxErr(offset int, format string, args ...any) error {
	return &SyntaxError{Message: fmt.Sprintf(format, args...), Offset: offset}
}

// nodeKind discriminates a parse-tree node.
type nodeKind int

const (
	nodeBool nodeKind = iota
	nodeInt
	nodeDouble
	nodeString
	nodeList
	nodeIdent
	nodeCall
	nodeIndex
	nodeUnary
	nodeBinary
	nodeTernary
)

// node is one parse-tree node: a flat union over the kinds [OAR-EXPR-20]
// enumerates for counting.
type node struct {
	minimumInteger bool
	kind           nodeKind
	offset         int

	boolValue   bool
	intValue    int64
	doubleValue float64
	strValue    string // string literal value, identifier name, or call name

	op string // unary or binary operator

	items []*node // list members, or call arguments
	left  *node   // binary left, index target, unary operand, ternary condition
	right *node   // binary right, index subscript, ternary then
	third *node   // ternary else
}

// countNodes counts the parse tree for the [OAR-EXPR-17] limit, exactly as
// [OAR-EXPR-20] defines a node: a literal, an identifier, a call, an index, a
// unary, a binary, or a ternary. A call's arguments, an index's operand and
// subscript, and a list literal's members each count separately, and the list
// literal itself counts as one. Parentheses are not nodes, because they do not
// survive parsing.
func countNodes(n *node) int {
	if n == nil {
		return 0
	}
	total := 1
	if n.minimumInteger {
		total++
	}
	for _, item := range n.items {
		total += countNodes(item)
	}
	total += countNodes(n.left) + countNodes(n.right) + countNodes(n.third)
	return total
}

// walkNodes visits every node of a parse tree, in no particular order.
func walkNodes(n *node, visit func(*node)) {
	if n == nil {
		return
	}
	visit(n)
	for _, item := range n.items {
		walkNodes(item, visit)
	}
	walkNodes(n.left, visit)
	walkNodes(n.right, visit)
	walkNodes(n.third, visit)
}

type tokenKind int

const (
	tokInt tokenKind = iota
	tokDouble
	tokString
	tokName
	tokPunct
	tokEOF
)

type token struct {
	kind tokenKind
	text string // punct text, name text, or numeric source
	str  string // decoded string literal
	at   int
}

var twoCharPunct = []string{"&&", "||", "==", "!=", "<=", ">="}

var oneCharPunct = []string{"(", ")", "[", "]", ",", "?", ":", "<", ">", "!", "+", "-", "*", "/", "%"}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isHex(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// tokenize splits a condition into tokens. Offsets are code-point offsets, so a
// diagnostic points at the same place whatever the host language counts in.
func tokenize(src string) ([]token, error) {
	runes := []rune(src)
	at := func(i int) rune {
		if i < 0 || i >= len(runes) {
			return 0
		}
		return runes[i]
	}

	var out []token
	i := 0
	for i < len(runes) {
		c := runes[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			// [OAR-EXPR-24] Whitespace between tokens is not significant and is
			// not otherwise part of the language.
			i++
			continue
		}
		// [OAR-EXPR-6] A comment is not part of the language.
		if c == '/' && (at(i+1) == '/' || at(i+1) == '*') {
			return nil, syntaxErr(i, "[OAR-EXPR-6] a comment is not part of the condition language")
		}
		if c == '#' {
			return nil, syntaxErr(i, "[OAR-EXPR-6] a comment is not part of the condition language")
		}
		if c == '"' || c == '\'' {
			tok, next, err := lexString(runes, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tok)
			i = next
			continue
		}
		if isDigit(c) {
			tok, next, err := lexNumber(runes, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tok)
			i = next
			continue
		}
		if isLetter(c) || c == '_' {
			// identifier = name { "." name } — a dotted identifier is a single
			// name ([OAR-EXPR-2]). A name is lexed greedily, the longest run of
			// letter, digit, and `_` beginning at that position, so
			// `international` is one identifier and not `in` followed by
			// `ternational` ([OAR-EXPR-24]).
			start := i
			for {
				for i < len(runes) && (isLetter(runes[i]) || isDigit(runes[i]) || runes[i] == '_') {
					i++
				}
				if at(i) == '.' && (isLetter(at(i+1)) || at(i+1) == '_') {
					i++
					continue
				}
				break
			}
			if at(i) == '.' {
				return nil, syntaxErr(i, "a \".\" must be followed by a name")
			}
			out = append(out, token{kind: tokName, text: string(runes[start:i]), at: start})
			continue
		}
		if c == '{' || c == '}' {
			return nil, syntaxErr(i, "[OAR-EXPR-1] a map literal is not part of the condition language")
		}
		if c == '.' {
			return nil, syntaxErr(i, "[OAR-EXPR-2] there is no field-selection operator; index a map<string,string> or call a typed accessor")
		}
		if i+1 < len(runes) {
			two := string(runes[i : i+2])
			if contains(twoCharPunct, two) {
				out = append(out, token{kind: tokPunct, text: two, at: i})
				i += 2
				continue
			}
		}
		if contains(oneCharPunct, string(c)) {
			out = append(out, token{kind: tokPunct, text: string(c), at: i})
			i++
			continue
		}
		if c == '&' || c == '|' {
			return nil, syntaxErr(i, "bitwise %q is not part of the condition language", string(c))
		}
		if c == '=' {
			return nil, syntaxErr(i, "[OAR-EXPR-1] assignment is not part of the condition language")
		}
		return nil, syntaxErr(i, "unexpected character %q", string(c))
	}
	out = append(out, token{kind: tokEOF, at: len(runes)})
	return out, nil
}

// lexString reads a quoted literal. The escape set is closed ([OAR-EXPR-3]):
// anything outside it is rejected, never passed through.
func lexString(runes []rune, start int) (token, int, error) {
	quote := runes[start]
	i := start + 1
	var b strings.Builder
	for {
		if i >= len(runes) {
			return token{}, 0, syntaxErr(start, "unterminated string literal")
		}
		ch := runes[i]
		if ch == '\n' || ch == '\r' {
			return token{}, 0, syntaxErr(i, "[OAR-EXPR-1] literal newline in string")
		}
		if ch == quote {
			i++
			break
		}
		if ch == '\\' {
			if i+1 >= len(runes) {
				return token{}, 0, syntaxErr(i, "[OAR-EXPR-3] unsupported string escape \\")
			}
			esc := runes[i+1]
			switch esc {
			case '\\':
				b.WriteRune('\\')
			case '"':
				b.WriteRune('"')
			case '\'':
				b.WriteRune('\'')
			case 'n':
				b.WriteRune('\n')
			case 'r':
				b.WriteRune('\r')
			case 't':
				b.WriteRune('\t')
			case 'u':
				// \u is followed by exactly four hexadecimal digits.
				if i+5 >= len(runes) {
					return token{}, 0, syntaxErr(i, "[OAR-EXPR-3] \\u must be followed by exactly four hexadecimal digits")
				}
				hex := runes[i+2 : i+6]
				for _, h := range hex {
					if !isHex(h) {
						return token{}, 0, syntaxErr(i, "[OAR-EXPR-3] \\u must be followed by exactly four hexadecimal digits")
					}
				}
				code, err := strconv.ParseUint(string(hex), 16, 32)
				if err != nil {
					return token{}, 0, syntaxErr(i, "[OAR-EXPR-3] \\u must be followed by exactly four hexadecimal digits")
				}
				b.WriteRune(rune(code)) // #nosec G115 -- \\uXXXX is at most 0xFFFF
				i += 6
				continue
			default:
				return token{}, 0, syntaxErr(i, "[OAR-EXPR-3] unsupported string escape \\%s", string(esc))
			}
			i += 2
			continue
		}
		b.WriteRune(ch)
		i++
	}
	return token{kind: tokString, str: b.String(), at: start}, i, nil
}

// lexNumber reads an int or a double. `double` requires digits on both sides of
// the point, and the grammar has no exponent form and no unsigned suffix.
func lexNumber(runes []rune, start int) (token, int, error) {
	at := func(i int) rune {
		if i < 0 || i >= len(runes) {
			return 0
		}
		return runes[i]
	}
	i := start
	for i < len(runes) && isDigit(runes[i]) {
		i++
	}
	if at(i) == '.' && isDigit(at(i+1)) {
		i++
		for i < len(runes) && isDigit(runes[i]) {
			i++
		}
		if at(i) == '.' {
			return token{}, 0, syntaxErr(start, "malformed number literal")
		}
		if at(i) == 'e' || at(i) == 'E' {
			return token{}, 0, syntaxErr(i, "[OAR-EXPR-1] exponent notation is not part of the condition language")
		}
		return token{kind: tokDouble, text: string(runes[start:i]), at: start}, i, nil
	}
	if at(i) == '.' {
		return token{}, 0, syntaxErr(start, "malformed number literal")
	}
	if at(i) == 'e' || at(i) == 'E' {
		return token{}, 0, syntaxErr(i, "[OAR-EXPR-1] exponent notation is not part of the condition language")
	}
	if at(i) == 'u' || at(i) == 'U' {
		return token{}, 0, syntaxErr(i, "[OAR-EXPR-1] an unsigned literal is not part of the condition language")
	}
	if r := at(i); r != 0 && (isLetter(r) || r == '_') {
		return token{}, 0, syntaxErr(start, "malformed number literal")
	}
	return token{kind: tokInt, text: string(runes[start:i]), at: start}, i, nil
}

var relops = []string{"==", "!=", "<", "<=", ">", ">="}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	p.pos++
	return t
}

func (p *parser) atPunct(text string) bool {
	t := p.peek()
	return t.kind == tokPunct && t.text == text
}

func (p *parser) expect(text string) error {
	t := p.next()
	if t.kind != tokPunct || t.text != text {
		return syntaxErr(t.at, "expected %q", text)
	}
	return nil
}

// parseCondition parses a condition, or names the offending construct and its
// offset ([OAR-EXPR-1]).
func parseCondition(src string) (*node, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.ternary()
	if err != nil {
		return nil, err
	}
	if end := p.peek(); end.kind != tokEOF {
		return nil, syntaxErr(end.at, "unexpected trailing input")
	}
	return n, nil
}

// ternary = disjunction [ "?" ternary ":" ternary ] — right-associative.
func (p *parser) ternary() (*node, error) {
	cond, err := p.disjunction()
	if err != nil {
		return nil, err
	}
	if !p.atPunct("?") {
		return cond, nil
	}
	at := p.next().at
	then, err := p.ternary()
	if err != nil {
		return nil, err
	}
	if err := p.expect(":"); err != nil {
		return nil, err
	}
	other, err := p.ternary()
	if err != nil {
		return nil, err
	}
	return &node{kind: nodeTernary, offset: at, left: cond, right: then, third: other}, nil
}

func (p *parser) disjunction() (*node, error) {
	left, err := p.conjunction()
	if err != nil {
		return nil, err
	}
	for p.atPunct("||") {
		at := p.next().at
		right, err := p.conjunction()
		if err != nil {
			return nil, err
		}
		left = &node{kind: nodeBinary, op: "||", offset: at, left: left, right: right}
	}
	return left, nil
}

func (p *parser) conjunction() (*node, error) {
	left, err := p.relation()
	if err != nil {
		return nil, err
	}
	for p.atPunct("&&") {
		at := p.next().at
		right, err := p.relation()
		if err != nil {
			return nil, err
		}
		left = &node{kind: nodeBinary, op: "&&", offset: at, left: left, right: right}
	}
	return left, nil
}

// isRelop reports whether a token is a relational operator, including the
// reserved word `in`.
func isRelop(t token) bool {
	if t.kind == tokPunct && contains(relops, t.text) {
		return true
	}
	return t.kind == tokName && t.text == "in"
}

// relation = addition [ relop addition ]. The grammar admits at most one
// relational operator, so `a < b < c` is not derivable and is rejected rather
// than grouped by the precedence table ([OAR-EXPR-4]).
func (p *parser) relation() (*node, error) {
	left, err := p.addition()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if !isRelop(t) {
		return left, nil
	}
	op := t.text
	at := p.next().at
	right, err := p.addition()
	if err != nil {
		return nil, err
	}
	if after := p.peek(); isRelop(after) {
		return nil, syntaxErr(after.at, "[OAR-EXPR-4] a relation admits at most one relational operator")
	}
	return &node{kind: nodeBinary, op: op, offset: at, left: left, right: right}, nil
}

func (p *parser) addition() (*node, error) {
	left, err := p.multiplication()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tokPunct || (t.text != "+" && t.text != "-") {
			return left, nil
		}
		at := p.next().at
		right, err := p.multiplication()
		if err != nil {
			return nil, err
		}
		left = &node{kind: nodeBinary, op: t.text, offset: at, left: left, right: right}
	}
}

func (p *parser) multiplication() (*node, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tokPunct || (t.text != "*" && t.text != "/" && t.text != "%") {
			return left, nil
		}
		at := p.next().at
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		left = &node{kind: nodeBinary, op: t.text, offset: at, left: left, right: right}
	}
}

// unary = [ "!" | "-" ] postfix — exactly zero or one prefix operator, so `!!x`
// and `--x` are outside the grammar.
func (p *parser) unary() (*node, error) {
	t := p.peek()
	if t.kind == tokPunct && (t.text == "!" || t.text == "-") {
		at := p.next().at
		after := p.peek()
		if after.kind == tokPunct && (after.text == "!" || after.text == "-") {
			return nil, syntaxErr(after.at, "[OAR-EXPR-4] a unary operator may not be repeated")
		}
		// [OAR-EXPR-21] The magnitude 9223372036854775808 is admitted as the
		// immediate operand of unary minus, without which the least value of the
		// int type could not be written at all.
		if t.text == "-" && after.kind == tokInt && after.text == "9223372036854775808" {
			p.next()
			return &node{kind: nodeInt, offset: at, intValue: math.MinInt64, minimumInteger: true}, nil
		}
		operand, err := p.postfix()
		if err != nil {
			return nil, err
		}
		return &node{kind: nodeUnary, op: t.text, offset: at, left: operand}, nil
	}
	return p.postfix()
}

func (p *parser) postfix() (*node, error) {
	n, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.atPunct("[") {
		at := p.next().at
		index, err := p.ternary()
		if err != nil {
			return nil, err
		}
		if err := p.expect("]"); err != nil {
			return nil, err
		}
		n = &node{kind: nodeIndex, offset: at, left: n, right: index}
	}
	return n, nil
}

func (p *parser) primary() (*node, error) {
	t := p.next()
	if t.kind == tokPunct && t.text == "(" {
		inner, err := p.ternary()
		if err != nil {
			return nil, err
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		return inner, nil
	}
	if t.kind == tokPunct && t.text == "[" {
		var items []*node
		if !p.atPunct("]") {
			for {
				item, err := p.ternary()
				if err != nil {
					return nil, err
				}
				items = append(items, item)
				if p.atPunct(",") {
					p.next()
					continue
				}
				break
			}
		}
		if err := p.expect("]"); err != nil {
			return nil, err
		}
		return &node{kind: nodeList, offset: t.at, items: items}, nil
	}
	switch t.kind {
	case tokInt:
		// [OAR-EXPR-21] An integer literal that does not fit 64 bits is rejected
		// at load rather than silently losing precision.
		value, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return nil, syntaxErr(t.at, "[OAR-EXPR-21] integer literal %s does not fit int", t.text)
		}
		return &node{kind: nodeInt, offset: t.at, intValue: value}, nil
	case tokDouble:
		// [OAR-EXPR-21] A double literal whose nearest binary64 value is not
		// finite is rejected at load.
		value, err := strconv.ParseFloat(t.text, 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, syntaxErr(t.at, "[OAR-EXPR-21] double literal %s has no finite binary64 value", t.text)
		}
		return &node{kind: nodeDouble, offset: t.at, doubleValue: value}, nil
	case tokString:
		return &node{kind: nodeString, offset: t.at, strValue: t.str}, nil
	case tokName:
		// [OAR-EXPR-24] A reserved word is lexed as itself and never as an
		// identifier.
		if t.text == "true" || t.text == "false" {
			return &node{kind: nodeBool, offset: t.at, boolValue: t.text == "true"}, nil
		}
		if t.text == "in" {
			return nil, syntaxErr(t.at, "\"in\" is a reserved word")
		}
		if p.atPunct("(") {
			p.next()
			var args []*node
			if !p.atPunct(")") {
				for {
					arg, err := p.ternary()
					if err != nil {
						return nil, err
					}
					args = append(args, arg)
					if p.atPunct(",") {
						p.next()
						continue
					}
					break
				}
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			return &node{kind: nodeCall, offset: t.at, strValue: t.text, items: args}, nil
		}
		return &node{kind: nodeIdent, offset: t.at, strValue: t.text}, nil
	case tokPunct, tokEOF:
	}
	return nil, syntaxErr(t.at, "expected an expression")
}
