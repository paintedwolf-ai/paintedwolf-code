package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// ErrBlobTooLarge is reported for a blob above the caller's byte bound.
var ErrBlobTooLarge = errors.New("git blob exceeds the byte bound")

// ReadBlobs streams each object's exact bytes, in request order, through one
// cat-file batch. A blob above maxBytes reaches yield with ErrBlobTooLarge
// and no content; the batch continues past it.
func ReadBlobs(ctx context.Context, dir string, oids []string, maxBytes int64, yield func(index int, content []byte, err error) error) error {
	if len(oids) == 0 {
		return nil
	}
	var input strings.Builder
	for _, oid := range oids {
		if !validObjectID(oid) {
			return fmt.Errorf("invalid Git object id %q", oid)
		}
		input.WriteString(oid)
		input.WriteByte('\n')
	}
	opts := hermeticOpts(ReadCommandTimeout)
	opts.Input = []byte(input.String())
	parser := &blobBatch{ctx: ctx, maxBytes: maxBytes, yield: yield, want: len(oids)}
	out, code, err := gitexec.RunTo(ctx, dir, []string{"--no-replace-objects", "cat-file", "--batch"}, opts, parser)
	if parser.err != nil {
		return parser.err
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("read Git blobs: %s", bytes.TrimSpace(out))
	}
	if parser.index != len(oids) || parser.header != nil || parser.remaining != 0 {
		return errors.New("read Git blobs: batch ended inside an object")
	}
	return nil
}

// blobBatch parses "<oid> <type> <size>\n<bytes>\n" records as they arrive.
type blobBatch struct {
	ctx       context.Context
	maxBytes  int64
	yield     func(int, []byte, error) error
	want      int
	index     int
	header    []byte
	content   []byte
	remaining int64
	discard   bool
	trailer   bool
	err       error
}

func (b *blobBatch) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n := len(p)
	for len(p) > 0 {
		if err := b.ctx.Err(); err != nil {
			b.err = err
			return 0, err
		}
		switch {
		case b.trailer:
			if p[0] != '\n' {
				b.err = errors.New("read Git blobs: malformed batch record")
				return 0, b.err
			}
			p = p[1:]
			b.trailer = false
			b.err = b.finish()
		case b.remaining > 0:
			take := min(int64(len(p)), b.remaining)
			if !b.discard {
				b.content = append(b.content, p[:take]...)
			}
			p = p[take:]
			b.remaining -= take
			b.trailer = b.remaining == 0
		default:
			end := bytes.IndexByte(p, '\n')
			if end < 0 {
				b.header = append(b.header, p...)
				p = nil
				continue
			}
			b.header = append(b.header, p[:end]...)
			p = p[end+1:]
			b.err = b.start(string(b.header))
			b.header = nil
		}
		if b.err != nil {
			return 0, b.err
		}
	}
	return n, nil
}

func (b *blobBatch) start(header string) error {
	if b.index >= b.want {
		return errors.New("read Git blobs: unexpected batch record")
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[1] == "missing" {
		return fmt.Errorf("read Git blobs: object %s is missing", fields[0])
	}
	if len(fields) != 3 || fields[1] != "blob" {
		return fmt.Errorf("read Git blobs: %q is not a blob record", header)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return errors.New("read Git blobs: malformed blob size")
	}
	b.discard = b.maxBytes > 0 && size > b.maxBytes
	b.content = nil
	if !b.discard {
		b.content = make([]byte, 0, size)
	}
	b.remaining = size
	b.trailer = size == 0
	return nil
}

func (b *blobBatch) finish() error {
	index := b.index
	b.index++
	if b.discard {
		return b.yield(index, nil, ErrBlobTooLarge)
	}
	return b.yield(index, b.content, nil)
}
