package oarcore

// Copy-binding language ([OAR-COPY-1]–[OAR-COPY-9]).
// Parses at load and substitutes after the decision.

import (
	"strconv"
	"strings"
)

// copyLoadError is a copy member the binding grammar cannot derive. Its
// message is the single machine token a fixture matches on.
type copyLoadError struct{ Token string }

func (e *copyLoadError) Error() string { return e.Token }

func copyLoadErr(token string) error {
	return &copyLoadError{Token: token}
}

// CopyBinding is one fact a copy member names.
type CopyBinding struct {
	Name        string
	Interpolate bool
}

type copyNodeKind int

const (
	copyText copyNodeKind = iota
	copyBind
	copyIf
)

type copyNode struct {
	kind   copyNodeKind
	value  string
	name   string
	negate bool
	then   []copyNode
	elseN  []copyNode
}

func copyInterpolatable(t FactType) bool {
	switch t {
	case TypeString, TypeBool, TypeInt, TypeDouble, TypeListString:
		return true
	case TypeMapString, TypeListMap, TypeMap:
	}
	return false
}

func copyIsWS(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func copySkipWS(s string, i int) int {
	for i < len(s) && copyIsWS(s[i]) {
		i++
	}
	return i
}

func copyIsLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func copyReadIdent(s string, i int) (name string, next int, ok bool) {
	i = copySkipWS(s, i)
	if i >= len(s) {
		return "", i, false
	}
	start := i
	c := s[i]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_') {
		return "", i, false
	}
	i++
	for i < len(s) {
		c = s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' {
			i++
			continue
		}
		break
	}
	name = s[start:i]
	if !factNameRE.MatchString(name) {
		return "", i, false
	}
	return name, copySkipWS(s, i), true
}

func copyParseTag(s string, i int, open string) (body string, next int, err error) {
	close := "}}"
	token := "binding"
	if open == "{%" {
		close = "%}"
		token = "conditional"
	}
	if i < 0 || i+len(open) > len(s) {
		return "", 0, copyLoadErr("endif")
	}
	end := strings.Index(s[i+len(open):], close)
	if end < 0 {
		return "", 0, copyLoadErr(token)
	}
	end += i + len(open)
	return s[i+len(open) : end], end + len(close), nil
}

func copyReadWord(s string, i int) (word string, next int) {
	start := i
	for i < len(s) && copyIsLetter(s[i]) {
		i++
	}
	return s[start:i], copySkipWS(s, i)
}

func copyParseNodes(s string, from int, until map[string]bool) (nodes []copyNode, next int, err error) {
	i := from
	for i < len(s) {
		bindAt := strings.Index(s[i:], "{{")
		tagAt := strings.Index(s[i:], "{%")
		open := "{{"
		at := -1
		if bindAt >= 0 && (tagAt < 0 || bindAt <= tagAt) {
			at = i + bindAt
			open = "{{"
		} else if tagAt >= 0 {
			at = i + tagAt
			open = "{%"
		}
		if at < 0 {
			if i < len(s) {
				nodes = append(nodes, copyNode{kind: copyText, value: s[i:]})
			}
			return nodes, len(s), nil
		}
		if at > i {
			nodes = append(nodes, copyNode{kind: copyText, value: s[i:at]})
		}
		body, after, err := copyParseTag(s, at, open)
		if err != nil {
			return nil, 0, err
		}
		if open == "{{" {
			name, identNext, ok := copyReadIdent(body, 0)
			if !ok || identNext != len(body) {
				return nil, 0, copyLoadErr("binding")
			}
			nodes = append(nodes, copyNode{kind: copyBind, name: name})
			i = after
			continue
		}
		p := copySkipWS(body, 0)
		word, p := copyReadWord(body, p)
		if until[word] && p == len(body) {
			return nodes, at, nil
		}
		if word == "if" {
			negate := false
			if strings.HasPrefix(body[p:], "not") && (p+3 == len(body) || copyIsWS(body[p+3])) {
				negate = true
				p = copySkipWS(body, p+3)
			}
			name, identNext, ok := copyReadIdent(body, p)
			if !ok || identNext != len(body) {
				return nil, 0, copyLoadErr("if")
			}
			thenNodes, thenAt, err := copyParseNodes(s, after, map[string]bool{"else": true, "endif": true})
			if err != nil {
				return nil, 0, err
			}
			closerBody, closerNext, err := copyParseTag(s, thenAt, "{%")
			if err != nil {
				return nil, 0, err
			}
			q := copySkipWS(closerBody, 0)
			closerWord, q := copyReadWord(closerBody, q)
			if q != len(closerBody) {
				if closerWord == "" {
					closerWord = "conditional"
				}
				return nil, 0, copyLoadErr(closerWord)
			}
			var elseNodes []copyNode
			var done int
			switch closerWord {
			case "else":
				elsePart, elseAt, err := copyParseNodes(s, closerNext, map[string]bool{"endif": true})
				if err != nil {
					return nil, 0, err
				}
				elseNodes = elsePart
				endBody, endNext, err := copyParseTag(s, elseAt, "{%")
				if err != nil {
					return nil, 0, err
				}
				e := copySkipWS(endBody, 0)
				endWord, e := copyReadWord(endBody, e)
				if endWord != "endif" || e != len(endBody) {
					if endWord == "" {
						endWord = "endif"
					}
					return nil, 0, copyLoadErr(endWord)
				}
				done = endNext
			case "endif":
				done = closerNext
			default:
				if closerWord == "" {
					closerWord = "conditional"
				}
				return nil, 0, copyLoadErr(closerWord)
			}
			nodes = append(nodes, copyNode{kind: copyIf, name: name, negate: negate, then: thenNodes, elseN: elseNodes})
			i = done
			continue
		}
		if word == "" {
			word = "conditional"
		}
		return nil, 0, copyLoadErr(word)
	}
	return nodes, i, nil
}

