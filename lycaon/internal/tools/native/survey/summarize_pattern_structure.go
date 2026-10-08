package survey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
)

func (g *summaryPatterns) structureFromPathPattern(ctx context.Context, display, pattern string, matches []grepMatch) (summarize.StructureCandidate, int, error) {
	resolved, err := g.access.reads.Resolve(ctx, display)
	if err != nil {
		return summarize.StructureCandidate{}, 0, err
	}
	content, err := g.sources.readFileCached(ctx, resolved.Abs)
	if err != nil {
		if errors.Is(err, errFileNeedsStreaming) {
			sc, n, ok := g.sources.structureFromStream(ctx, resolved.Abs, resolved.DisplayPath)
			if !ok {
				return summarize.StructureCandidate{}, 0, fmt.Errorf("summarize pattern structure %s", display)
			}
			g.sources.markPartialStructure(resolved.Abs, &sc)
			sc.Symbols = patternMatchSymbols(matches)
			sc.ContentHash = structureContentHashPattern(sc, pattern)
			return sc, n, nil
		}
		return summarize.StructureCandidate{}, 0, fmt.Errorf("summarize pattern read %s: %w", display, err)
	}
	sc, _, ok := g.sources.structureFromContent(ctx, resolved.DisplayPath, content)
	if !ok {
		return summarize.StructureCandidate{}, 0, fmt.Errorf("summarize pattern structure %s", display)
	}
	lines := strings.Split(string(content), "\n")
	sc = scopeStructureToMatches(sc, pattern, matches, lines, g.caps.Gather.FileHeadLines)
	g.sources.markPartialStructure(resolved.Abs, &sc)
	return sc, len(sc.Head), nil
}

func patternMatchSymbols(matches []grepMatch) []summarize.StructureSymbol {
	symbols := make([]summarize.StructureSymbol, 0, len(matches))
	seen := make(map[int]bool, len(matches))
	for _, match := range matches {
		if match.Line <= 0 || seen[match.Line] {
			continue
		}
		seen[match.Line] = true
		name := strings.TrimSpace(match.Content)
		if len(name) > 96 {
			name = name[:96]
		}
		if name == "" {
			name = fmt.Sprintf("match:%d", match.Line)
		}
		symbols = append(symbols, summarize.StructureSymbol{Kind: "match", Name: name, Line: match.Line})
	}
	return symbols
}

// patternSymbolWindow bounds nearby outline symbols.

const patternSymbolWindow = 40

const patternMatchSampleLineMax = 160

func patternMatchSamples(matches []grepMatch, max int) []summarize.PatternMatchSample {
	if max <= 0 || len(matches) == 0 {
		return nil
	}
	out := make([]summarize.PatternMatchSample, 0, max)
	for _, m := range matches {
		if len(out) >= max {
			break
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			content = strings.TrimSpace(m.Match)
		}
		if content == "" {
			continue
		}
		content = clampExcerpt(content, patternMatchSampleLineMax)
		out = append(out, summarize.PatternMatchSample{
			Path: m.Path, Line: m.Line, Content: content,
		})
	}
	return out
}

func scopeStructureToMatches(sc summarize.StructureCandidate, pattern string, matches []grepMatch, fileLines []string, headCap int) summarize.StructureCandidate {
	if len(matches) == 0 {
		sc.ContentHash = structureContentHashPattern(sc, pattern)
		return sc
	}
	matchLines := make([]int, 0, len(matches))
	seenLine := map[int]bool{}
	matchByLine := map[int]string{}
	for _, m := range matches {
		if m.Line <= 0 || seenLine[m.Line] {
			continue
		}
		seenLine[m.Line] = true
		matchLines = append(matchLines, m.Line)
		name := strings.TrimSpace(m.Content)
		if name == "" {
			name = strings.TrimSpace(m.Match)
		}
		if name == "" {
			name = fmt.Sprintf("match:%d", m.Line)
		}
		if len(name) > 96 {
			name = name[:96]
		}
		matchByLine[m.Line] = name
	}
	sort.Ints(matchLines)
	if len(matchLines) == 0 {
		sc.ContentHash = structureContentHashPattern(sc, pattern)
		return sc
	}

	// Match rows preserve discrete hits.
	filtered := make([]summarize.StructureSymbol, 0, len(matchLines)+len(sc.Symbols))
	occupied := map[int]bool{}
	for _, ml := range matchLines {
		filtered = append(filtered, summarize.StructureSymbol{
			Kind: "match", Name: matchByLine[ml], Line: ml,
		})
		occupied[ml] = true
	}
	for _, sym := range sc.Symbols {
		if occupied[sym.Line] {
			continue
		}
		for _, ml := range matchLines {
			if absInt(sym.Line-ml) <= patternSymbolWindow {
				filtered = append(filtered, sym)
				occupied[sym.Line] = true
				break
			}
		}
	}
	sc.Symbols = filtered

	minL, maxL := matchLines[0], matchLines[len(matchLines)-1]
	pad := 2
	start := minL - pad
	if start < 1 {
		start = 1
	}
	end := maxL + pad
	if end > len(fileLines) {
		end = len(fileLines)
	}
	if headCap > 0 && end-start+1 > headCap {
		end = start + headCap - 1
		if end > len(fileLines) {
			end = len(fileLines)
		}
	}
	sc.Head = strings.Join(fileLines[start-1:end], "\n")
	sc.StartLine = start
	sc.ContentHash = structureContentHashPattern(sc, pattern)
	return sc
}

func patternNoMaterialReject(ctx context.Context, g *summaryPatterns, paths []string, pattern string, skipped []string) *tools.ToolReject {
	for _, path := range skipped {
		g.sources.noteSkippedPath(path)
	}
	data := g.sources.noMaterialData(summarize.Request{Paths: paths, Pattern: pattern})
	data["match_count"] = 0
	if len(paths) == 1 {
		if samples := g.sampleOutlineIdentifiers(ctx, paths[0], 8); len(samples) > 0 {
			data["sample_identifiers"] = strings.Join(samples, ", ")
		}
	}
	return &tools.ToolReject{Code: "SUMMARIZE_NO_MATERIAL", Data: data}
}

func (g *summaryPatterns) sampleOutlineIdentifiers(ctx context.Context, display string, max int) []string {
	if g == nil || max <= 0 || strings.TrimSpace(display) == "" || display == "." {
		return nil
	}
	resolved, err := g.access.reads.Resolve(ctx, display)
	if err != nil {
		return nil
	}
	info, serr := os.Stat(resolved.Abs)
	if serr != nil || info.IsDir() {
		return nil
	}
	sc, _, ok := g.sources.structureFromAbs(ctx, resolved.Abs, resolved.DisplayPath)
	if !ok {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, sym := range sc.Symbols {
		name := strings.TrimSpace(sym.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= max {
			break
		}
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func structureContentHashPattern(sc summarize.StructureCandidate, pattern string) string {
	var b strings.Builder
	b.WriteString(sc.RelPath)
	b.WriteString(sc.Kind)
	fmt.Fprintf(&b, "%d", sc.LineCount)
	fmt.Fprintf(&b, "%d", sc.StartLine)
	b.WriteString(sc.Head)
	for _, s := range sc.Symbols {
		fmt.Fprintf(&b, "%s%s%d", s.Kind, s.Name, s.Line)
	}
	for _, t := range sc.RollupRows {
		b.WriteString(t)
	}
	if p := strings.TrimSpace(pattern); p != "" {
		b.WriteString("\x00p=")
		b.WriteString(p)
	}
	return summarize.HashString(b.String())
}
