package summarize

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/textrank"
)

// symbolWindowLines extracts a source window starting at a definition line from
// the candidate's gathered head, with an optional pre-split head (same file
// reused across many defs in one assemble pass). Returns ok=false when the
// symbol line falls outside the gathered head span.
func symbolWindowLines(d DefinitionItem, windowLines int, lines []string) (PackWindow, bool) {
	if windowLines <= 0 {
		windowLines = DefaultCaps().Gather.SymbolWindowLines
	}
	if d.Head == "" || d.Line <= 0 {
		return PackWindow{}, false
	}
	start := d.StartLine
	if start <= 0 {
		start = 1
	}
	if lines == nil {
		lines = strings.Split(d.Head, "\n")
	}
	endHead := start + len(lines) - 1
	if d.Line < start || d.Line > endHead {
		return PackWindow{}, false
	}
	wStart := d.Line
	wEnd := min(endHead, wStart+windowLines-1)
	if d.EndLine >= wStart {
		wEnd = min(wEnd, d.EndLine)
	}
	body := strings.Join(lines[wStart-start:wEnd-start+1], "\n")
	return PackWindow{
		Path:      d.RelPath,
		StartLine: wStart,
		EndLine:   wEnd,
		Symbol:    d.Name,
		Body:      numberWindowBody(body, wStart),
	}, true
}

func numberWindowBody(body string, startLine int) string {
	if startLine <= 0 {
		startLine = 1
	}
	lines := strings.Split(body, "\n")
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d: %s\n", startLine+i, line)
	}
	return b.String()
}

// mergeOverlappingWindows collapses adjacent/overlapping windows in the same
// file into a single span (keeps first symbol name as label).
func mergeOverlappingWindows(windows []PackWindow) []PackWindow {
	if len(windows) < 2 {
		return windows
	}
	byPath := map[string][]PackWindow{}
	order := []string{}
	for _, w := range windows {
		if _, ok := byPath[w.Path]; !ok {
			order = append(order, w.Path)
		}
		byPath[w.Path] = append(byPath[w.Path], w)
	}
	var out []PackWindow
	for _, path := range order {
		group := byPath[path]
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].StartLine < group[j].StartLine
		})
		cur := group[0]
		for _, next := range group[1:] {
			if next.StartLine <= cur.EndLine+1 {
				if next.EndLine > cur.EndLine {
					// Extend body by appending unseen tail lines.
					cur.Body = mergeWindowBodies(cur, next)
					cur.EndLine = next.EndLine
				}
				continue
			}
			out = append(out, cur)
			cur = next
		}
		out = append(out, cur)
	}
	return out
}

func mergeWindowBodies(a, b PackWindow) string {
	if b.StartLine > a.EndLine {
		return a.Body + b.Body
	}
	// Overlap: keep a's body and append lines of b past a.EndLine.
	lines := strings.Split(strings.TrimSuffix(b.Body, "\n"), "\n")
	var extra strings.Builder
	for _, line := range lines {
		var n int
		rest := line
		if i := strings.IndexByte(line, ':'); i > 0 {
			_, _ = fmt.Sscanf(line[:i], "%d", &n)
			rest = line
		}
		if n > a.EndLine {
			extra.WriteString(rest)
			extra.WriteByte('\n')
		}
	}
	return a.Body + extra.String()
}

