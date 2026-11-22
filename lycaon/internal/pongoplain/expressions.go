package pongoplain

import (
	"fmt"
	"strings"
	"unicode"
)

// ExprKind classifies a parsed Pongo expression node.
type ExprKind string

const (
	ExprOr      ExprKind = "or"
	ExprAnd     ExprKind = "and"
	ExprNot     ExprKind = "not"
	ExprCompare ExprKind = "compare"
	ExprTernary ExprKind = "ternary"
	ExprFilter  ExprKind = "filter"
	ExprIdent   ExprKind = "ident"
	ExprLiteral ExprKind = "literal"
)

// Expr is a node in a parsed Pongo expression tree.
type Expr struct {
	Kind       ExprKind
	Op         string
	Ident      string
	Literal    string
	Left       *Expr
	Right      *Expr
	Cond       *Expr
	FilterName string
	FilterArg  *Expr
}

// Identifiers returns every non-keyword variable or dotted selector in e.
func (e *Expr) Identifiers() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*Expr)
	walk = func(n *Expr) {
		if n == nil {
			return
		}
		if n.Kind == ExprIdent && n.Ident != "" && !seen[n.Ident] {
			seen[n.Ident] = true
			out = append(out, n.Ident)
		}
		walk(n.Cond)
		walk(n.Left)
		walk(n.Right)
		walk(n.FilterArg)
	}
	walk(e)
	return out
}

// HasLogicalOrCompare reports whether e applies boolean, comparison, or inline
// conditional operators.
func (e *Expr) HasLogicalOrCompare() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case ExprOr, ExprAnd, ExprNot, ExprCompare, ExprTernary:
		return true
	case ExprLiteral:
		switch e.Literal {
		case "true", "false", "True", "False":
			return true
		}
	}
	return e.Cond.HasLogicalOrCompare() ||
		e.Left.HasLogicalOrCompare() ||
		e.Right.HasLogicalOrCompare() ||
		e.FilterArg.HasLogicalOrCompare()
}

// NodeKind classifies a structural node in a parsed Pongo template.
type NodeKind string

const (
	NodeText    NodeKind = "text"
	NodeOutput  NodeKind = "output"
	NodeSet     NodeKind = "set"
	NodeInclude NodeKind = "include"
	NodeIf      NodeKind = "if"
	NodeFor     NodeKind = "for"
	NodeWith    NodeKind = "with"
)

// Binding is a local variable assignment in {% set %} or {% with %}.
type Binding struct {
	Name    string
	RawExpr string
	Expr    *Expr
}

// CondBranch is one arm of an {% if %} / {% elif %} / {% else %} chain.
type CondBranch struct {
	Tag     string
	Line    int
	RawExpr string
	Expr    *Expr
	Body    []*TemplateNode
}

// TemplateNode is one block or inline construct in a parsed template tree.
type TemplateNode struct {
	Kind       NodeKind
	Line       int
	Text       string
	RawExpr    string
	Expr       *Expr
	IncludeRef string
	LoopVars   []string
	Bindings   []Binding
	Branches   []CondBranch
	Body       []*TemplateNode
	EmptyBody  []*TemplateNode
}

// TemplateTree is the parsed block and expression AST of a Pongo template.
type TemplateTree struct {
	Nodes []*TemplateNode
}

type stackFrame struct {
	kind    string
	line    int
	node    *TemplateNode
	inEmpty bool
}

