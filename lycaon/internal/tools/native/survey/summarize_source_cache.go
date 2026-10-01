package survey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tsparse"
)

type structureCacheEntry struct {
	sc summarize.StructureCandidate
	n  int
	ok bool
}

func (g *summarizeGatherer) cachedBytes(abs string) ([]byte, bool) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.bytesByAbs == nil {
		return nil, false
	}
	b, ok := g.bytesByAbs[abs]
	return b, ok
}

func (g *summarizeGatherer) storeBytes(abs string, content []byte) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.bytesByAbs == nil {
		g.bytesByAbs = map[string][]byte{}
	}
	g.bytesByAbs[abs] = content
}

// openRead opens one file or directory through the gather's read session.
func (g *summarizeGatherer) openRead(ctx context.Context, abs string) (*os.File, error) {
	resolved, err := g.reads.Resolve(ctx, abs)
	if err != nil {
		return nil, err
	}
	return g.reads.Open(resolved)
}

func (g *summarizeGatherer) readFileCached(ctx context.Context, abs string) ([]byte, error) {
	g.readMu.Lock()
	defer g.readMu.Unlock()
	if b, ok := g.cachedBytes(abs); ok {
		return b, nil
	}
	f, err := g.openRead(ctx, abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	chunkBytes := g.caps.Gather.FileChunkBytes
	if chunkBytes > 0 && info.Size() > int64(chunkBytes) {
		return nil, errFileNeedsStreaming
	}
	limit := info.Size()
	limit, err = g.reserveSourceRead(abs, limit)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit))
	g.recordSourceBytes(int64(len(raw)))
	if err != nil {
		return nil, err
	}
	if limit < info.Size() {
		if len(raw) >= 2 && ((raw[0] == 0xff && raw[1] == 0xfe) || (raw[0] == 0xfe && raw[1] == 0xff)) {
			raw = raw[:len(raw)-len(raw)%2]
		} else {
			raw = trimSummaryUTF8Tail(raw)
		}
	}
	decodeLimit := int64(chunkBytes)
	if decodeLimit <= 0 {
		decodeLimit = int64(len(raw))
	}
	doc, _, err := textfile.Open(raw, textfile.LimitsForRaw(decodeLimit))
	if err != nil {
		return nil, errUnsupportedSummaryText
	}
	content := []byte(doc.Text())
	g.storeBytes(abs, content)
	return content, nil
}

var errUnsupportedSummaryText = errors.New("unsupported summary text")

var errFileNeedsStreaming = errors.New("file requires streaming structure extraction")

func (g *summarizeGatherer) structureFromAbs(ctx context.Context, abs, display string) (summarize.StructureCandidate, int, bool) {
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

func (g *summarizeGatherer) storeStructure(display string, sc summarize.StructureCandidate, n int, ok bool) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.structureByPath == nil {
		g.structureByPath = map[string]structureCacheEntry{}
	}
	g.structureByPath[display] = structureCacheEntry{sc: sc, n: n, ok: ok}
}

func (g *summarizeGatherer) structureFromContent(ctx context.Context, display string, content []byte) (summarize.StructureCandidate, int, bool) {
	root := strings.TrimSpace(g.tctx.ActiveRootPath())
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
	if ip := g.goPackageImportPath(ctx, display); ip != "" {
		sc.ImportPath = ip
	}
	sc = summarize.FocusSource(ctx, g.rerank, g.treeRequest.Task, sc, string(content), g.caps.Gather.FileHeadLines)
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

func (g *summarizeGatherer) noteParseFailure(path, phase string, failure *tsparse.Failure) {
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
