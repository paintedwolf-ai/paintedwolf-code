// Package toolvocabtest validates tool arguments, backticked protocol tokens,
// and subject-attributed capability claims in rendered model-facing text.
package toolvocabtest

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// ArgClaim is an argument or value constraint rendered text attributes to a tool.
type ArgClaim struct {
	Tool, Arg string
}

// CallExample represents a parsed structured call example in prose.
type CallExample struct {
	Tool           string
	Raw            string
	Line           int
	Start          int
	End            int
	Args           map[string]any
	BareIdents     []string
	JSONObjectForm bool
	HasEllipsis    bool
	HasPlaceholder bool
}

// AttributedClaimIssue reports a call or standalone schema claim rejected by
// its attributed tool's projected schema or turn offer set.
type AttributedClaimIssue struct {
	Tool            string
	AttributedTools []string
	Path            string
	Value           string
	Reason          string
	Span            string
	Line            int
}

type unquotedIdent string

type placeholderValue struct {
	raw     string
	numeric bool
}

var (
	callOpenRe       = regexp.MustCompile(`\b([a-z][a-z0-9_]*)\(`)
	dottedToolPathRe = regexp.MustCompile("`([a-z][a-z0-9_]*)\\.([a-z_][a-z0-9_]*(?:\\[\\])?(?:\\.[a-z_][a-z0-9_]*(?:\\[\\])?)*)(?:\\s*[:=]\\s*([^`]+))?`")
	numRangeRe       = regexp.MustCompile(`^[0-9]+\s*[–-]\s*[0-9]+$`)
	identPathRe      = regexp.MustCompile(`^[a-z_][a-z0-9_]*(?:\[\])?(?:\.[a-z_][a-z0-9_]*(?:\[\])?)*$`)
	bareArgIdentRe   = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
	boundPrepToRe    = regexp.MustCompile(`\b(?:on|to|in|for)\s+(?:\*\*)?` + "`?" + `([a-z][a-z0-9_]*)` + "`?")
	boundVerbWithRe  = regexp.MustCompile(`(?i)\b(?:call|run|use|invoke|pass)\s+(?:\*\*)?` + "`?" + `([a-z][a-z0-9_]*)` + "`?" + `(?:\*\*)?\s+with\b`)
)

// ParseCallExamples parses structured call examples attributed to known tools.
func ParseCallExamples(c *toolvocab.Catalog, text string) []CallExample {
	var out []CallExample
	for _, loc := range callOpenRe.FindAllStringSubmatchIndex(text, -1) {
		tool := text[loc[2]:loc[3]]
		if c == nil || !c.KnownName(tool) {
			continue
		}
		body, ok := balancedCallBody(text[loc[1]:])
		if !ok {
			continue
		}
		line := 1 + strings.Count(text[:loc[0]], "\n")
		callEnd := loc[1] + len(body) + 1
		if ex, valid := parseCallBody(tool, text[loc[0]:callEnd], body, line); valid {
			ex.Start = loc[0]
			ex.End = callEnd
			out = append(out, ex)
		}
	}
	return out
}

// ArgClaimsIn lists sorted argument claims attributed to known tools in text.
func ArgClaimsIn(c *toolvocab.Catalog, text string) []ArgClaim {
	seen := map[ArgClaim]bool{}
	for _, ex := range ParseCallExamples(c, text) {
		for _, p := range collectSuppliedArgPaths(ex.Args, "") {
			head, _, _ := strings.Cut(p, ".")
			seen[ArgClaim{Tool: ex.Tool, Arg: head}] = true
		}
	}
	for _, m := range dottedToolPathRe.FindAllStringSubmatch(text, -1) {
		if c != nil && c.KnownName(m[1]) {
			head, _, _ := strings.Cut(normalizeSchemaPath(m[2]), ".")
			seen[ArgClaim{Tool: m[1], Arg: head}] = true
		}
	}
	return sortArgClaims(seen)
}