// ParseTemplateTree parses source into a structured block and expression AST,
// failing closed on unknown executable tags, unbalanced blocks, or unhandled
// expression syntax.
func ParseTemplateTree(source string) (*TemplateTree, error) {
	root := &TemplateTree{}
	var stack []*stackFrame

	appendNode := func(n *TemplateNode) {
		if len(stack) == 0 {
			root.Nodes = append(root.Nodes, n)
			return
		}
		top := stack[len(stack)-1]
		switch top.kind {
		case "if":
			br := &top.node.Branches[len(top.node.Branches)-1]
			br.Body = append(br.Body, n)
		case "for":
			if top.inEmpty {
				top.node.EmptyBody = append(top.node.EmptyBody, n)
			} else {
				top.node.Body = append(top.node.Body, n)
			}
		case "with", "autoescape":
			top.node.Body = append(top.node.Body, n)
		}
	}

	locs := templateRegion.FindAllStringIndex(source, -1)
	cursor := 0
	for _, loc := range locs {
		if loc[0] > cursor {
			line, _ := lineColAt(source, cursor)
			appendNode(&TemplateNode{Kind: NodeText, Line: line, Text: source[cursor:loc[0]]})
		}
		cursor = loc[1]
		region := source[loc[0]:loc[1]]
		if isOpaqueRegion(region) {
			continue
		}
		line, _ := lineColAt(source, loc[0])
		if strings.HasPrefix(region, "{{") {
			raw := trimDelimiters(region, "{{", "}}")
			if raw == "" {
				return nil, fmt.Errorf("line %d: empty output expression", line)
			}
			expr, err := ParseExpression(raw)
			if err != nil {
				return nil, fmt.Errorf("line %d: output {{ %s }}: %w", line, raw, err)
			}
			appendNode(&TemplateNode{Kind: NodeOutput, Line: line, RawExpr: raw, Expr: expr})
			continue
		}
		if !strings.HasPrefix(region, "{%") {
			continue
		}
		body := trimDelimiters(region, "{%", "%}")
		fields := strings.Fields(body)
		if len(fields) == 0 {
			return nil, fmt.Errorf("line %d: empty control tag", line)
		}
		tag := fields[0]
		rest := strings.TrimSpace(body[len(tag):])
		if err := applyParsedControlTag(tag, rest, line, &stack, appendNode); err != nil {
			return nil, err
		}
	}
	if cursor < len(source) {
		line, _ := lineColAt(source, cursor)
		appendNode(&TemplateNode{Kind: NodeText, Line: line, Text: source[cursor:]})
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return nil, fmt.Errorf("line %d: unclosed {%% %s %%} block", top.line, top.kind)
	}
	return root, nil
}

func applyParsedControlTag(tag, rest string, line int, stack *[]*stackFrame, appendNode func(*TemplateNode)) error {
	switch tag {
	case "if", "elif", "else", "endif":
		return applyIfControlTag(tag, rest, line, stack, appendNode)
	case "for":
		loopVars, iterRaw, err := splitForClause(rest)
		if err != nil {
			return fmt.Errorf("line %d: {%% for %s %%}: %w", line, rest, err)
		}
		expr, err := ParseExpression(iterRaw)
		if err != nil {
			return fmt.Errorf("line %d: {%% for %s %%}: %w", line, rest, err)
		}
		n := &TemplateNode{Kind: NodeFor, Line: line, RawExpr: iterRaw, Expr: expr, LoopVars: loopVars}
		appendNode(n)
		*stack = append(*stack, &stackFrame{kind: "for", line: line, node: n})
	case "empty":
		if len(*stack) == 0 || (*stack)[len(*stack)-1].kind != "for" {
			return fmt.Errorf("line %d: unexpected {%% empty %%} outside {%% for %%}", line)
		}
		(*stack)[len(*stack)-1].inEmpty = true
	case "endfor":
		if len(*stack) == 0 || (*stack)[len(*stack)-1].kind != "for" {
			return fmt.Errorf("line %d: unexpected {%% endfor %%}", line)
		}
		*stack = (*stack)[:len(*stack)-1]
	case "with":
		bindings, err := parseWithClause(rest)
		if err != nil {
			return fmt.Errorf("line %d: {%% with %s %%}: %w", line, rest, err)
		}
		n := &TemplateNode{Kind: NodeWith, Line: line, RawExpr: rest, Bindings: bindings}
		appendNode(n)
		*stack = append(*stack, &stackFrame{kind: "with", line: line, node: n})
	case "endwith":
		if len(*stack) == 0 || (*stack)[len(*stack)-1].kind != "with" {
			return fmt.Errorf("line %d: unexpected {%% endwith %%}", line)
		}
		*stack = (*stack)[:len(*stack)-1]
	case "set":
		name, rhsRaw, ok := splitSetAssignment(rest)
		if !ok || rhsRaw == "" {
			return fmt.Errorf("line %d: malformed {%% set %s %%}", line, rest)
		}
		expr, err := ParseExpression(rhsRaw)
		if err != nil {
			return fmt.Errorf("line %d: {%% set %s %%}: %w", line, rest, err)
		}
		appendNode(&TemplateNode{
			Kind: NodeSet, Line: line, RawExpr: rest, Expr: expr,
			Bindings: []Binding{{Name: name, RawExpr: rhsRaw, Expr: expr}},
		})
	case "include":
		dep, bindings, err := parseIncludeClause(rest)
		if err != nil {
			return fmt.Errorf("line %d: {%% include %s %%}: %w", line, rest, err)
		}
		appendNode(&TemplateNode{Kind: NodeInclude, Line: line, RawExpr: rest, IncludeRef: dep, Bindings: bindings})
	case "autoescape":
		if rest != "on" && rest != "off" {
			return fmt.Errorf("line %d: invalid {%% autoescape %s %%}", line, rest)
		}
		n := &TemplateNode{Kind: NodeWith, Line: line, RawExpr: "autoescape " + rest}
		appendNode(n)
		*stack = append(*stack, &stackFrame{kind: "autoescape", line: line, node: n})
	case "endautoescape":
		if len(*stack) == 0 || (*stack)[len(*stack)-1].kind != "autoescape" {
			return fmt.Errorf("line %d: unexpected {%% endautoescape %%}", line)
		}
		*stack = (*stack)[:len(*stack)-1]
	case "templatetag":
		appendNode(&TemplateNode{Kind: NodeText, Line: line, Text: ""})
	default:
		return fmt.Errorf("line %d: unsupported or unhandled executable tag %q", line, tag)
	}
	return nil
}

