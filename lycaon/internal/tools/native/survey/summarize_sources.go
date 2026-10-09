package survey

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tsparse"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type structureCacheEntry struct {
	sc summarize.StructureCandidate
	n  int
	ok bool
}

func (g *summarySources) cachedBytes(abs string) ([]byte, bool) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.bytesByAbs == nil {
		return nil, false
	}
	b, ok := g.bytesByAbs[abs]
	return b, ok
}

func (g *summarySources) storeBytes(abs string, content []byte) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.bytesByAbs == nil {
		g.bytesByAbs = map[string][]byte{}
	}
	g.bytesByAbs[abs] = content
}

// openRead opens one file or directory through the gather's read session.

func (g *summarySources) openRead(ctx context.Context, abs string) (*os.File, error) {
	resolved, err := g.access.reads.Resolve(ctx, abs)
	if err != nil {
		return nil, err
	}
	return g.access.reads.Open(resolved)
}

func (g *summarySources) readFileCached(ctx context.Context, abs string) ([]byte, error) {
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

func (g *summarySources) scanSummaryChunks(ctx context.Context, abs string, chunkBytes int, visit func(int, []byte) error) error {
	f, err := g.openRead(ctx, abs)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	limit, err := g.reserveSourceRead(abs, info.Size())
	if err != nil {
		return err
	}
	counted := &summaryCountingReader{reader: io.LimitReader(f, limit)}
	defer func() { g.recordSourceBytes(counted.read) }()
	reader := summaryDecodedReader(counted)
	buffered := bufio.NewReaderSize(reader, 64*1024)
	validator := textfile.UTF8Validator{}
	chunk := make([]byte, 0, chunkBytes)
	line := 1
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		part := append([]byte(nil), chunk...)
		if err := visit(line, part); err != nil {
			return err
		}
		line += bytes.Count(part, []byte{'\n'})
		chunk = chunk[:0]
		return nil
	}
	for {
		fragment, readErr := buffered.ReadSlice('\n')
		atEOF := errors.Is(readErr, io.EOF)
		if atEOF && limit < info.Size() {
			fragment = trimSummaryUTF8Tail(fragment)
		}
		if !validator.Add(fragment, atEOF && limit == info.Size()) {
			return errUnsupportedSummaryText
		}
		if len(fragment) <= chunkBytes {
			if len(chunk) > 0 && len(chunk)+len(fragment) > chunkBytes {
				if err := flush(); err != nil {
					return err
				}
			}
			chunk = append(chunk, fragment...)
			if len(chunk) == chunkBytes {
				if err := flush(); err != nil {
					return err
				}
			}
			fragment = nil
		}
		for len(fragment) > 0 {
			space := chunkBytes - len(chunk)
			if space <= 0 {
				if err := flush(); err != nil {
					return err
				}
				space = chunkBytes
			}
			take := summaryUTF8Prefix(fragment, min(space, len(fragment)))
			if take == 0 {
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			chunk = append(chunk, fragment[:take]...)
			fragment = fragment[take:]
			if len(chunk) == chunkBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if atEOF {
			break
		}
		if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
			return readErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return flush()
}

func summaryUTF8Prefix(content []byte, limit int) int {
	if limit >= len(content) {
		return len(content)
	}
	for limit > 0 && !utf8.RuneStart(content[limit]) {
		limit--
	}
	if limit == 0 {
		_, size := utf8.DecodeRune(content)
		return min(size, len(content))
	}
	return limit
}

func summaryDecodedReader(reader io.Reader) io.Reader {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	prefix, _ := buffered.Peek(3)
	if len(prefix) >= 2 && prefix[0] == 0xff && prefix[1] == 0xfe {
		return transform.NewReader(buffered, unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder())
	}
	if len(prefix) >= 2 && prefix[0] == 0xfe && prefix[1] == 0xff {
		return transform.NewReader(buffered, unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder())
	}
	if len(prefix) >= 3 && prefix[0] == 0xef && prefix[1] == 0xbb && prefix[2] == 0xbf {
		_, _ = buffered.Discard(3)
	}
	return buffered
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

var errSummaryReadBudget = errors.New("summary source read budget exhausted")

func trimSummaryUTF8Tail(raw []byte) []byte {
	start := len(raw) - 1
	for start > 0 && !utf8.RuneStart(raw[start]) {
		start--
	}
	if start >= 0 && !utf8.FullRune(raw[start:]) {
		return raw[:start]
	}
	return raw
}

func (g *summarySources) reserveSourceRead(abs string, size int64) (int64, error) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.readReservations == nil {
		g.readReservations = map[string]int64{}
	}
	if _, ok := g.readReservations[abs]; ok {
		g.sourceLimited = true
		return 0, errSummaryReadBudget
	}
	if g.caps.Gather.MaxFilesRead > 0 && g.sourceFiles >= g.caps.Gather.MaxFilesRead {
		g.sourceLimited = true
		return 0, errSummaryReadBudget
	}
	remaining := int64(g.caps.Gather.MaxBytes) - g.sourceBytes
	if remaining <= 0 {
		g.sourceLimited = true
		return 0, errSummaryReadBudget
	}
	n := min(size, remaining, int64(g.caps.Gather.FileReadBytes))
	g.readReservations[abs] = n
	g.sourceFiles++
	g.sourceBytes += n
	if n < size {
		g.sourceLimited = true
		if g.truncatedPaths == nil {
			g.truncatedPaths = map[string]bool{}
		}
		g.truncatedPaths[abs] = true
	}
	return n, nil
}

func (g *summarySources) markPartialStructure(abs string, sc *summarize.StructureCandidate) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if !g.truncatedPaths[abs] {
		return
	}
	sc.LineCount = 0
	sc.Parses = nil
	sc.Errors = nil
	sc.ErrorKind = ""
}

type summaryCountingReader struct {
	reader io.Reader
	read   int64
}

func (r *summaryCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	return n, err
}

func (g *summarySources) recordSourceBytes(n int64) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	g.sourceReadBytes += n
}

func (g *summarySources) sourceBudgetExhausted() bool {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	full := g.sourceBytes >= int64(g.caps.Gather.MaxBytes) || (g.caps.Gather.MaxFilesRead > 0 && g.sourceFiles >= g.caps.Gather.MaxFilesRead)
	g.sourceLimited = g.sourceLimited || full
	return full
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