// UnacceptedArgs lists argument, nested-path, type, and enum claims in text
// that their attributed tool's projected schema rejects.
func UnacceptedArgs(c *toolvocab.Catalog, text string, schema func(tool string) (map[string]any, bool)) []ArgClaim {
	seen := map[ArgClaim]bool{}
	for _, ex := range ParseCallExamples(c, text) {
		s, ok := schema(ex.Tool)
		if !ok {
			continue
		}
		for _, issue := range validateCallAgainstSchema(ex, s) {
			arg := issue.Path
			if issue.Value != "" && strings.Contains(issue.Reason, "enum") {
				arg = issue.Path + "=" + issue.Value
			}
			seen[ArgClaim{Tool: ex.Tool, Arg: arg}] = true
		}
	}
	for _, m := range dottedToolPathRe.FindAllStringSubmatch(text, -1) {
		tool, rawPath, rawVal := m[1], normalizeSchemaPath(m[2]), strings.TrimSpace(m[3])
		if c == nil || !c.KnownName(tool) {
			continue
		}
		s, ok := schema(tool)
		if !ok {
			continue
		}
		if !schemaAcceptsPath(s, rawPath) {
			seen[ArgClaim{Tool: tool, Arg: missingPathHead(s, rawPath)}] = true
			continue
		}
		if rawVal != "" {
			if val, ell, _ := parseValueExpr(rawVal); !ell {
				for _, issue := range validateValueAtPath(tool, s, rawPath, val, 0) {
					seen[ArgClaim{Tool: tool, Arg: issue.Path}] = true
				}
			}
		}
	}
	return sortArgClaims(seen)
}

// ValidateAttributedClaims validates tool call examples, tool.path references,
// and standalone backticked schema paths/assignments against offered tools and
// their exact projected schemas.
func ValidateAttributedClaims(
	c *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	text string,
	offered map[string]bool,
	schema func(tool string) (map[string]any, bool),
) []AttributedClaimIssue {
	var issues []AttributedClaimIssue
	for _, sent := range splitActiveSentences(text) {
		for _, ex := range ParseCallExamples(c, sent.sentence) {
			if isClaimSpanNegated(sent.sentence, ex.Start, ex.End) {
				continue
			}
			ex.Line = sent.line
			if offered != nil && !offered[ex.Tool] {
				issues = append(issues, AttributedClaimIssue{
					Tool: ex.Tool, AttributedTools: []string{ex.Tool}, Span: ex.Raw, Line: ex.Line,
					Reason: fmt.Sprintf("tool %q is not offered", ex.Tool),
				})
				continue
			}
			if s, ok := schema(ex.Tool); ok {
				issues = append(issues, validateCallAgainstSchema(ex, s)...)
			}
		}
		issues = append(issues, validateStandaloneBackticks(c, idx, sent, offered, schema)...)
	}
	return deduplicateIssues(issues)
}

// ValidateCallExample validates one parsed CallExample against a projected schema.
func ValidateCallExample(ex CallExample, projSchema map[string]any) []AttributedClaimIssue {
	if issues := validateCallAgainstSchema(ex, projSchema); len(issues) > 0 {
		return issues
	}
	if !ex.HasEllipsis && !ex.HasPlaceholder && len(ex.BareIdents) == 0 && len(ex.Args) == 0 {
		if err := tools.ValidateToolArgs(projSchema, map[string]any{}); err != nil {
			return []AttributedClaimIssue{{
				Tool: ex.Tool, AttributedTools: []string{ex.Tool}, Span: ex.Raw, Line: ex.Line,
				Reason: fmt.Sprintf("complete call fails %s schema: %v", ex.Tool, err),
			}}
		}
	}
	return nil
}

func validateCallAgainstSchema(ex CallExample, projSchema map[string]any) []AttributedClaimIssue {
	if len(ex.Args) == 0 && len(ex.BareIdents) == 0 {
		return nil
	}
	var issues []AttributedClaimIssue
	for _, bare := range ex.BareIdents {
		if !schemaAcceptsBareIdent(ex.Tool, projSchema, bare) {
			issues = append(issues, AttributedClaimIssue{
				Tool: ex.Tool, AttributedTools: []string{ex.Tool}, Path: bare, Span: ex.Raw, Line: ex.Line,
				Reason: fmt.Sprintf("%s projected schema does not declare %q", ex.Tool, bare),
			})
		}
	}
	for k, v := range ex.Args {
		issues = append(issues, validateValueAtPath(ex.Tool, projSchema, k, v, ex.Line)...)
	}
	if len(issues) > 0 {
		for i := range issues {
			issues[i].Span = ex.Raw
		}
		return issues
	}
	if isCompleteCallExample(ex, projSchema) {
		materialized := materializeCallArgs(projSchema, ex.Args)
		if err := tools.ValidateToolArgs(projSchema, materialized); err != nil {
			issues = append(issues, AttributedClaimIssue{
				Tool: ex.Tool, AttributedTools: []string{ex.Tool}, Path: firstSortedKey(ex.Args), Span: ex.Raw, Line: ex.Line,
				Reason: fmt.Sprintf("complete call fails %s schema: %v", ex.Tool, err),
			})
		}
	}
	return issues
}

