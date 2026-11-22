package contract

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolvocab"
)

type oarViolation struct {
	Source     string
	Expression string
	Action     string
	Reason     string
}

func (v oarViolation) String() string {
	return fmt.Sprintf("%s | expr=%q | action=%q: %s", v.Source, v.Expression, v.Action, v.Reason)
}

type condNode struct {
	kind  string
	op    string
	ident string
	str   string
	left  *condNode
	right *condNode
	third *condNode
	items []*condNode
}

type condTok struct {
	kind string
	text string
}

var allowedOARFunctions = map[string]bool{
	"size":        true,
	"starts_with": true,
	"ends_with":   true,
	"contains":    true,
	"matches":     true,
}

func tokenizeOARCondition(src string) ([]condTok, error) {
	var out []condTok
	for i := 0; i < len(src); {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '"' || c == '\'' {
			q := c
			j := i + 1
			for j < len(src) {
				if src[j] == '\\' && j+1 < len(src) {
					j += 2
					continue
				}
				if src[j] == q {
					break
				}
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string at %d", i)
			}
			out = append(out, condTok{kind: "str", text: src[i+1 : j]})
			i = j + 1
			continue
		}
		if i+1 < len(src) && slices.Contains([]string{"&&", "||", "==", "!=", "<=", ">="}, src[i:i+2]) {
			out = append(out, condTok{kind: "punct", text: src[i : i+2]})
			i += 2
			continue
		}
		if strings.ContainsRune("()[]?,:!<>+-*/", rune(c)) {
			out = append(out, condTok{kind: "punct", text: src[i : i+1]})
			i++
			continue
		}
		if c >= '0' && c <= '9' {
			j := i
			for j < len(src) && ((src[j] >= '0' && src[j] <= '9') || src[j] == '.') {
				j++
			}
			out = append(out, condTok{kind: "num", text: src[i:j]})
			i = j
			continue
		}
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' {
			j := i
			for j < len(src) && ((src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= 'a' && src[j] <= 'z') || (src[j] >= '0' && src[j] <= '9') || src[j] == '_' || src[j] == '.') {
				j++
			}
			out = append(out, condTok{kind: "ident", text: src[i:j]})
			i = j
			continue
		}
		return nil, fmt.Errorf("unhandled condition token %q at %d", src[i:i+1], i)
	}
	return append(out, condTok{kind: "eof"}), nil
}

type condParser struct {
	toks []condTok
	pos  int
}

func parseOARConditionAST(src string) (*condNode, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	toks, err := tokenizeOARCondition(src)
	if err != nil {
		return nil, err
	}
	p := &condParser{toks: toks}
	root, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != "eof" {
		return nil, fmt.Errorf("unexpected token %q", p.cur().text)
	}
	return root, nil
}

func (p *condParser) cur() condTok { return p.toks[p.pos] }

func (p *condParser) parseTernary() (*condNode, error) {
	cond, err := p.parseOr()
	if err != nil || p.cur().text != "?" {
		return cond, err
	}
	p.pos++
	thenN, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if p.cur().text != ":" {
		return nil, fmt.Errorf("expected ':' in ternary, got %q", p.cur().text)
	}
	p.pos++
	elseN, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	return &condNode{kind: "ternary", left: cond, right: thenN, third: elseN}, nil
}

func (p *condParser) parseOr() (*condNode, error) {
	left, err := p.parseAnd()
	for err == nil && (p.cur().text == "||" || p.cur().text == "or") {
		op := p.cur().text
		p.pos++
		var right *condNode
		right, err = p.parseAnd()
		left = &condNode{kind: "binary", op: op, left: left, right: right}
	}
	return left, err
}

func (p *condParser) parseAnd() (*condNode, error) {
	left, err := p.parseCompare()
	for err == nil && (p.cur().text == "&&" || p.cur().text == "and") {
		op := p.cur().text
		p.pos++
		var right *condNode
		right, err = p.parseCompare()
		left = &condNode{kind: "binary", op: op, left: left, right: right}
	}
	return left, err
}

func (p *condParser) parseCompare() (*condNode, error) {
	left, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	t := p.cur()
	if slices.Contains([]string{"==", "!=", "<", "<=", ">", ">=", "in"}, t.text) {
		p.pos++
		right, rErr := p.parseAdd()
		if rErr != nil {
			return nil, rErr
		}
		return &condNode{kind: "binary", op: t.text, left: left, right: right}, nil
	}
	return left, nil
}

func (p *condParser) parseAdd() (*condNode, error) {
	left, err := p.parseMul()
	for err == nil && (p.cur().text == "+" || p.cur().text == "-") {
		op := p.cur().text
		p.pos++
		var right *condNode
		right, err = p.parseMul()
		left = &condNode{kind: "binary", op: op, left: left, right: right}
	}
	return left, err
}

func (p *condParser) parseMul() (*condNode, error) {
	left, err := p.parseUnary()
	for err == nil && (p.cur().text == "*" || p.cur().text == "/") {
		op := p.cur().text
		p.pos++
		var right *condNode
		right, err = p.parseUnary()
		left = &condNode{kind: "binary", op: op, left: left, right: right}
	}
	return left, err
}