func applyIfControlTag(tag, rest string, line int, stack *[]*stackFrame, appendNode func(*TemplateNode)) error {
	switch tag {
	case "if":
		expr, err := ParseExpression(rest)
		if err != nil {
			return fmt.Errorf("line %d: {%% if %s %%}: %w", line, rest, err)
		}
		n := &TemplateNode{
			Kind:     NodeIf,
			Line:     line,
			Branches: []CondBranch{{Tag: "if", Line: line, RawExpr: rest, Expr: expr}},
		}
		appendNode(n)
		*stack = append(*stack, &stackFrame{kind: "if", line: line, node: n})
	case "elif":
		if len(*stack) == 0 || (*stack)[len(*stack)-1].kind != "if" {
			return fmt.Errorf("line %d: unexpected {%% elif %%} outside {%% if %%}", line)
		}
		top := (*stack)[len(*stack)-1]
		if top.node.Branches[len(top.node.Branches)-1].Tag == "else" {
			return fmt.Errorf("line %d: {%% elif %%} after {%% else %%}", line)
		}
		expr, err := ParseExpression(rest)
		if err != nil {
			return fmt.Errorf("line %d: {%% elif %s %%}: %w", line, rest, err)
		}
		top.node.Branches = append(top.node.Branches, CondBranch{Tag: "elif", Line: line, RawExpr: rest, Expr: expr})
	case "else":
		if rest != "" {
			return fmt.Errorf("line %d: unexpected arguments in {%% else %s %%}", line, rest)
		}
		if len(*stack) == 0 || (*stack)[len(*stack)-1].kind != "if" {
			return fmt.Errorf("line %d: unexpected {%% else %%} outside {%% if %%}", line)
		}
		top := (*stack)[len(*stack)-1]
		if top.node.Branches[len(top.node.Branches)-1].Tag == "else" {
			return fmt.Errorf("line %d: duplicate {%% else %%}", line)
		}
		top.node.Branches = append(top.node.Branches, CondBranch{Tag: "else", Line: line})
	case "endif":
		if len(*stack) == 0 || (*stack)[len(*stack)-1].kind != "if" {
			return fmt.Errorf("line %d: unexpected {%% endif %%}", line)
		}
		*stack = (*stack)[:len(*stack)-1]
	}
	return nil
}