func isCompleteCallExample(ex CallExample, projSchema map[string]any) bool {
	if ex.HasEllipsis || len(ex.BareIdents) > 0 {
		return false
	}
	if ex.JSONObjectForm {
		return true
	}
	if len(ex.Args) == 0 {
		return false
	}
	req := requiredSet(projSchema)
	for name := range req {
		if _, ok := ex.Args[name]; !ok {
			return len(ex.Args) > 1
		}
	}
	return true
}

func validateValueAtPath(tool string, rootSchema map[string]any, path string, val any, line int) []AttributedClaimIssue {
	normPath := normalizeSchemaPath(path)
	if !schemaAcceptsPath(rootSchema, normPath) {
		rep := missingPathHead(rootSchema, normPath)
		return []AttributedClaimIssue{{Tool: tool, AttributedTools: []string{tool}, Path: rep, Line: line, Reason: fmt.Sprintf("%s projected schema lacks %q", tool, rep)}}
	}
	nodes := schemaNodesAtPath(rootSchema, normPath)
	switch typed := val.(type) {
	case placeholderValue:
		return nil
	case map[string]any:
		if !nodesAcceptKind(nodes, "object") {
			return []AttributedClaimIssue{{Tool: tool, AttributedTools: []string{tool}, Path: normPath, Line: line, Reason: fmt.Sprintf("%s.%s rejects object value", tool, normPath)}}
		}
		var out []AttributedClaimIssue
		for k, child := range typed {
			out = append(out, validateValueAtPath(tool, rootSchema, normPath+"."+k, child, line)...)
		}
		return out
	case []any:
		if !nodesAcceptKind(nodes, "array") {
			return []AttributedClaimIssue{{Tool: tool, AttributedTools: []string{tool}, Path: normPath, Line: line, Reason: fmt.Sprintf("%s.%s rejects array value", tool, normPath)}}
		}
		var out []AttributedClaimIssue
		for _, elem := range typed {
			if m, ok := elem.(map[string]any); ok {
				for k, child := range m {
					out = append(out, validateValueAtPath(tool, rootSchema, normPath+"."+k, child, line)...)
				}
			} else if lit, isLit := concreteStringOrIdent(elem); isLit {
				if issue := checkEnumAtNodes(tool, normPath, lit, nodes, line); issue != nil {
					out = append(out, *issue)
				}
			}
		}
		return out
	case bool:
		if !nodesAcceptBool(nodes, typed) {
			return []AttributedClaimIssue{{Tool: tool, AttributedTools: []string{tool}, Path: normPath, Value: strconv.FormatBool(typed), Line: line, Reason: fmt.Sprintf("%s.%s rejects boolean %t", tool, normPath, typed)}}
		}
	case int64, float64:
		if !nodesAcceptKind(nodes, "integer") && !nodesAcceptKind(nodes, "number") {
			return []AttributedClaimIssue{{Tool: tool, AttributedTools: []string{tool}, Path: normPath, Line: line, Reason: fmt.Sprintf("%s.%s rejects numeric value", tool, normPath)}}
		}
	case string:
		if !nodesAcceptKind(nodes, "string") {
			return []AttributedClaimIssue{{Tool: tool, AttributedTools: []string{tool}, Path: normPath, Value: typed, Line: line, Reason: fmt.Sprintf("%s.%s rejects string value", tool, normPath)}}
		}
		if !isPlaceholderText(typed) {
			if issue := checkEnumAtNodes(tool, normPath, typed, nodes, line); issue != nil {
				return []AttributedClaimIssue{*issue}
			}
		}
	case unquotedIdent:
		s := string(typed)
		if len(collectEnumsFromNodes(nodes)) > 0 && !isPlaceholderText(s) {
			if issue := checkEnumAtNodes(tool, normPath, s, nodes, line); issue != nil {
				return []AttributedClaimIssue{*issue}
			}
		}
	}
	return nil
}

