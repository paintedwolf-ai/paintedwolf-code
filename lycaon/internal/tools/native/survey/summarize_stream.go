package survey

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/textfile"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func (g *summarizeGatherer) structureFromStream(ctx context.Context, abs, display string) (summarize.StructureCandidate, int, bool) {
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
	sc = summarize.FocusSource(ctx, g.rerank, g.treeRequest.Task, sc, observed.String(), headLines)
	sc.LineCount = totalLines
	sc.ContentHash = hex.EncodeToString(digest.Sum(nil))
	if ip := g.goPackageImportPath(ctx, display); ip != "" {
		sc.ImportPath = ip
	}
	return sc, len(sc.Head), true
}

func (g *summarizeGatherer) scanSummaryChunks(ctx context.Context, abs string, chunkBytes int, visit func(int, []byte) error) error {
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