func walkCopy(nodes []copyNode, visit func(copyNode)) {
	for _, n := range nodes {
		visit(n)
		if n.kind == copyIf {
			walkCopy(n.then, visit)
			walkCopy(n.elseN, visit)
		}
	}
}

// ParseCopy returns each fact referenced by a copy member.
func ParseCopy(source string) ([]CopyBinding, error) {
	nodes, next, err := copyParseNodes(source, 0, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if next != len(source) && strings.Contains(source[next:], "{%") {
		return nil, copyLoadErr("conditional")
	}
	out := make([]CopyBinding, 0)
	seen := map[string]bool{}
	walkCopy(nodes, func(n copyNode) {
		switch n.kind {
		case copyBind:
			key := "bind:" + n.name
			if !seen[key] {
				seen[key] = true
				out = append(out, CopyBinding{Name: n.name, Interpolate: true})
			}
		case copyIf:
			key := "if:" + n.name
			if !seen[key] {
				seen[key] = true
				out = append(out, CopyBinding{Name: n.name, Interpolate: false})
			}
		case copyText:
		}
	})
	return out, nil
}

func copyIsZero(v value) bool {
	switch x := v.(type) {
	case bool:
		return !x
	case int64:
		return x == 0
	case int:
		return x == 0
	case float64:
		return x == 0
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	case []map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return true
	}
}

func copyInterpolate(v value) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return CanonicalDouble(x)
	case []string:
		return strings.Join(x, ", ")
	case []any:
		values, err := coerce(x, TypeListString, "copy")
		if err != nil {
			return ""
		}
		return strings.Join(values.([]string), ", ")
	default:
		return ""
	}
}

func renderCopyNodes(nodes []copyNode, lookup func(string) value) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.kind {
		case copyText:
			b.WriteString(n.value)
		case copyBind:
			b.WriteString(copyInterpolate(lookup(n.name)))
		case copyIf:
			take := !copyIsZero(lookup(n.name))
			if n.negate {
				take = copyIsZero(lookup(n.name))
			}
			if take {
				b.WriteString(renderCopyNodes(n.then, lookup))
			} else {
				b.WriteString(renderCopyNodes(n.elseN, lookup))
			}
		}
	}
	return b.String()
}

// renderCopy substitutes bindings in a copy member that has already passed load.
func renderCopy(source string, lookup func(string) value) string {
	if !strings.Contains(source, "{{") && !strings.Contains(source, "{%") {
		return source
	}
	nodes, _, err := copyParseNodes(source, 0, map[string]bool{})
	if err != nil {
		return source
	}
	return renderCopyNodes(nodes, lookup)
}

// RenderCopy validates observations using the same capability as the document.
func (r *Rule) RenderCopy(facts map[string]any) (map[string]string, error) {
	values := make(map[string]value)
	for _, source := range r.Copy {
		bindings, err := ParseCopy(source)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			decl := r.environment.Facts[binding.Name]
			v := zeroValue(decl.Type)
			if raw, present := facts[binding.Name]; present {
				v, err = coerce(raw, decl.Type, binding.Name)
				if err != nil {
					return nil, err
				}
			}
			values[binding.Name] = v
		}
	}
	out := make(map[string]string, len(r.Copy))
	for key, source := range r.Copy {
		out[key] = renderCopy(source, func(name string) value { return values[name] })
	}
	return out, nil
}