func validateStandaloneBackticks(
	c *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	sent sentenceContext,
	offered map[string]bool,
	schema func(tool string) (map[string]any, bool),
) []AttributedClaimIssue {
	var issues []AttributedClaimIssue
	for _, bs := range ExtractInlineBackticks(sent.sentence) {
		if isClaimSpanNegated(sent.sentence, bs.Start, bs.End) {
			continue
		}
		raw := strings.TrimSpace(bs.Span)
		if raw == "" || strings.Contains(raw, "(") {
			continue
		}
		if head, rest, hasDot := strings.Cut(raw, "."); hasDot && c != nil && c.KnownName(head) {
			p, val, hasVal, ell := splitPathAndOptionalValue(rest)
			if offered != nil && !offered[head] {
				issues = append(issues, AttributedClaimIssue{Tool: head, AttributedTools: []string{head}, Path: p, Span: raw, Line: sent.line, Reason: fmt.Sprintf("tool %q is not offered", head)})
				continue
			}
			if s, ok := schema(head); ok {
				if !schemaAcceptsPath(s, p) {
					issues = append(issues, AttributedClaimIssue{Tool: head, AttributedTools: []string{head}, Path: p, Span: raw, Line: sent.line, Reason: fmt.Sprintf("%s projected schema lacks %q", head, p)})
				} else if hasVal && !ell {
					for _, iss := range validateValueAtPath(head, s, p, val, sent.line) {
						iss.Span = raw
						issues = append(issues, iss)
					}
				}
			}
			continue
		}
		if idx == nil {
			continue
		}
		claimPath, val, hasVal, ok := parseStandaloneSchemaClaim(c, idx, sent, raw)
		if !ok {
			continue
		}
		attributed := resolveAttributedTools(c, idx, sent, raw, claimPath, hasVal, val)
		if len(attributed) == 0 {
			continue
		}
		if !anyAttributedToolAccepts(attributed, claimPath, hasVal, val, offered, schema) {
			valStr, _ := concreteStringOrIdent(val)
			if b, isBool := val.(bool); isBool {
				valStr = strconv.FormatBool(b)
			}
			primary := ""
			if len(attributed) == 1 {
				primary = attributed[0]
			}
			issues = append(issues, AttributedClaimIssue{
				Tool: primary, AttributedTools: attributed, Path: claimPath, Value: valStr, Span: raw, Line: sent.line,
				Reason: fmt.Sprintf("standalone claim %q rejected for attributed tool(s) %v", raw, attributed),
			})
		}
	}
	return issues
}

func sortArgClaims(seen map[ArgClaim]bool) []ArgClaim {
	if len(seen) == 0 {
		return nil
	}
	out := make([]ArgClaim, 0, len(seen))
	for claim := range seen {
		out = append(out, claim)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Arg < out[j].Arg
	})
	return out
}

func collectSuppliedArgPaths(m map[string]any, prefix string) []string {
	var out []string
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		out = append(out, p)
		if sub, ok := v.(map[string]any); ok {
			out = append(out, collectSuppliedArgPaths(sub, p)...)
		} else if arr, ok := v.([]any); ok {
			for _, elem := range arr {
				if elemMap, ok := elem.(map[string]any); ok {
					out = append(out, collectSuppliedArgPaths(elemMap, p)...)
				}
			}
		}
	}
	return out
}

func schemaAcceptsBareIdent(tool string, schema map[string]any, bare string) bool {
	if schemaAcceptsPath(schema, bare) {
		return true
	}
	for _, lits := range SchemaEnums(schema) {
		if slices.Contains(lits, bare) {
			return true
		}
	}
	if tool == "task" {
		if vocab, err := StockBacktickVocab(); err == nil && vocab.catalogIDs[bare] {
			return true
		}
	}
	return false
}

func checkEnumAtNodes(tool, normPath, lit string, nodes []map[string]any, line int) *AttributedClaimIssue {
	enums := collectEnumsFromNodes(nodes)
	if len(enums) == 0 || slices.Contains(enums, lit) || strings.Contains(lit, "|") {
		return nil
	}
	return &AttributedClaimIssue{
		Tool: tool, AttributedTools: []string{tool}, Path: normPath, Value: lit, Line: line,
		Reason: fmt.Sprintf("%s.%s enum %v rejects %q", tool, normPath, enums, lit),
	}
}

