package survey

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/textfile"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

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

type summaryCountingReader struct {
	reader io.Reader
	read   int64
}

func (r *summaryCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	return n, err
}
