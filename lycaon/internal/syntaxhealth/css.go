package syntaxhealth

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
)

// cssLeadingMarkupReport flags leading HTML/XML wrapper tags (<style>, <css>)
// while allowing the legacy top-level <!-- comment token.
func cssLeadingMarkupReport(src []byte) (Report, bool) {
	text := strings.TrimLeft(string(src), " \t\r\n\f\ufeff")
	if !strings.HasPrefix(text, "<") || strings.HasPrefix(text, "<!--") {
		return Report{}, false
	}
	start := len(src) - len(text)
	prefix := string(src[:start])
	row := strings.Count(prefix, "\n") + 1
	last := strings.LastIndex(prefix, "\n")
	col := utf8.RuneCountInString(prefix[last+1:]) + 1
	return Report{
		Language: "css",
		Status:   StatusBroken,
		Burden:   Burden{SpanBytes: 1, Faults: 1},
		Diagnostics: []Diagnostic{{
			Row: row, Col: col, EndRow: row, EndCol: col + 1,
			StartByte: start, EndByte: start + 1, Snippet: "<",
			Kind: DiagnosticError, NodeType: "invalid_stylesheet_token",
		}},
	}, true
}

// normalizeModernCSS rewrites modern CSS constructs that tree-sitter-css does
// not yet accept into byte-length- and newline-preserving equivalents so parse
// positions map 1:1 back to src.
func normalizeModernCSS(src []byte) []byte {
	out := bytes.Clone(src)
	var (
		braceDepth int
		parenDepth int
		atRule     string
	)
	for i := 0; i < len(out); {
		if end, ok := skipCSSCommentOrString(out, i); ok {
			i = end
			continue
		}
		if braceDepth == 0 && parenDepth == 0 && atRule == "" {
			if bytes.HasPrefix(out[i:], []byte("<!--")) {
				copy(out[i:i+4], "    ")
				i += 4
				continue
			}
			if bytes.HasPrefix(out[i:], []byte("-->")) {
				copy(out[i:i+3], "   ")
				i += 3
				continue
			}
		}
		b := out[i]
		switch b {
		case '@':
			if parenDepth == 0 {
				name, end := readCSSIdent(out, i+1)
				atRule = strings.ToLower(name)
				i = end
				if atRule == "container" {
					i = normalizeContainerName(out, i)
				}
				continue
			}
		case '{':
			braceDepth++
			atRule = ""
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
			atRule = ""
		case ';':
			if parenDepth == 0 {
				atRule = ""
			}
		case '.':
			if atRule == "layer" && parenDepth == 0 && i > 0 && i+1 < len(out) &&
				isCSSIdentChar(out[i-1]) && isCSSIdentStart(out[i+1]) {
				out[i] = '_'
			}
		case '(':
			if isRangeQueryAtRule(atRule) {
				if closeIdx, ok := findSimpleParenClose(out, i); ok {
					normalizeMediaRangeParen(out[i+1 : closeIdx])
					i = closeIdx + 1
					continue
				}
			}
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		}
		i++
	}
	return out
}

func isRangeQueryAtRule(atRule string) bool {
	switch atRule {
	case "media", "container", "supports", "import":
		return true
	default:
		return false
	}
}

func normalizeContainerName(buf []byte, pos int) int {
	j := skipCSSSpaces(buf, pos)
	name, end := readCSSIdent(buf, j)
	if name == "" {
		return pos
	}
	switch strings.ToLower(name) {
	case "not", "and", "or", "style", "scroll-state":
		return pos
	}
	next := skipCSSSpaces(buf, end)
	if next < len(buf) && (buf[next] == '(' || hasCSSKeywordPrefix(buf[next:], "not") ||
		hasCSSKeywordPrefix(buf[next:], "style") || hasCSSKeywordPrefix(buf[next:], "scroll-state")) {
		blankPreservingNewlines(buf[j:end])
		return end
	}
	return pos
}

func hasCSSKeywordPrefix(buf []byte, kw string) bool {
	if len(buf) <= len(kw) || !strings.EqualFold(string(buf[:len(kw)]), kw) {
		return false
	}
	next := buf[len(kw)]
	return next == '(' || isCSSSpace(next)
}

func findSimpleParenClose(buf []byte, openIdx int) (int, bool) {
	for i := openIdx + 1; i < len(buf); i++ {
		if end, ok := skipCSSCommentOrString(buf, i); ok {
			i = end - 1
			continue
		}
		switch buf[i] {
		case '(', '{', '}', ';':
			return 0, false
		case ')':
			return i, true
		}
	}
	return 0, false
}

type cssRangeToken struct {
	text       string
	start, end int
}