func concreteStringOrIdent(val any) (string, bool) {
	switch v := val.(type) {
	case string:
		return v, true
	case unquotedIdent:
		return string(v), true
	default:
		return "", false
	}
}

func isPlaceholderText(s string) bool {
	return strings.Contains(s, "…") || strings.Contains(s, "...") || (strings.Contains(s, "<") && strings.Contains(s, ">"))
}

func firstSortedKey(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func splitPathAndOptionalValue(rest string) (path string, val any, hasVal bool, hasEllipsis bool) {
	if k, vStr, hasSep := splitKeyValueTopLevel(rest); hasSep {
		v, ell, _ := parseValueExpr(vStr)
		return normalizeSchemaPath(k), v, true, ell
	}
	return normalizeSchemaPath(rest), nil, false, false
}

func explicitPrepositionTool(sentence, rawSpan string) string {
	cat := toolvocab.StockCatalog()
	if cat == nil {
		return ""
	}
	idx := strings.Index(sentence, "`"+rawSpan+"`")
	if idx < 0 {
		return ""
	}
	end := idx + len(rawSpan) + 2
	cStart, cEnd := enclosingSubClause(sentence, idx, end)
	after := sentence[end:max(end, cEnd)]
	if len(after) > 40 {
		after = after[:40]
	}
	if m := boundPrepToRe.FindStringSubmatch(after); len(m) == 2 && cat.KnownName(m[1]) {
		return m[1]
	}
	if m := boundVerbWithRe.FindAllStringSubmatch(sentence[min(idx, cStart):idx], -1); len(m) > 0 {
		if cand := m[len(m)-1][1]; cat.KnownName(cand) {
			return cand
		}
	}
	return ""
}

func isPrunableEnumValue(idx *PrunableSchemaIndex, normPath string, val any) bool {
	lit, ok := concreteStringOrIdent(val)
	if !ok {
		return false
	}
	for _, owner := range idx.AllOwners(normPath) {
		if idx.IsPrunableEnum(owner, normPath, lit) {
			return true
		}
	}
	return false
}

func compatibleBaseOwners(idx *PrunableSchemaIndex, path string, hasVal bool, val any) []string {
	owners := idx.AllOwners(path)
	if !hasVal {
		return owners
	}
	var out []string
	for _, t := range owners {
		if base, ok := idx.BaseSchema(t); ok && len(validateValueAtPath(t, base, path, val, 0)) == 0 {
			out = append(out, t)
		}
	}
	return out
}

func intersectSorted(a, b []string) []string {
	setB := stringSliceSet(b)
	var out []string
	for _, item := range a {
		if setB[item] && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func deduplicateIssues(issues []AttributedClaimIssue) []AttributedClaimIssue {
	type key struct{ tool, path, val, span string }
	seen := map[key]bool{}
	var out []AttributedClaimIssue
	for _, iss := range issues {
		k := key{iss.Tool, iss.Path, iss.Value, iss.Span}
		if !seen[k] {
			seen[k] = true
			out = append(out, iss)
		}
	}
	return out
}

func balancedCallBody(rest string) (string, bool) {
	end := -1
	failed := false
	walkTopLevel(rest, func(idx int, ch byte) bool {
		if ch == ')' {
			end = idx
			return false
		}
		if ch == '\n' {
			failed = true
			return false
		}
		return true
	})
	if failed || end < 0 {
		return "", false
	}
	return rest[:end], true
}

func splitTopLevel(s string, sep byte) []string {
	var out []string
	start := 0
	walkTopLevel(s, func(idx int, ch byte) bool {
		if ch == sep {
			out = append(out, s[start:idx])
			start = idx + 1
		}
		return true
	})
	return append(out, s[start:])
}

func splitKeyValueTopLevel(s string) (string, string, bool) {
	pos := -1
	bad := false
	walkTopLevel(s, func(idx int, ch byte) bool {
		if ch == '=' || ch == ':' {
			if idx+1 < len(s) && s[idx+1] == '=' {
				bad = true
				return false
			}
			pos = idx
			return false
		}
		return true
	})
	if bad || pos < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:pos]), strings.TrimSpace(s[pos+1:]), true
}

func parseCallBody(tool, raw, body string, line int) (CallExample, bool) {
	trimmed := strings.TrimSpace(body)
	ex := CallExample{Tool: tool, Raw: raw, Line: line, Args: map[string]any{}}
	if trimmed == "" {
		return ex, true
	}
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		obj, ell, ph, ok := parseObjectLiteral(trimmed)
		if !ok {
			return CallExample{}, false
		}
		ex.Args, ex.JSONObjectForm, ex.HasEllipsis, ex.HasPlaceholder = obj, true, ell, ph
		return ex, true
	}
	for _, part := range splitTopLevel(trimmed, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "…" || part == "..." {
			ex.HasEllipsis = true
			continue
		}
		if k, vStr, hasSep := splitKeyValueTopLevel(part); hasSep {
			if !identPathRe.MatchString(k) {
				return CallExample{}, false
			}
			val, ell, ph := parseValueExpr(vStr)
			ex.HasEllipsis, ex.HasPlaceholder = ex.HasEllipsis || ell, ex.HasPlaceholder || ph
			insertDottedArg(ex.Args, normalizeSchemaPath(k), val)
			continue
		}
		ident := strings.TrimSuffix(strings.TrimSuffix(part, "?"), "*")
		if !bareArgIdentRe.MatchString(ident) {
			return CallExample{}, false
		}
		ex.BareIdents = append(ex.BareIdents, ident)
		ex.HasEllipsis = true
	}
	return ex, true
}

