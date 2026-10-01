package project

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
)

// Definition resolution caps. Hit either and Truncated is true.
const (
	DefinitionSearchHitCap = 200
	DefinitionFileCap      = 40
)

// SourceDefinitionRequest is POST …/source/definition.
type SourceDefinitionRequest struct {
	RootID string
	Path   string
	Symbol string
	Line   int
	// ExcludeDirs carries catalog-provided dependency and build directories.
	ExcludeDirs []string
}

// SourceDefinitionCandidate is one AST-confirmed declaration.
type SourceDefinitionCandidate struct {
	RootID  string
	Path    string
	Line    int
	Kind    SourceSymbolKind
	Snippet string
}

// SourceDefinitionResult is the ranked candidate list for one invocation.
type SourceDefinitionResult struct {
	Candidates []SourceDefinitionCandidate
	Truncated  bool
}

// ResolveProjectSourceDefinitions returns declarations from whole-word matches.
func ResolveProjectSourceDefinitions(
	ctx context.Context,
	p *Project,
	req SourceDefinitionRequest,
	search DeclarationSearch,
) (SourceDefinitionResult, error) {
	symbol := strings.TrimSpace(req.Symbol)
	if symbol == "" {
		return SourceDefinitionResult{Candidates: []SourceDefinitionCandidate{}}, nil
	}
	if p == nil || len(p.Roots) == 0 {
		return SourceDefinitionResult{}, ErrSourceNoRoot
	}
	if search == nil {
		return SourceDefinitionResult{}, errors.New("definition search is required")
	}
	root, err := selectSingleSourceRoot(p, req.RootID)
	if err != nil {
		return SourceDefinitionResult{}, err
	}
	originPath := filepathToSlash(req.Path)

	if err := ctx.Err(); err != nil {
		// Cancellation returns an empty truncated result.
		return SourceDefinitionResult{Candidates: []SourceDefinitionCandidate{}, Truncated: true}, nil //nolint:nilerr // cancellation is a truncated result
	}

	hits, truncated, err := search(ctx, DeclarationSearchQuery{
		ProjectID:   p.ID,
		Roots:       []DeclarationSearchRoot{{ID: root.ID, Path: root.Path}},
		Pattern:     symbol,
		Match:       DeclarationMatchWholeWord,
		ExcludeDirs: append([]string(nil), req.ExcludeDirs...),
		HitCap:      DefinitionSearchHitCap,
	})
	if err != nil {
		return SourceDefinitionResult{}, err
	}
	files := declarationFilesFromHits(hits)
	if len(files) > DefinitionFileCap {
		truncated = true
		files = files[:DefinitionFileCap]
	}

	perFile, incomplete := parseDeclarations(ctx, p, files, func(file declarationFile, symbols []SourceSymbol, content string) []SourceDefinitionCandidate {
		var out []SourceDefinitionCandidate
		for _, sym := range symbols {
			if sym.Name != symbol {
				continue
			}
			snippet := lineSnippet(content, sym.Line)
			if snippet == "" && len(file.snippets) > 0 {
				snippet = file.snippets[0]
			}
			out = append(out, SourceDefinitionCandidate{
				RootID:  file.rootID,
				Path:    file.path,
				Line:    sym.Line,
				Kind:    sym.Kind,
				Snippet: snippet,
			})
		}
		return out
	})
	truncated = truncated || incomplete
	candidates := make([]SourceDefinitionCandidate, 0, 8)
	for _, list := range perFile {
		candidates = append(candidates, list...)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return definitionRankLess(candidates[i], candidates[j], originPath, req.Line)
	})
	return SourceDefinitionResult{Candidates: candidates, Truncated: truncated}, nil
}

func filepathToSlash(p string) string {
	s := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	s = strings.TrimPrefix(s, "/")
	s = path.Clean("/" + s)
	s = strings.TrimPrefix(s, "/")
	if s == "." {
		return ""
	}
	return s
}

func definitionDir(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

func definitionRankLess(a, b SourceDefinitionCandidate, originPath string, originLine int) bool {
	aSameFile := a.Path == originPath
	bSameFile := b.Path == originPath
	if aSameFile != bSameFile {
		return aSameFile
	}
	if aSameFile && originLine > 0 {
		aDistance := definitionLineDistance(a.Line, originLine)
		bDistance := definitionLineDistance(b.Line, originLine)
		if aDistance != bDistance {
			return aDistance < bDistance
		}
	}
	originDir := definitionDir(originPath)
	aSameDir := definitionDir(a.Path) == originDir
	bSameDir := definitionDir(b.Path) == originDir
	if aSameDir != bSameDir {
		return aSameDir
	}
	aDepth := strings.Count(a.Path, "/")
	bDepth := strings.Count(b.Path, "/")
	if aDepth != bDepth {
		return aDepth < bDepth
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.Line < b.Line
}

func definitionLineDistance(line, originLine int) int {
	if line < originLine {
		return originLine - line
	}
	return line - originLine
}

func lineSnippet(content string, line int) string {
	if line < 1 || content == "" {
		return ""
	}
	start := 0
	cur := 1
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			if cur == line {
				return strings.TrimSpace(content[start:i])
			}
			cur++
			start = i + 1
		}
	}
	if cur == line {
		return strings.TrimSpace(content[start:])
	}
	return ""
}
