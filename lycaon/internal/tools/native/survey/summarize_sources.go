package survey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/tsparse"
)

type structureCacheEntry struct {
	sc summarize.StructureCandidate
	n  int
	ok bool
}

func (g *summarySources) structureFromAbs(ctx context.Context, abs, display string) (summarize.StructureCandidate, int, bool) {
	g.memoMu.Lock()
	if g.structureByPath != nil {
		if ent, ok := g.structureByPath[display]; ok {
			g.memoMu.Unlock()
			return ent.sc, ent.n, ent.ok
		}
	}
	g.memoMu.Unlock()

	content, err := g.readFileCached(ctx, abs)
	if err != nil {
		if errors.Is(err, errFileNeedsStreaming) {
			sc, n, ok := g.structureFromStream(ctx, abs, display)
			g.markPartialStructure(abs, &sc)
			g.storeStructure(display, sc, n, ok)
			return sc, n, ok
		}
		if errors.Is(err, errUnsupportedSummaryText) {
			g.noteNonTextPath(display)
		}
		g.noteSkippedPath(display)
		g.storeStructure(display, summarize.StructureCandidate{}, 0, false)
		return summarize.StructureCandidate{}, 0, false
	}
	sc, n, ok := g.structureFromContent(ctx, display, content)
	g.markPartialStructure(abs, &sc)
	g.storeStructure(display, sc, n, ok)
	return sc, n, ok
}

func (g *summarySources) storeStructure(display string, sc summarize.StructureCandidate, n int, ok bool) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.structureByPath == nil {
		g.structureByPath = map[string]structureCacheEntry{}
	}
	g.structureByPath[display] = structureCacheEntry{sc: sc, n: n, ok: ok}
}

func (g *summarySources) structureFromContent(ctx context.Context, display string, content []byte) (summarize.StructureCandidate, int, bool) {
	root := strings.TrimSpace(g.access.activeRoot)
	if root == "" {
		return summarize.StructureCandidate{}, 0, false
	}
	head := headLines(string(content), g.caps.Gather.FileHeadLines)
	outline, _ := fileoutline.BuildContent(ctx, display, content, int64(len(content)))
	g.noteParseFailure(display, "source", outline.ParseFailure)
	sc := summarize.StructureCandidate{
		RelPath:       display,
		Kind:          summarize.StructureKindFile,
		Head:          head,
		StartLine:     1,
		LineCount:     strings.Count(string(content), "\n") + 1,
		Language:      outline.Language,
		OutlineSource: outline.Source,
		Parses:        outline.Parses,
	}
	if outline.TotalLines > 0 {
		sc.LineCount = outline.TotalLines
	}
	lines := strings.Split(string(content), "\n")
	for _, sym := range outline.Symbols {
		sc.Symbols = append(sc.Symbols, summarize.StructureSymbol{
			Kind: sym.Kind, Name: sym.Name, Line: sym.Line,
			Signature: summarize.SignatureLine(lines, sym.Line), Doc: summarize.LeadingComment(lines, sym.Line),
		})
	}
	if len(outline.Errors) > 0 {
		limit := 8
		if len(outline.Errors) < limit {
			limit = len(outline.Errors)
		}
		for _, e := range outline.Errors[:limit] {
			kind := "unexpected syntax"
			if e.Kind == syntaxhealth.DiagnosticMissing {
				kind = "missing " + e.NodeType
			}
			sc.Errors = append(sc.Errors, fmt.Sprintf("%d:%d %s: %s", e.Row, e.Col, kind, e.Snippet))
		}
		sc.ErrorKind = summarizeErrorKind(outline.Errors)
	}
	if outline.Source == "log" && outline.LogDigest != nil {
		sc.LogDigest = fmt.Sprintf("%s records=%d parsed=%d", outline.LogDigest.Format, outline.LogDigest.RecordCount, outline.LogDigest.ParsedCount)
	}
	if ip := g.moduleImport(ctx, display); ip != "" {
		sc.ImportPath = ip
	}
	sc = summarize.FocusSource(ctx, g.rerank, g.task, sc, string(content), g.caps.Gather.FileHeadLines)
	sc.ContentHash = structureContentHashPattern(sc, "")
	return sc, len(sc.Head), true
}

func summarizeErrorKind(diags []fileoutline.SyntaxDiagnostic) string {
	for _, d := range diags {
		if d.Kind == syntaxhealth.DiagnosticError {
			return summarize.ErrorKindUnexpected
		}
	}
	if len(diags) > 0 {
		return summarize.ErrorKindIncomplete
	}
	return ""
}

func headLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (g *summarySources) noteParseFailure(path, phase string, failure *tsparse.Failure) {
	if failure == nil {
		return
	}
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	g.parseFailureCount++
	if len(g.parseFailures) < tsparse.MaxFailureExamples {
		g.parseFailures = append(g.parseFailures, tsparse.FileFailure{Path: path, Phase: phase, Failure: failure})
	}
}

