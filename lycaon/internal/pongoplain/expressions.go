package pongoplain

import (
	"fmt"
	"strings"
)

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
	lines := newLineCursor(source)
	cursor := 0
	for _, loc := range locs {
		if loc[0] > cursor {
			line, _ := lines.at(cursor)
			appendNode(&TemplateNode{Kind: NodeText, Line: line, Text: source[cursor:loc[0]]})
		}
		cursor = loc[1]
		region := source[loc[0]:loc[1]]
		if isOpaqueRegion(region) {
			continue
		}
		line, _ := lines.at(loc[0])
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
		// The remaining region kind is a control tag.
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
		line, _ := lines.at(cursor)
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
		if tk.kind == tokSymbol && tk.val == "," {
			continue
		}
		if tk.kind == tokIdent && !pongoKeywords[tk.val] {
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
	if err != nil || len(toks) == 0 || toks[0].kind != tokString || toks[0].val == "" {
		return "", nil, fmt.Errorf("include target must be a static string literal")
	}
	dep := toks[0].val
	tail := strings.TrimSpace(rest[toks[0].end:])
	if tail == "if_exists" {
		return dep, nil, nil
	}
	if strings.HasPrefix(tail, "if_exists ") {
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "if_exists "))
	}
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
		afterDot := i > 0 && toks[i-1].kind == tokSymbol && toks[i-1].val == "."
		if toks[i].kind == tokIdent && !afterDot && toks[i+1].kind == tokSymbol && toks[i+1].val == "=" {
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