// normalizeMediaRangeParen rewrites a valid Media Queries Level 4 single or
// bounded double range inside (...) into a plain feature_query of identical
// byte length. Malformed range expressions are left untouched so tree-sitter
// flags them as syntax errors.
func normalizeMediaRangeParen(inner []byte) {
	tokens, ok := tokenizeMediaRange(inner)
	if !ok || len(tokens) < 3 {
		return
	}
	opIdx := -1
	secondOpIdx := -1
	for i, tok := range tokens {
		if isMediaRangeOp(tok.text) {
			if opIdx < 0 {
				opIdx = i
			} else if secondOpIdx < 0 {
				secondOpIdx = i
			} else {
				return
			}
		}
	}
	if opIdx <= 0 || opIdx >= len(tokens)-1 {
		return
	}
	if secondOpIdx < 0 {
		lhs, rhs := tokens[:opIdx], tokens[opIdx+1:]
		if len(lhs) == 1 && isMediaFeatureIdent(lhs[0].text) && isMediaRangeValue(rhs) {
			op := tokens[opIdx]
			inner[op.start] = ':'
			for k := op.start + 1; k < op.end; k++ {
				inner[k] = ' '
			}
			return
		}
		if isMediaRangeValue(lhs) && len(rhs) == 1 && isMediaFeatureIdent(rhs[0].text) {
			rewriteMediaFeatureSlice(inner, rhs[0].text, lhs[0].text)
		}
		return
	}
	if secondOpIdx <= opIdx+1 || secondOpIdx >= len(tokens)-1 {
		return
	}
	if !sameRangeDirection(tokens[opIdx].text, tokens[secondOpIdx].text) {
		return
	}
	leftVal := tokens[:opIdx]
	mid := tokens[opIdx+1 : secondOpIdx]
	rightVal := tokens[secondOpIdx+1:]
	if len(mid) == 1 && isMediaFeatureIdent(mid[0].text) && isMediaRangeValue(leftVal) && isMediaRangeValue(rightVal) {
		rewriteMediaFeatureSlice(inner, mid[0].text, leftVal[0].text)
	}
}

func rewriteMediaFeatureSlice(inner []byte, feature, value string) {
	replacement := feature + ":" + value
	if len(replacement) > len(inner) {
		return
	}
	blankPreservingNewlines(inner)
	w := 0
	for i := 0; i < len(inner) && w < len(replacement); i++ {
		if inner[i] == '\n' || inner[i] == '\r' {
			continue
		}
		inner[i] = replacement[w]
		w++
	}
}

func tokenizeMediaRange(inner []byte) ([]cssRangeToken, bool) {
	var tokens []cssRangeToken
	for i := 0; i < len(inner); {
		if isCSSSpace(inner[i]) {
			i++
			continue
		}
		if i+1 < len(inner) && (inner[i] == '<' || inner[i] == '>') && inner[i+1] == '=' {
			tokens = append(tokens, cssRangeToken{text: string(inner[i : i+2]), start: i, end: i + 2})
			i += 2
			continue
		}
		if inner[i] == '<' || inner[i] == '>' || inner[i] == '=' || inner[i] == '/' {
			tokens = append(tokens, cssRangeToken{text: string(inner[i : i+1]), start: i, end: i + 1})
			i++
			continue
		}
		if inner[i] == ':' {
			return nil, false
		}
		start := i
		for i < len(inner) && !isCSSSpace(inner[i]) &&
			inner[i] != '<' && inner[i] != '>' && inner[i] != '=' && inner[i] != '/' && inner[i] != ':' {
			i++
		}
		tokens = append(tokens, cssRangeToken{text: string(inner[start:i]), start: start, end: i})
	}
	return tokens, true
}

func isMediaRangeOp(s string) bool {
	switch s {
	case "<", "<=", ">", ">=", "=":
		return true
	default:
		return false
	}
}

func sameRangeDirection(op1, op2 string) bool {
	isLess1 := op1 == "<" || op1 == "<="
	isLess2 := op2 == "<" || op2 == "<="
	isGreater1 := op1 == ">" || op1 == ">="
	isGreater2 := op2 == ">" || op2 == ">="
	return (isLess1 && isLess2) || (isGreater1 && isGreater2)
}

func isMediaFeatureIdent(s string) bool {
	if s == "" || !isCSSIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isCSSIdentChar(s[i]) {
			return false
		}
	}
	switch strings.ToLower(s) {
	case "and", "or", "not", "only":
		return false
	default:
		return true
	}
}