func (g *summarySources) Outline(ctx context.Context, relPath string) (summarize.StructureCandidate, bool) {
	resolved, err := g.access.reads.Resolve(ctx, relPath)
	if err != nil {
		return summarize.StructureCandidate{}, false
	}
	sc, _, ok := g.structureFromAbs(ctx, resolved.Abs, resolved.DisplayPath)
	return sc, ok
}

func (g *summarySources) structureFromStream(ctx context.Context, abs, display string) (summarize.StructureCandidate, int, bool) {
	chunkBytes := g.caps.Gather.FileChunkBytes
	if chunkBytes <= 0 {
		chunkBytes = 1 << 20
	}
	sc := summarize.StructureCandidate{
		RelPath: display,
		Kind:    summarize.StructureKindFile,
	}
	headLimit := chunkBytes
	headLines := g.caps.Gather.FileHeadLines
	headLineCount := 0
	var head strings.Builder
	var observed strings.Builder
	digest := sha256.New()
	totalLines := 1
	err := g.scanSummaryChunks(ctx, abs, chunkBytes, func(startLine int, chunk []byte) error {
		_, _ = observed.Write(chunk)
		_, _ = digest.Write(chunk)
		totalLines += bytes.Count(chunk, []byte{'\n'})
		appendSummaryHead(&head, chunk, headLimit, headLines, &headLineCount)

		outline, err := fileoutline.BuildContent(ctx, display, chunk, int64(len(chunk)))
		if err != nil {
			return err
		}
		g.noteParseFailure(display, "source chunk", outline.ParseFailure)
		if outline.Parses != nil && !*outline.Parses {
			sc.Parses = outline.Parses
		}
		if sc.Language == "" {
			sc.Language = outline.Language
		}
		if sc.OutlineSource == "" {
			sc.OutlineSource = outline.Source
		}
		chunkLines := strings.Split(string(chunk), "\n")
		for _, symbol := range outline.Symbols {
			sc.Symbols = append(sc.Symbols, summarize.StructureSymbol{
				Kind:      symbol.Kind,
				Name:      symbol.Name,
				Line:      startLine + symbol.Line - 1,
				Signature: summarize.SignatureLine(chunkLines, symbol.Line),
				Doc:       summarize.LeadingComment(chunkLines, symbol.Line),
			})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errUnsupportedSummaryText) {
			g.noteNonTextPath(display)
		}
		g.noteSkippedPath(display)
		return summarize.StructureCandidate{}, 0, false
	}
	sc.Head = head.String()
	sc.StartLine = 1
	sc = summarize.FocusSource(ctx, g.rerank, g.task, sc, observed.String(), headLines)
	sc.LineCount = totalLines
	sc.ContentHash = hex.EncodeToString(digest.Sum(nil))
	if ip := g.moduleImport(ctx, display); ip != "" {
		sc.ImportPath = ip
	}
	return sc, len(sc.Head), true
}

func appendSummaryHead(dst *strings.Builder, chunk []byte, maxBytes, maxLines int, lineCount *int) {
	if dst.Len() >= maxBytes || (maxLines > 0 && *lineCount >= maxLines) {
		return
	}
	for _, b := range chunk {
		if dst.Len() >= maxBytes || (maxLines > 0 && *lineCount >= maxLines) {
			return
		}
		_ = dst.WriteByte(b)
		if b == '\n' {
			(*lineCount)++
		}
	}
}

func (g *summarySources) noteSkippedPath(path string) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.skippedPaths == nil {
		g.skippedPaths = map[string]bool{}
	}
	g.skippedPaths[path] = true
}

func (g *summarySources) noMaterialData(req summarize.Request) map[string]any {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	roots := append([]string(nil), req.Paths...)
	if len(roots) == 0 && req.Path != "" {
		roots = []string{req.Path}
	}
	skipped := make([]string, 0, len(g.skippedPaths))
	for path := range g.skippedPaths {
		skipped = append(skipped, path)
	}
	sort.Strings(skipped)
	path := ""
	if len(roots) == 1 {
		path = roots[0]
	}
	return map[string]any{
		"path": path, "paths": roots, "pattern": req.Pattern,
		"summary_source_limited": g.sourceLimited,
		"summary_skipped_count":  len(skipped),
		"summary_skipped_paths":  skipped[:min(len(skipped), 10)],
	}
}

func (g *summarySources) noteNonTextPath(path string) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.nonTextPaths == nil {
		g.nonTextPaths = map[string]bool{}
	}
	g.nonTextPaths[path] = true
}

func (g *summarySources) actionableSources(actions []summarize.NextAction) []summarize.NextAction {
	out := make([]summarize.NextAction, 0, len(actions))
	for _, action := range actions {
		if g.nonTextPaths[action.Path] {
			continue
		}
		out = append(out, action)
	}
	return out
}
