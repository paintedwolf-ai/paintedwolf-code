package summarize

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/textrank"
)

// Candidate text the engine reads is bounded so one call stays inside its
// deadline; the lexical score still sees the whole field.
const (
	rerankHeadRunes    = 300
	rerankExcerptRunes = 200
	// SignatureRunes bounds a definition's signature line.
	SignatureRunes = 160
)

// SignatureLine returns the trimmed, bounded source line at a 1-based line
// number, or "" when the line is out of range.
func SignatureLine(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return boundRunes(lines[line-1], SignatureRunes)
}

// DocRunes bounds the comment carried with a definition.
const DocRunes = 240

// docMarkers are line-comment prefixes across the languages the host parses.
var docMarkers = []string{"///", "//!", "//", "/**", "/*", "*/", "*", "#", "--", "\"\"\""}

// LeadingComment returns the comment lines directly above a 1-based line,
// joined and bounded. Attributes and decorators between the comment and the
// definition are skipped; a blank line ends the comment.
func LeadingComment(lines []string, line int) string {
	var parts []string
	for i := line - 2; i >= 0 && len(parts) < 6; i-- {
		text := strings.TrimSpace(lines[i])
		if text == "" {
			break
		}
		if strings.HasPrefix(text, "#[") || strings.HasPrefix(text, "@") {
			continue
		}
		marker := ""
		for _, m := range docMarkers {
			if strings.HasPrefix(text, m) {
				marker = m
				break
			}
		}
		if marker == "" {
			break
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, marker), "*/"))
		parts = append([]string{text}, parts...)
	}
	return boundRunes(strings.Join(parts, " "), DocRunes)
}

func pathClean(rel string) string {
	return strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "./")
}

// rerank blends engine relevance into lexical scores when the site is active.
// texts is built only when the engine will read it.
func rerank(ctx context.Context, rr decide.Reranker, site decide.Site, task string, lexical []float64, texts func() []string) []float64 {
	if !rr.Active(site) {
		return lexical
	}
	blended, _ := rr.Rerank(ctx, site, task, lexical, texts())
	return blended
}

func boundRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

// rankStructureCandidates orders files by task and observed structure.
func rankStructureCandidates(ctx context.Context, rr decide.Reranker, task string, structure []StructureCandidate) []StructureCandidate {
	if len(structure) < 2 {
		return structure
	}
	files := make([]StructureCandidate, 0, len(structure))
	other := make([]StructureCandidate, 0, len(structure))
	for _, s := range structure {
		if s.Kind == StructureKindFile || s.Kind == StructureKindInline {
			files = append(files, s)
		} else {
			other = append(other, s)
		}
	}
	if len(files) < 2 {
		return structure
	}
	aligned := structureAffinityScores(ctx, rr, task, files)
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		sa, sb := structureSalience(files[ia], aligned[ia]), structureSalience(files[ib], aligned[ib])
		if sa != sb {
			return sa > sb
		}
		return pathDepth(files[ia].RelPath) < pathDepth(files[ib].RelPath)
	})
	rankedFiles := make([]StructureCandidate, len(files))
	for i, idx := range order {
		rankedFiles[i] = files[idx]
	}
	out := make([]StructureCandidate, 0, len(structure))
	out = append(out, other...)
	out = append(out, rankedFiles...)
	return out
}

// structureAffinityScores is BM25F over path and head, with engine relevance
// blended in on the structure site.
func structureAffinityScores(ctx context.Context, rr decide.Reranker, task string, files []StructureCandidate) []float64 {
	docs := make([][]textrank.Field, len(files))
	for i, s := range files {
		docs[i] = []textrank.Field{{Text: s.RelPath, Weight: 3}, {Text: structureRankBody(s), Weight: 1}}
	}
	return rerank(ctx, rr, decide.SiteSummarizeStructure, task, taskFieldScores(task, docs), func() []string {
		texts := make([]string, len(files))
		for i, s := range files {
			texts[i] = structureCandidateText(s)
		}
		return texts
	})
}

// structureCandidateText is what the engine reads for one file: its path and
// the head the structure tier already observed.
func structureCandidateText(s StructureCandidate) string {
	var b strings.Builder
	b.WriteString("File: ")
	b.WriteString(s.RelPath)
	if s.Language != "" {
		b.WriteString(" (")
		b.WriteString(s.Language)
		b.WriteString(")")
	}
	if body := boundRunes(structureRankBody(s), rerankHeadRunes); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
	}
	return b.String()
}

func structureSalience(c StructureCandidate, taskAffinity float64) float64 {
	s := taskAffinity
	if len(c.Symbols) > 0 {
		s += 0.2
	} else if c.Head == "" {
		s -= 0.2
	}
	return s
}