func isMediaRangeValue(tokens []cssRangeToken) bool {
	if len(tokens) == 1 {
		return isMediaNumericOrDimension(tokens[0].text)
	}
	if len(tokens) == 3 && tokens[1].text == "/" {
		return isMediaPositiveNumber(tokens[0].text) && isMediaPositiveNumber(tokens[2].text)
	}
	return false
}

func isMediaNumericOrDimension(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[i] == '+' || s[i] == '-' {
		i++
	}
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i == len(s) {
		return true
	}
	if s[i] == '%' && i+1 == len(s) {
		return true
	}
	if !isCSSIdentStart(s[i]) {
		return false
	}
	for i++; i < len(s); i++ {
		if !isCSSIdentChar(s[i]) {
			return false
		}
	}
	return true
}

func isMediaPositiveNumber(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// cssStructuralFaults detects broken CSS constructs that tree-sitter-css
// accepts without emitting an ERROR or MISSING node.
func cssStructuralFaults(root *gotreesitter.Node, language *gotreesitter.Language, src []byte) []fault {
	var out []fault
	gotreesitter.Walk(root, func(node *gotreesitter.Node, _ int) gotreesitter.WalkAction {
		if node == nil || len(out) >= maxCollectedFaults {
			return gotreesitter.WalkContinue
		}
		switch node.Type(language) {
		case "declaration":
			if node.Parent() != nil && node.Parent().Type(language) == "stylesheet" {
				out = append(out, fault{
					node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError,
				})
				return gotreesitter.WalkContinue
			}
			if faultItem, ok := cssDeclarationValueFault(node, language, src); ok {
				out = append(out, faultItem)
			}
		case "string_value":
			if pythonUnescapedLineBreak(cssStringInterior(string(node.Text(src)))) {
				out = append(out, fault{
					node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError,
				})
			}
		}
		return gotreesitter.WalkContinue
	})
	return out
}

func cssDeclarationValueFault(node *gotreesitter.Node, language *gotreesitter.Language, src []byte) (fault, bool) {
	var propName string
	valueCount := 0
	for i := 0; i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		typ := child.Type(language)
		if typ == "property_name" {
			propName = strings.TrimSpace(string(child.Text(src)))
			continue
		}
		if typ == "important" {
			continue
		}
		if child.EndByte() <= child.StartByte() || strings.TrimSpace(string(child.Text(src))) == "" {
			if strings.HasPrefix(propName, "--") {
				return fault{}, false
			}
			return fault{
				node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError,
			}, true
		}
		if typ == "plain_value" && strings.Contains(string(child.Text(src)), ":") {
			return fault{
				node: child, start: int(child.StartByte()), end: int(child.EndByte()), kind: DiagnosticError,
			}, true
		}
		valueCount++
	}
	if valueCount == 0 && !strings.HasPrefix(propName, "--") {
		return fault{
			node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError,
		}, true
	}
	return fault{}, false
}

func cssStringInterior(raw string) string {
	if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '\'') && raw[len(raw)-1] == raw[0] {
		return raw[1 : len(raw)-1]
	}
	if len(raw) >= 1 && (raw[0] == '"' || raw[0] == '\'') {
		return raw[1:]
	}
	return raw
}

func skipCSSCommentOrString(buf []byte, i int) (int, bool) {
	if i+1 < len(buf) && buf[i] == '/' && buf[i+1] == '*' {
		j := i + 2
		for j+1 < len(buf) && !(buf[j] == '*' && buf[j+1] == '/') {
			j++
		}
		if j+1 < len(buf) {
			return j + 2, true
		}
		return len(buf), true
	}
	if buf[i] == '"' || buf[i] == '\'' {
		quote := buf[i]
		j := i + 1
		for j < len(buf) {
			if buf[j] == '\\' {
				j += 2
				continue
			}
			if buf[j] == quote {
				return j + 1, true
			}
			j++
		}
		return len(buf), true
	}
	return i, false
}

func skipCSSSpaces(buf []byte, i int) int {
	for i < len(buf) && isCSSSpace(buf[i]) {
		i++
	}
	return i
}

func readCSSIdent(buf []byte, i int) (string, int) {
	if i >= len(buf) || !isCSSIdentStart(buf[i]) {
		return "", i
	}
	start := i
	for i < len(buf) && isCSSIdentChar(buf[i]) {
		i++
	}
	return string(buf[start:i]), i
}

func blankPreservingNewlines(buf []byte) {
	for i, b := range buf {
		if b != '\n' && b != '\r' {
			buf[i] = ' '
		}
	}
}

func isCSSSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

func isCSSIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_' || b == '-'
}

func isCSSIdentChar(b byte) bool {
	return isCSSIdentStart(b) || (b >= '0' && b <= '9')
}