// FocusSource keeps a relevant definition inside the gathered source span.
// Ranking cannot fetch outside the bytes the caller already observed.
func FocusSource(ctx context.Context, rr decide.Reranker, task string, candidate StructureCandidate, content string, lineLimit int) StructureCandidate {
	if lineLimit <= 0 {
		return candidate
	}
	lines := strings.Split(content, "\n")
	symbols := append([]StructureSymbol(nil), candidate.Symbols...)
	if candidate.Parses != nil && !*candidate.Parses {
		for i, line := range lines {
			if text := strings.TrimSpace(line); text != "" {
				symbols = append(symbols, StructureSymbol{Kind: "source", Name: text, Line: i + 1})
			}
		}
	}
	symbols = focusQualifiedSymbols(task, symbols)
	scores := sourceFocusScores(ctx, rr, task, candidate.RelPath, symbols)
	best := -1
	for i, score := range scores {
		if score > 0 && (best < 0 || score > scores[best]) {
			best = i
		}
	}
	if best < 0 {
		return candidate
	}
	selected := symbols[best]
	if selected.Kind == "source" {
		name := []rune(selected.Name)
		selected.Name = string(name[:min(len(name), 160)])
		candidate.Symbols = append(candidate.Symbols, selected)
	}
	line := selected.Line
	if line >= candidate.StartLine && line < candidate.StartLine+strings.Count(candidate.Head, "\n") {
		return candidate
	}
	if line < 1 || line > len(lines) {
		return candidate
	}
	candidate.StartLine = line
	candidate.Head = strings.Join(lines[line-1:min(len(lines), line-1+lineLimit)], "\n")
	return candidate
}

var qualifiedIdentifier = regexp.MustCompile(`[\pL_$][\pL\pN_$]*(?:(?:::|\.)[\pL_$][\pL\pN_$]*)+`)

// Exact qualified names outrank incidental prose matches in source fallback.
func focusQualifiedSymbols(task string, symbols []StructureSymbol) []StructureSymbol {
	requested := make(map[string]bool)
	for _, name := range qualifiedIdentifier.FindAllString(task, -1) {
		requested[name] = true
	}
	if len(requested) == 0 {
		return symbols
	}
	var exact []StructureSymbol
	for _, symbol := range symbols {
		for _, name := range qualifiedIdentifier.FindAllString(symbol.Name, -1) {
			if requested[name] {
				exact = append(exact, symbol)
				break
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return symbols
}

var callableIdentifier = regexp.MustCompile(`[\pL_$][\pL\pN_$]*(?:(?:::|\.)[\pL_$][\pL\pN_$]*)*\s*\(`)

var sourceIdentifier = regexp.MustCompile(`[\pL_$][\pL\pN_$]*(?:(?:::|\.)[\pL_$][\pL\pN_$]*)*`)

// Compound identifiers carry domain terms without a language-specific word list.
func sourceIdentifierText(line string) string {
	var names []string
	for _, name := range sourceIdentifier.FindAllString(line, -1) {
		if len(textrank.SplitIdent(name)) > 1 || strings.Contains(name, "::") || strings.Contains(name, ".") {
			names = append(names, name)
		}
	}
	for _, call := range callableIdentifier.FindAllString(line, -1) {
		names = append(names, strings.TrimSpace(strings.TrimSuffix(call, "(")))
	}
	return strings.Join(names, " ")
}

// Prefer code identifiers to incidental question words in comments. The
// engine reads every symbol on the windows site, so a symbol lexical scoring
// missed can still win the window.
func sourceFocusScores(ctx context.Context, rr decide.Reranker, task, relPath string, symbols []StructureSymbol) []float64 {
	code := make([][]textrank.Field, len(symbols))
	prose := make([][]textrank.Field, len(symbols))
	for i, symbol := range symbols {
		name := symbol.Name
		if symbol.Kind == "source" {
			name = sourceIdentifierText(name)
		}
		code[i] = []textrank.Field{{Text: name, Weight: 1}}
		prose[i] = []textrank.Field{{Text: symbol.Name, Weight: 1}}
	}
	scores := taskFieldScores(task, code)
	lexicalHit := false
	for _, score := range scores {
		if score > 0 {
			lexicalHit = true
			break
		}
	}
	if !lexicalHit {
		scores = taskFieldScores(task, prose)
	}
	return rerank(ctx, rr, decide.SiteSummarizeWindows, task, scores, func() []string {
		texts := make([]string, len(symbols))
		for i, symbol := range symbols {
			texts[i] = symbolText(relPath, "", boundRunes(symbol.Name, rerankExcerptRunes), symbol.Kind, symbol.Signature, symbol.Doc)
		}
		return texts
	})
}