func splitForClause(rest string) ([]string, string, error) {
	toks, err := lexExpr(rest)
	if err != nil {
		return nil, "", err
	}
	var vars []string
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		if tk.kind == tokIdent && tk.val == "in" {
			if len(vars) == 0 {
				return nil, "", fmt.Errorf("missing loop variable before 'in'")
			}
			iter := strings.TrimSpace(rest[tk.end:])
			for _, mod := range []string{"reversed", "sorted"} {
				if strings.HasSuffix(iter, " "+mod) {
					iter = strings.TrimSpace(strings.TrimSuffix(iter, " "+mod))
				}
			}
			if iter == "" {
				return nil, "", fmt.Errorf("missing collection expression after 'in'")
			}
			return vars, iter, nil
		}
		if tk.kind == tokPunct && tk.val == "," {
			continue
		}
		if tk.kind == tokIdent && !strings.Contains(tk.val, ".") && !pongoKeywords[tk.val] {
			vars = append(vars, tk.val)
			continue
		}
		return nil, "", fmt.Errorf("invalid loop variable token %q", tk.val)
	}
	return nil, "", fmt.Errorf("missing 'in' keyword")
}

func parseWithClause(rest string) ([]Binding, error) {
	if strings.TrimSpace(rest) == "" {
		return nil, fmt.Errorf("empty with binding")
	}
	toks, err := lexExpr(rest)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(toks); i++ {
		if toks[i].kind == tokIdent && toks[i].val == "as" && i+1 == len(toks)-1 && toks[i+1].kind == tokIdent {
			rawExpr := strings.TrimSpace(rest[:toks[i].pos])
			expr, err := ParseExpression(rawExpr)
			if err != nil {
				return nil, err
			}
			return []Binding{{Name: toks[i+1].val, RawExpr: rawExpr, Expr: expr}}, nil
		}
	}
	return parseKeyValueBindings(rest, toks)
}

func parseIncludeClause(rest string) (string, []Binding, error) {
	rest = strings.TrimSpace(rest)
	if dep, ok := quotedDependency(rest); ok {
		return dep, nil, nil
	}
	toks, err := lexExpr(rest)
	if err != nil || len(toks) == 0 || toks[0].kind != tokString {
		return "", nil, fmt.Errorf("include target must be a static string literal")
	}
	dep := toks[0].val
	tail := strings.TrimSpace(rest[toks[0].end:])
	if tail == "" || tail == "only" {
		return dep, nil, nil
	}
	if strings.HasPrefix(tail, "with ") {
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "with "))
		tail = strings.TrimSpace(strings.TrimSuffix(tail, " only"))
		tailToks, err := lexExpr(tail)
		if err != nil {
			return "", nil, err
		}
		bindings, err := parseKeyValueBindings(tail, tailToks)
		if err != nil {
			return "", nil, err
		}
		return dep, bindings, nil
	}
	return "", nil, fmt.Errorf("unsupported include clause %q", tail)
}

func parseKeyValueBindings(src string, toks []exprToken) ([]Binding, error) {
	var assignIdxs []int
	for i := 0; i+2 < len(toks); i++ {
		if toks[i].kind == tokIdent && !strings.Contains(toks[i].val, ".") && toks[i+1].kind == tokOp && toks[i+1].val == "=" {
			assignIdxs = append(assignIdxs, i)
		}
	}
	if len(assignIdxs) == 0 {
		return nil, fmt.Errorf("expected name=expr binding in %q", src)
	}
	var out []Binding
	for idx, tokPos := range assignIdxs {
		name := toks[tokPos].val
		valStart := toks[tokPos+1].end
		valEnd := len(src)
		if idx+1 < len(assignIdxs) {
			valEnd = toks[assignIdxs[idx+1]].pos
		}
		rawVal := strings.TrimSpace(src[valStart:valEnd])
		expr, err := ParseExpression(rawVal)
		if err != nil {
			return nil, err
		}
		out = append(out, Binding{Name: name, RawExpr: rawVal, Expr: expr})
	}
	return out, nil
}

type tokKind uint8

const (
	tokEOF tokKind = iota
	tokIdent
	tokString
	tokNumber
	tokOp
	tokPunct
)

type exprToken struct {
	kind tokKind
	val  string
	pos  int
	end  int
}

