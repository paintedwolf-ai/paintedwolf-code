package survey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// All concurrent searches share the same file-read budget.
var grepReadSlots = make(chan struct{}, grepReadCap)

// readGrepFile rejects binary prefixes before allocating the full read cap.
// BOM-marked text uses the same decoder as read.
func readGrepFile(ctx context.Context, reads *projectpaths.ReadSession, absPath string, info fs.FileInfo) (content []byte, binary bool, bytesTruncated bool, err error) {
	select {
	case grepReadSlots <- struct{}{}:
		defer func() { <-grepReadSlots }()
	case <-ctx.Done():
		return nil, false, false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, false, fmt.Errorf("search requires a regular file")
	}
	resolved, err := reads.Resolve(ctx, absPath)
	if err != nil {
		return nil, false, false, err
	}
	f, err := reads.Open(resolved)
	if err != nil {
		return nil, false, false, err
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil {
		return nil, false, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, false, fmt.Errorf("search target is no longer a regular file")
	}
	limit := min(info.Size(), int64(hostGrepMaxFileBytes))
	bytesTruncated = info.Size() > limit
	prefix, err := io.ReadAll(io.LimitReader(f, min(limit, 4096)))
	if err != nil {
		return nil, false, false, err
	}
	if textfile.Classify(prefix).Class == textfile.Binary {
		return nil, true, false, nil
	}
	content = prefix
	if remaining := limit - int64(len(prefix)); remaining > 0 {
		content = make([]byte, limit)
		copy(content, prefix)
		n, readErr := io.ReadFull(&grepContextReader{ctx: ctx, reader: f}, content[len(prefix):])
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return nil, false, false, readErr
		}
		content = content[:len(prefix)+n]
	}
	if err := ctx.Err(); err != nil {
		return nil, false, false, err
	}
	if bytesTruncated {
		content = textfile.TrimIncompleteTail(content)
	}
	text, _, err := textfile.Decode(content, textfile.LimitsForRaw(int64(hostGrepMaxFileBytes)))
	if err != nil {
		return nil, true, bytesTruncated, nil
	}
	return []byte(text), false, bytesTruncated, nil
}

type grepContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *grepContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p[:min(len(p), 64<<10)])
}