func (p *condParser) parseUnary() (*condNode, error) {
	if p.cur().text == "!" || p.cur().text == "-" || p.cur().text == "not" {
		op := p.cur().text
		p.pos++
		sub, err := p.parseUnary()
		return &condNode{kind: "unary", op: op, left: sub}, err
	}
	return p.parsePrimary()
}

func (p *condParser) parsePrimary() (*condNode, error) {
	t := p.cur()
	switch {
	case t.text == "(":
		p.pos++
		inner, err := p.parseTernary()
		if err != nil || p.cur().text != ")" {
			return nil, fmt.Errorf("unclosed '(' in condition")
		}
		p.pos++
		return inner, nil
	case t.text == "[":
		p.pos++
		var items []*condNode
		for p.cur().text != "]" && p.cur().kind != "eof" {
			item, err := p.parseTernary()
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			if p.cur().text == "," {
				p.pos++
			}
		}
		if p.cur().text != "]" {
			return nil, fmt.Errorf("unclosed '[' in condition")
		}
		p.pos++
		return &condNode{kind: "list", items: items}, nil
	case t.kind == "str":
		p.pos++
		return &condNode{kind: "str", str: t.text}, nil
	case t.kind == "num":
		if _, err := strconv.ParseFloat(t.text, 64); err != nil {
			return nil, err
		}
		p.pos++
		return &condNode{kind: "num", str: t.text}, nil
	case t.kind == "ident":
		p.pos++
		if t.text == "true" || t.text == "false" {
			return &condNode{kind: "bool", str: t.text}, nil
		}
		if p.cur().text == "(" {
			if !allowedOARFunctions[t.text] {
				return nil, fmt.Errorf("unhandled function call %q", t.text)
			}
			p.pos++
			var args []*condNode
			for p.cur().text != ")" && p.cur().kind != "eof" {
				arg, err := p.parseTernary()
				if err != nil {
					return nil, err
				}
				args = append(args, arg)
				if p.cur().text == "," {
					p.pos++
				}
			}
			if p.cur().text != ")" {
				return nil, fmt.Errorf("unclosed call %s", t.text)
			}
			p.pos++
			return &condNode{kind: "call", ident: t.text, items: args}, nil
		}
		return &condNode{kind: "ident", ident: t.text}, nil
	default:
		return nil, fmt.Errorf("unexpected token %q", t.text)
	}
}

func collectCondIdents(n *condNode, out map[string]bool) {
	if n == nil {
		return
	}
	if n.kind == "ident" {
		out[n.ident] = true
	}
	collectCondIdents(n.left, out)
	collectCondIdents(n.right, out)
	collectCondIdents(n.third, out)
	for _, it := range n.items {
		collectCondIdents(it, out)
	}
}

var (
	commandPromiseRE = regexp.MustCompile("(?i)\\b(?:use command|with command|inside command|in command|via command|through command|command redirect|command redirects)\\b|`(?:edit\\s+--path|replace-lines|jq-edit|git\\s+config|sed\\s+-n|cat\\s+-n|rg\\s+-n|grep\\s+-n|wc\\s+-l|unzip\\s+-p|tar\\s+-x|tee\\s+--syntax-override-reason)|KEY='<reference>'")
	proseToolCallRE  = regexp.MustCompile(`(?i)\b(?:call|use|send|retry|run|invoke|prefer)\s+` + "`?" + `([a-z][a-z0-9_]*)\b`)
	backtickToolRE   = regexp.MustCompile("`([a-z][a-z0-9_]*)(?:\\(|`)")
	loaderPromiseRE  = regexp.MustCompile(`(?i)\brequest_tools\b|\brequest it,\s*then run\b`)
	negativeCallRE   = regexp.MustCompile(`(?i)\b(?:do not|never|avoid|without|instead of|before)\s+(?:call|calling|repeat|repeating|retry|retrying|use|using)?\s*` + "`?" + `([a-z][a-z0-9_]*)\b`)
)

func promisedActionsInText(vocab *toolvocab.Catalog, text string) (needsCommand bool, tools []string) {
	clean := regexp.MustCompile(`\{\{.*?\}\}`).ReplaceAllString(text, " ")
	needsCommand = commandPromiseRE.MatchString(clean)
	negated := map[string]bool{}
	for _, m := range negativeCallRE.FindAllStringSubmatch(clean, -1) {
		negated[m[1]] = true
	}
	seen := map[string]bool{}
	addTool := func(name string) {
		if name == "" || name == "command" || negated[name] || seen[name] {
			return
		}
		if vocab != nil && !vocab.KnownName(name) {
			return
		}
		if strings.Contains(clean, "prior "+name+" result") || strings.Contains(clean, "from "+name) {
			return
		}
		seen[name] = true
		tools = append(tools, name)
	}
	for _, m := range backtickToolRE.FindAllStringSubmatch(clean, -1) {
		if m[1] == "command" && (strings.Contains(clean, "`command` argument") || strings.Contains(clean, "in `command`")) {
			continue
		}
		if m[1] == "command" {
			needsCommand = true
			continue
		}
		addTool(m[1])
	}
	for _, m := range proseToolCallRE.FindAllStringSubmatch(clean, -1) {
		addTool(m[1])
	}
	slices.Sort(tools)
	return needsCommand, tools
}
