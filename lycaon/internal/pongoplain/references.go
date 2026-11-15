package pongoplain

import (
	"regexp"
	"strings"
)

var (
	templateRegion  = regexp.MustCompile(`(?s)\{#.*?#\}|\{%\s*-?\s*(?:comment|verbatim)\s*-?\s*%\}.*?\{%\s*-?\s*end(?:comment|verbatim)\s*-?\s*%\}|\{\{.*?\}\}|\{%.*?%\}`)
	expressionToken = regexp.MustCompile(`"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[A-Za-z_][A-Za-z_0-9]*(?:\s*\.\s*[A-Za-z_][A-Za-z_0-9]*)*`)
)

var pongoKeywords = map[string]bool{
	"if":            true,
	"elif":          true,
	"else":          true,
	"endif":         true,
	"for":           true,
	"in":            true,
	"empty":         true,
	"endfor":        true,
	"with":          true,
	"endwith":       true,
	"include":       true,
	"only":          true,
	"set":           true,
	"autoescape":    true,
	"endautoescape": true,
	"templatetag":   true,
	"and":           true,
	"or":            true,
	"not":           true,
	"is":            true,
	"as":            true,
	"by":            true,
	"reversed":      true,
	"sorted":        true,
	"true":          true,
	"false":         true,
	"True":          true,
	"False":         true,
	"nil":           true,
	"none":          true,
	"None":          true,
	"null":          true,
	"on":            true,
	"off":           true,
}

// LoopBindings returns all forloop.* selector references in source, ignoring
// comments, verbatim blocks, and string literals.
func LoopBindings(source string) []string {
	var out []string
	for _, region := range templateRegion.FindAllString(source, -1) {
		if isOpaqueRegion(region) {
			continue
		}
		for _, tok := range expressionToken.FindAllString(region, -1) {
			if isQuotedLiteral(tok) {
				continue
			}
			selector := strings.Join(strings.Fields(tok), "")
			if strings.HasPrefix(selector, "forloop.") {
				out = append(out, selector)
			}
		}
	}
	return out
}

// VariableReferences returns all variable references in source, excluding
// comments, verbatim blocks, string literals, filter names, forloop, and
// active loop, with, or set bindings. References are returned in source order
// with duplicates removed.
func VariableReferences(source string) []string {
	var (
		out      []string
		seen     = map[string]bool{}
		scopes   []map[string]bool
		setScope = map[string]bool{}
	)
	isBound := func(root string) bool {
		if root == "forloop" || setScope[root] {
			return true
		}
		for i := len(scopes) - 1; i >= 0; i-- {
			if scopes[i][root] {
				return true
			}
		}
		return false
	}
	record := func(selector string) {
		root := selectorRoot(selector)
		if root == "" || pongoKeywords[root] || isBound(root) {
			return
		}
		if !seen[selector] {
			seen[selector] = true
			out = append(out, selector)
		}
	}

	for _, region := range templateRegion.FindAllString(source, -1) {
		if isOpaqueRegion(region) {
			continue
		}
		if strings.HasPrefix(region, "{{") {
			body := trimDelimiters(region, "{{", "}}")
			scanExpression(body, record, nil)
			continue
		}
		if !strings.HasPrefix(region, "{%") {
			continue
		}
		body := trimDelimiters(region, "{%", "%}")
		fields := strings.Fields(body)
		if len(fields) == 0 {
			continue
		}
		tag := fields[0]
		rest := strings.TrimSpace(body[len(tag):])
		switch tag {
		case "if", "elif":
			scanExpression(rest, record, nil)
		case "for":
			bound, expr := parseForTag(rest)
			scanExpression(expr, record, nil)
			scopes = append(scopes, bound)
		case "endfor":
			if len(scopes) > 0 {
				scopes = scopes[:len(scopes)-1]
			}
		case "with":
			bound := map[string]bool{}
			scanExpression(rest, record, bound)
			scopes = append(scopes, bound)
		case "endwith":
			if len(scopes) > 0 {
				scopes = scopes[:len(scopes)-1]
			}
		case "set":
			scanExpression(rest, record, setScope)
		case "include":
			scanExpression(rest, record, map[string]bool{})
		}
	}
	return out
}

func isOpaqueRegion(region string) bool {
	return strings.HasPrefix(region, "{#") ||
		strings.Contains(region, "endcomment") ||
		strings.Contains(region, "endverbatim")
}

func trimDelimiters(region, open, close string) string {
	body := strings.TrimPrefix(region, open)
	body = strings.TrimSuffix(body, close)
	body = strings.TrimSpace(body)
	body = strings.TrimPrefix(body, "-")
	body = strings.TrimSuffix(body, "-")
	return strings.TrimSpace(body)
}

func isQuotedLiteral(tok string) bool {
	return len(tok) >= 2 && (tok[0] == '"' || tok[0] == '\'')
}

func selectorRoot(selector string) string {
	if idx := strings.IndexByte(selector, '.'); idx >= 0 {
		return selector[:idx]
	}
	return selector
}

// parseForTag splits "k, v in items" into the bound loop variable set and the
// collection expression after "in".
func parseForTag(rest string) (map[string]bool, string) {
	bound := map[string]bool{}
	matches := expressionToken.FindAllStringIndex(rest, -1)
	for i, loc := range matches {
		tok := rest[loc[0]:loc[1]]
		if isQuotedLiteral(tok) {
			continue
		}
		ident := strings.Join(strings.Fields(tok), "")
		if ident == "in" {
			return bound, rest[loc[1]:]
		}
		if i >= 0 && !pongoKeywords[ident] && !strings.Contains(ident, ".") {
			bound[ident] = true
		}
	}
	return bound, ""
}

// scanExpression extracts variable references from expr. When bindings is
// non-nil, identifiers bound by "name = expr" or "expr as name" are added to
// bindings instead of recorded as variable references.
func scanExpression(expr string, record func(string), bindings map[string]bool) {
	locs := expressionToken.FindAllStringIndex(expr, -1)
	afterAs := false
	for _, loc := range locs {
		tok := expr[loc[0]:loc[1]]
		if isQuotedLiteral(tok) {
			afterAs = false
			continue
		}
		selector := strings.Join(strings.Fields(tok), "")
		if selector == "as" && bindings != nil {
			afterAs = true
			continue
		}
		if afterAs {
			afterAs = false
			if bindings != nil && !strings.Contains(selector, ".") {
				bindings[selector] = true
			}
			continue
		}
		if precededByPipe(expr, loc[0]) {
			continue
		}
		if bindings != nil && !strings.Contains(selector, ".") && followedBySingleAssign(expr, loc[0], loc[1]) {
			bindings[selector] = true
			continue
		}
		if pongoKeywords[selector] {
			continue
		}
		record(selector)
	}
}

func precededByPipe(expr string, start int) bool {
	for i := start - 1; i >= 0; i-- {
		switch expr[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '|':
			return true
		default:
			return false
		}
	}
	return false
}

func followedBySingleAssign(expr string, start, end int) bool {
	for i := start - 1; i >= 0; i-- {
		c := expr[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c == '!' || c == '<' || c == '>' || c == '=' {
			return false
		}
		break
	}
	for i := end; i < len(expr); i++ {
		c := expr[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c != '=' {
			return false
		}
		return i+1 >= len(expr) || expr[i+1] != '='
	}
	return false
}