func parseValueExpr(raw string) (val any, hasEllipsis bool, hasPlaceholder bool) {
	s := strings.TrimSpace(raw)
	if s == "" || (strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">")) {
		return placeholderValue{raw: s}, false, true
	}
	if s == "…" || s == "..." {
		return placeholderValue{raw: s}, true, true
	}
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		u := unquoteLiteral(s)
		if u == "…" || u == "..." {
			return placeholderValue{raw: u}, true, true
		}
		if isPlaceholderText(u) {
			return u, strings.Contains(u, "…") || strings.Contains(u, "..."), true
		}
		return u, false, false
	}
	switch s {
	case "true":
		return true, false, false
	case "false":
		return false, false, false
	case "null":
		return nil, false, false
	}
	if numRangeRe.MatchString(s) {
		return placeholderValue{raw: s, numeric: true}, true, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, false, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, false, false
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		items := []any{}
		for _, elem := range splitTopLevel(s[1:len(s)-1], ',') {
			if elem = strings.TrimSpace(elem); elem == "" {
				continue
			}
			if elem == "…" || elem == "..." {
				hasEllipsis = true
				continue
			}
			ev, ell, ph := parseValueExpr(elem)
			hasEllipsis, hasPlaceholder = hasEllipsis || ell, hasPlaceholder || ph
			items = append(items, ev)
		}
		return items, hasEllipsis, hasPlaceholder
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		if obj, ell, ph, ok := parseObjectLiteral(s); ok {
			return obj, ell, ph
		}
	}
	return unquotedIdent(s), false, false
}

func parseObjectLiteral(s string) (map[string]any, bool, bool, bool) {
	inner := strings.TrimSpace(s[1 : len(s)-1])
	out := map[string]any{}
	if inner == "" {
		return out, false, false, true
	}
	var ell, ph bool
	for _, entry := range splitTopLevel(inner, ',') {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		if entry == "…" || entry == "..." {
			ell = true
			continue
		}
		if k, vStr, ok := splitKeyValueTopLevel(entry); ok {
			if k = unquoteLiteral(strings.TrimSpace(k)); k == "" {
				return nil, false, false, false
			}
			v, e, p := parseValueExpr(vStr)
			ell, ph = ell || e, ph || p
			out[k] = v
			continue
		}
		if !identPathRe.MatchString(entry) {
			return nil, false, false, false
		}
		out[entry] = placeholderValue{raw: entry}
		ph = true
	}
	return out, ell, ph, true
}

func insertDottedArg(dst map[string]any, dotted string, val any) {
	head, tail, hasMore := strings.Cut(dotted, ".")
	if !hasMore {
		dst[head] = val
		return
	}
	next, ok := dst[head].(map[string]any)
	if !ok {
		next = map[string]any{}
		dst[head] = next
	}
	insertDottedArg(next, tail, val)
}

func unquoteLiteral(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}