func pathDepth(rel string) int {
	rel = pathClean(rel)
	if rel == "" || rel == "." {
		return 0
	}
	return strings.Count(rel, "/")
}

// buildDefinitionPile turns structure candidates into definition-granular pile
// items. Applies no-definition degrade so non-empty files always contribute.
// Directory maps become pinned items. Task tokens are not consulted here.
func buildDefinitionPile(structure []StructureCandidate) []DefinitionItem {
	var out []DefinitionItem
	for _, raw := range structure {
		c, health := ensureDefinitionsForCandidate(raw)
		if c.Kind == StructureKindDirMap {
			out = append(out, DefinitionItem{
				RelPath: c.RelPath, Kind: "directory_map", Name: c.RelPath, Line: 1,
				Pinned: true, FileKind: StructureKindDirMap, LineCount: c.LineCount,
				ParseHealth: parseHealthOK, Head: strings.Join(c.RollupRows, "\n"),
				StartLine: 1, ContentHash: c.ContentHash,
				Language: c.Language, OutlineSource: c.OutlineSource,
			})
			continue
		}
		if len(c.Symbols) == 0 {
			continue
		}
		for i, sym := range c.Symbols {
			end := 0
			if (sym.Kind == "section" || sym.Kind == "header") && i+1 < len(c.Symbols) {
				end = c.Symbols[i+1].Line - 1
			}
			out = append(out, DefinitionItem{
				RelPath: c.RelPath, Kind: sym.Kind, Name: sym.Name, Line: sym.Line, EndLine: end, Signature: sym.Signature, Doc: sym.Doc,
				FileKind: c.Kind, LineCount: c.LineCount, ParseHealth: health,
				Head: c.Head, StartLine: c.StartLine, ContentHash: c.ContentHash,
				Language: c.Language, OutlineSource: c.OutlineSource, Parses: c.Parses,
				Errors: c.Errors, ErrorKind: c.ErrorKind, LogDigest: c.LogDigest, ImportPath: c.ImportPath,
			})
		}
	}
	return out
}

// rankDefinitions orders definition items by task-IDF + structural salience.
// Pinned directory-map items stay ahead. Stable for equal scores; with no task
// overlap, structural salience alone orders.
func rankDefinitions(ctx context.Context, rr decide.Reranker, task string, defs []DefinitionItem) []DefinitionItem {
	if len(defs) < 2 {
		return defs
	}
	pinned := make([]DefinitionItem, 0, 2)
	rest := make([]DefinitionItem, 0, len(defs))
	for _, d := range defs {
		if d.Pinned {
			pinned = append(pinned, d)
		} else {
			rest = append(rest, d)
		}
	}
	if len(rest) < 2 {
		return append(pinned, rest...)
	}
	scores := definitionScores(ctx, rr, task, rest)
	order := make([]int, len(rest))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if scores[ia] != scores[ib] {
			return scores[ia] > scores[ib]
		}
		return pathDepth(rest[ia].RelPath) < pathDepth(rest[ib].RelPath)
	})
	ranked := make([]DefinitionItem, len(rest))
	for i, idx := range order {
		ranked[i] = rest[idx]
	}
	return append(pinned, diverseDefinitions(ranked)...)
}

// definitionScores is BM25F over path, name, and kind plus kind salience, with
// engine relevance blended in on the definitions site.
func definitionScores(ctx context.Context, rr decide.Reranker, task string, defs []DefinitionItem) []float64 {
	docs := make([][]textrank.Field, len(defs))
	for i, d := range defs {
		docs[i] = []textrank.Field{{Text: d.RelPath, Weight: 3}, {Text: d.Name + " " + d.Kind, Weight: 2}, {Text: d.Signature, Weight: 1}, {Text: d.Doc, Weight: 1}}
	}
	lex := taskFieldScores(task, docs)
	for i, d := range defs {
		lex[i] += definitionSalience(d)
	}
	return rerank(ctx, rr, decide.SiteSummarizeDefinitions, task, lex, func() []string {
		texts := make([]string, len(defs))
		for i, d := range defs {
			texts[i] = DefinitionText(d)
		}
		return texts
	})
}

// DefinitionText is what the engine reads for one definition: where it is,
// what it is, its signature line, and the comment above it. The file head
// the skeleton tier holds is the same for every symbol of a file, so it is
// left out.
func DefinitionText(d DefinitionItem) string {
	return symbolText(d.RelPath, d.Language, d.Name, d.Kind, d.Signature, d.Doc)
}

