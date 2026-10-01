package summarize

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	parseHealthOK                 = "ok"
	parseHealthNoSymbols          = "no_symbols"
	parseHealthDegradedHeaders    = "degraded_headers"
	parseHealthDegradedKeys       = "degraded_keys"
	parseHealthDegradedParagraphs = "degraded_paragraphs"
)

// ensureDefinitionsForCandidate synthesizes anchors for non-empty symbol-free files.
func ensureDefinitionsForCandidate(c StructureCandidate) (StructureCandidate, string) {
	if c.Kind == StructureKindDirMap {
		return c, parseHealthOK
	}
	if len(c.Symbols) > 0 {
		return c, parseHealthOK
	}
	if strings.TrimSpace(c.Head) == "" && c.LineCount <= 0 {
		return c, parseHealthNoSymbols
	}
	if headers := extractMarkdownHeaders(c.Head, c.StartLine); len(headers) > 0 {
		c.Symbols = headers
		return c, parseHealthDegradedHeaders
	}
	if keys := extractConfigLikeKeys(c.Head, c.StartLine); len(keys) > 0 {
		c.Symbols = keys
		return c, parseHealthDegradedKeys
	}
	if paras := extractHeadParagraphs(c.Head, c.StartLine); len(paras) > 0 {
		c.Symbols = paras
		return c, parseHealthDegradedParagraphs
	}
	// Preserve one anchor when structured extraction finds nothing.
	line := c.StartLine
	if line <= 0 {
		line = 1
	}
	name := firstNonEmptyLine(c.Head)
	if name == "" {
		name = c.RelPath
	}
	if len(name) > 80 {
		name = name[:80]
	}
	c.Symbols = []StructureSymbol{{Kind: "paragraph", Name: name, Line: line}}
	return c, parseHealthDegradedParagraphs
}

func extractMarkdownHeaders(head string, startLine int) []StructureSymbol {
	if startLine <= 0 {
		startLine = 1
	}
	var out []StructureSymbol
	for i, line := range strings.Split(head, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		level := 0
		for _, r := range t {
			if r == '#' {
				level++
				continue
			}
			break
		}
		if level == 0 || level > 6 || (level < len(t) && t[level] != ' ' && t[level] != '\t') {
			continue
		}
		rest := strings.TrimSpace(t[level:])
		if rest == "" {
			continue
		}
		out = append(out, StructureSymbol{Kind: "header", Name: rest, Line: startLine + i})
	}
	return out
}

func extractConfigLikeKeys(head string, startLine int) []StructureSymbol {
	if startLine <= 0 {
		startLine = 1
	}
	var out []StructureSymbol
	seen := map[string]bool{}
	for i, line := range strings.Split(head, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		key := ""
		switch {
		case strings.HasPrefix(t, "\"") || strings.HasPrefix(t, "'"):
			quote := t[0]
			end := strings.IndexByte(t[1:], quote)
			if end < 0 {
				continue
			}
			key = t[1 : 1+end]
			rest := strings.TrimSpace(t[1+end+1:])
			if !strings.HasPrefix(rest, ":") {
				continue
			}
		default:
			// Accept unquoted top-level assignment keys.
			for _, sep := range []string{":", "="} {
				if j := strings.Index(t, sep); j > 0 {
					cand := strings.TrimSpace(t[:j])
					if isSimpleIdent(cand) {
						key = cand
						break
					}
				}
			}
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, StructureSymbol{Kind: "field", Name: key, Line: startLine + i})
		if len(out) >= 24 {
			break
		}
	}
	return out
}

func extractHeadParagraphs(head string, startLine int) []StructureSymbol {
	if startLine <= 0 {
		startLine = 1
	}
	lines := strings.Split(head, "\n")
	var out []StructureSymbol
	i := 0
	for i < len(lines) && len(out) < 6 {
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= len(lines) {
			break
		}
		start := i
		var b strings.Builder
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(strings.TrimSpace(lines[i]))
			i++
			if b.Len() > 120 {
				break
			}
		}
		name := b.String()
		if len(name) > 80 {
			name = name[:80]
		}
		if name == "" {
			continue
		}
		out = append(out, StructureSymbol{Kind: "paragraph", Name: name, Line: startLine + start})
	}
	return out
}

func firstNonEmptyLine(head string) string {
	for _, line := range strings.Split(head, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func isSimpleIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, r := range s {
		if unicode.IsLetter(r) || r == '_' {
			continue
		}
		if i > 0 && (unicode.IsDigit(r) || r == '-' || r == '.') {
			continue
		}
		return false
	}
	return true
}

func identityLine(id PackIdentity) string {
	return fmt.Sprintf("%s %s %d %s %s %s", id.Path, id.Kind, id.LineCount, id.ParseHealth, id.Language, id.OutlineSource)
}

func neighborLine(n PackNeighbor) string {
	return fmt.Sprintf("%s %s", n.Path, n.Why)
}

func importEdgeLine(e PackImportEdge) string {
	return fmt.Sprintf("%s %s %s", e.Kind, e.From, e.To)
}

func callSiteLine(cs PackCallSite) string {
	return fmt.Sprintf("%s %d %s", cs.Path, cs.Line, cs.Excerpt)
}

func skeletonLine(d DefinitionItem) string {
	return fmt.Sprintf("%s %s %s %d", d.RelPath, d.Kind, d.Name, d.Line)
}

func windowLine(w PackWindow) string {
	return fmt.Sprintf("%s %d-%d %s\n%s", w.Path, w.StartLine, w.EndLine, w.Symbol, w.Body)
}