func lexExpr(src string) ([]exprToken, error) {
	var out []exprToken
	for i := 0; i < len(src); {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '"' || c == '\'' {
			quote := c
			start := i
			i++
			var b strings.Builder
			closed := false
			for i < len(src) {
				ch := src[i]
				if ch == '\\' {
					if i+1 >= len(src) {
						return nil, fmt.Errorf("unterminated escape in string literal")
					}
					b.WriteByte(src[i+1])
					i += 2
					continue
				}
				if ch == quote {
					i++
					closed = true
					break
				}
				b.WriteByte(ch)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string literal at offset %d", start)
			}
			out = append(out, exprToken{kind: tokString, val: b.String(), pos: start, end: i})
			continue
		}
		if (c >= '0' && c <= '9') || (c == '-' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9' && (len(out) == 0 || out[len(out)-1].kind == tokOp || out[len(out)-1].kind == tokPunct)) {
			start := i
			if c == '-' {
				i++
			}
			for i < len(src) && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			if i+1 < len(src) && src[i] == '.' && src[i+1] >= '0' && src[i+1] <= '9' {
				i++
				for i < len(src) && src[i] >= '0' && src[i] <= '9' {
					i++
				}
			}
			out = append(out, exprToken{kind: tokNumber, val: src[start:i], pos: start, end: i})
			continue
		}
		if c == '_' || unicode.IsLetter(rune(c)) {
			start := i
			var ident strings.Builder
			for {
				segStart := i
				for i < len(src) && (src[i] == '_' || unicode.IsLetter(rune(src[i])) || unicode.IsDigit(rune(src[i]))) {
					i++
				}
				ident.WriteString(src[segStart:i])
				j := i
				for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
					j++
				}
				if j < len(src) && src[j] == '.' {
					k := j + 1
					for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
						k++
					}
					if k < len(src) && (src[k] == '_' || unicode.IsLetter(rune(src[k]))) {
						ident.WriteByte('.')
						i = k
						continue
					}
				}
				break
			}
			out = append(out, exprToken{kind: tokIdent, val: ident.String(), pos: start, end: i})
			continue
		}
		if i+1 < len(src) {
			two := src[i : i+2]
			switch two {
			case "==", "!=", "<=", ">=", "&&", "||":
				out = append(out, exprToken{kind: tokOp, val: two, pos: i, end: i + 2})
				i += 2
				continue
			}
		}
		switch c {
		case '<', '>', '!', '=', '+', '-', '*', '/', '%':
			out = append(out, exprToken{kind: tokOp, val: string(c), pos: i, end: i + 1})
			i++
		case '(', ')', '|', ':', ',':
			out = append(out, exprToken{kind: tokPunct, val: string(c), pos: i, end: i + 1})
			i++
		default:
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

// ParseExpression parses raw into a typed Pongo expression AST, failing closed
// on any unknown operator or trailing unparsed tokens.
func ParseExpression(raw string) (*Expr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty expression")
	}
	toks, err := lexExpr(raw)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("empty expression")
	}
	p := &exprParser{toks: toks}
	node, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if rem := p.peek(); rem.kind != tokEOF {
		return nil, fmt.Errorf("unexpected token %q in %q", rem.val, raw)
	}
	return node, nil
}

func (p *exprParser) parseTernary() (*Expr, error) {
	left, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if tk := p.peek(); tk.kind == tokIdent && tk.val == "if" {
		p.next()
		cond, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		var alt *Expr
		if elseTk := p.peek(); elseTk.kind == tokIdent && elseTk.val == "else" {
			p.next()
			alt, err = p.parseTernary()
			if err != nil {
				return nil, err
			}
		}
		return &Expr{Kind: ExprTernary, Cond: cond, Left: left, Right: alt}, nil
	}
	return left, nil
}

func (p *exprParser) parseOr() (*Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		tk := p.peek()
		if (tk.kind == tokIdent && tk.val == "or") || (tk.kind == tokOp && tk.val == "||") {
			p.next()
			right, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			left = &Expr{Kind: ExprOr, Op: "or", Left: left, Right: right}
			continue
		}
		return left, nil
	}
}