func symbolText(relPath, language, name, kind, signature, doc string) string {
	var b strings.Builder
	b.WriteString("File: ")
	b.WriteString(relPath)
	if language != "" {
		b.WriteString(" (")
		b.WriteString(language)
		b.WriteString(")")
	}
	b.WriteString("\nSymbol: ")
	b.WriteString(name)
	if kind != "" {
		b.WriteString(" (")
		b.WriteString(kind)
		b.WriteString(")")
	}
	if signature = strings.TrimSpace(signature); signature != "" {
		b.WriteString("\n")
		b.WriteString(signature)
	}
	if doc = strings.TrimSpace(doc); doc != "" {
		b.WriteString("\nDoc: ")
		b.WriteString(doc)
	}
	return b.String()
}

func definitionSalience(d DefinitionItem) float64 {
	switch strings.ToLower(d.Kind) {
	case "type", "class", "struct", "interface", "enum":
		return 0.4
	case "func", "function", "method", "def":
		return 0.3
	case "header", "section":
		if d.Line == 1 {
			return 0.4
		}
		return 0.2
	default:
		return 0
	}
}

// rankNeighborStubs orders fit-tier neighbor stubs by task-IDF over path + why.
// Stable for equal scores.
func rankNeighborStubs(ctx context.Context, rr decide.Reranker, task string, neighbors []PackNeighbor) []PackNeighbor {
	if len(neighbors) < 2 {
		return neighbors
	}
	docs := make([][]textrank.Field, len(neighbors))
	for i, n := range neighbors {
		docs[i] = []textrank.Field{{Text: n.Path, Weight: 3}, {Text: n.Why, Weight: 1}}
	}
	scores := rerank(ctx, rr, decide.SiteSummarizeNeighbors, task, taskFieldScores(task, docs), func() []string {
		texts := make([]string, len(neighbors))
		for i, n := range neighbors {
			texts[i] = "File: " + n.Path + "\n" + boundRunes(n.Why, rerankExcerptRunes)
		}
		return texts
	})
	out := make([]PackNeighbor, len(neighbors))
	for i, idx := range textrank.StableOrderByScore(scores) {
		out[i] = neighbors[idx]
	}
	return out
}

// rankCallSites orders fit-tier call-site rows by task-IDF over path + excerpt.
func rankCallSites(ctx context.Context, rr decide.Reranker, task string, sites []PackCallSite) []PackCallSite {
	if len(sites) < 2 {
		return sites
	}
	docs := make([][]textrank.Field, len(sites))
	for i, cs := range sites {
		docs[i] = []textrank.Field{{Text: cs.Path, Weight: 3}, {Text: cs.Excerpt, Weight: 2}}
	}
	scores := rerank(ctx, rr, decide.SiteSummarizeCallSites, task, taskFieldScores(task, docs), func() []string {
		texts := make([]string, len(sites))
		for i, cs := range sites {
			texts[i] = "File: " + cs.Path + "\nLine " + strconv.Itoa(cs.Line) + ": " + boundRunes(cs.Excerpt, rerankExcerptRunes)
		}
		return texts
	})
	out := make([]PackCallSite, len(sites))
	for i, idx := range textrank.StableOrderByScore(scores) {
		out[i] = sites[idx]
	}
	return out
}

// rankImportEdges orders fit-tier module edges by task-IDF over from/to.
// Outbound edges keep relative order ahead of inbound when scores tie.
func rankImportEdges(ctx context.Context, rr decide.Reranker, task string, edges []PackImportEdge) []PackImportEdge {
	if len(edges) < 2 {
		return edges
	}
	docs := make([][]textrank.Field, len(edges))
	for i, e := range edges {
		docs[i] = []textrank.Field{{Text: e.From, Weight: 2}, {Text: e.To, Weight: 3}}
	}
	scores := taskFieldScores(task, docs)
	for i, e := range edges {
		if e.Kind == "outbound" {
			scores[i] += 1 // prefer known outbound when task has no overlap
		}
	}
	scores = rerank(ctx, rr, decide.SiteSummarizeImports, task, scores, func() []string {
		texts := make([]string, len(edges))
		for i, e := range edges {
			texts[i] = "Import (" + e.Kind + "): " + e.From + " -> " + e.To
		}
		return texts
	})
	out := make([]PackImportEdge, len(edges))
	for i, idx := range textrank.StableOrderByScore(scores) {
		out[i] = edges[idx]
	}
	return out
}

// diverseDefinitions admits each file once before revisiting its definitions.
func diverseDefinitions(ranked []DefinitionItem) []DefinitionItem {
	groups := map[string][]DefinitionItem{}
	var paths []string
	for _, d := range ranked {
		if _, ok := groups[d.RelPath]; !ok {
			paths = append(paths, d.RelPath)
		}
		groups[d.RelPath] = append(groups[d.RelPath], d)
	}
	out := make([]DefinitionItem, 0, len(ranked))
	for round := 0; len(out) < len(ranked); round++ {
		for _, path := range paths {
			if round < len(groups[path]) {
				out = append(out, groups[path][round])
			}
		}
	}
	return out
}