func (p *exprParser) parseAnd() (*Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for {
		tk := p.peek()
		if (tk.kind == tokIdent && tk.val == "and") || (tk.kind == tokOp && tk.val == "&&") {
			p.next()
			right, err := p.parseNot()
			if err != nil {
				return nil, err
			}
			left = &Expr{Kind: ExprAnd, Op: "and", Left: left, Right: right}
			continue
		}
		return left, nil
	}
}

func (p *exprParser) parseNot() (*Expr, error) {
	tk := p.peek()
	if (tk.kind == tokIdent && tk.val == "not") || (tk.kind == tokOp && tk.val == "!") {
		p.next()
		sub, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &Expr{Kind: ExprNot, Op: "not", Left: sub}, nil
	}
	return p.parseCompare()
}

func (p *exprParser) parseCompare() (*Expr, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		tk := p.peek()
		if tk.kind == tokOp {
			switch tk.val {
			case "==", "!=", "<=", ">=", "<", ">":
				p.next()
				right, err := p.parseAdditive()
				if err != nil {
					return nil, err
				}
				left = &Expr{Kind: ExprCompare, Op: tk.val, Left: left, Right: right}
				continue
			}
		}
		if tk.kind == tokIdent && tk.val == "in" {
			p.next()
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = &Expr{Kind: ExprCompare, Op: "in", Left: left, Right: right}
			continue
		}
		if tk.kind == tokIdent && tk.val == "not" && p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tokIdent && p.toks[p.pos+1].val == "in" {
			p.next()
			p.next()
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = &Expr{Kind: ExprCompare, Op: "not in", Left: left, Right: right}
			continue
		}
		return left, nil
	}
}

func (p *exprParser) parseAdditive() (*Expr, error) {
	left, err := p.parseFilter()
	if err != nil {
		return nil, err
	}
	for {
		tk := p.peek()
		if tk.kind == tokOp && (tk.val == "+" || tk.val == "-" || tk.val == "*" || tk.val == "/" || tk.val == "%") {
			p.next()
			right, err := p.parseFilter()
			if err != nil {
				return nil, err
			}
			left = &Expr{Kind: ExprCompare, Op: tk.val, Left: left, Right: right}
			continue
		}
		return left, nil
	}
}

func (p *exprParser) parseFilter() (*Expr, error) {
	base, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		tk := p.peek()
		if tk.kind != tokPunct || tk.val != "|" {
			return base, nil
		}
		p.next()
		fn := p.next()
		if fn.kind != tokIdent || strings.Contains(fn.val, ".") {
			return nil, fmt.Errorf("expected filter name after '|', got %q", fn.val)
		}
		var arg *Expr
		if colon := p.peek(); colon.kind == tokPunct && colon.val == ":" {
			p.next()
			arg, err = p.parsePrimary()
			if err != nil {
				return nil, err
			}
		}
		base = &Expr{Kind: ExprFilter, Left: base, FilterName: fn.val, FilterArg: arg}
	}
}

func (p *exprParser) parsePrimary() (*Expr, error) {
	tk := p.next()
	switch tk.kind {
	case tokString:
		return &Expr{Kind: ExprLiteral, Literal: fmt.Sprintf("%q", tk.val)}, nil
	case tokNumber:
		return &Expr{Kind: ExprLiteral, Literal: tk.val}, nil
	case tokIdent:
		switch tk.val {
		case "true", "false", "True", "False", "nil", "none", "None", "null":
			return &Expr{Kind: ExprLiteral, Literal: tk.val}, nil
		}
		if pongoKeywords[tk.val] {
			return nil, fmt.Errorf("unexpected keyword %q in expression", tk.val)
		}
		return &Expr{Kind: ExprIdent, Ident: tk.val}, nil
	case tokPunct:
		if tk.val == "(" {
			inner, err := p.parseTernary()
			if err != nil {
				return nil, err
			}
			closeTk := p.next()
			if closeTk.kind != tokPunct || closeTk.val != ")" {
				return nil, fmt.Errorf("expected ')', got %q", closeTk.val)
			}
			return inner, nil
		}
	}
	return nil, fmt.Errorf("unexpected token %q", tk.val)
}
