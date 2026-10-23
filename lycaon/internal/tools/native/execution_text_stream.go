package native

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// OpenTextStream keeps the existing raw revision pin while a query's codec
// projects its captured representation to UTF-8. It does not gather the file
// or claim that the raw pin names a different decoded representation.
func (e ExecutionRevisions) OpenTextStream(ctx context.Context, tc tools.ToolContext, path string) (ExecutionStream, error) {
	stream, err := e.OpenStream(ctx, tc, path, 0)
	if err != nil {
		return ExecutionStream{}, err
	}
	release, err := e.reserveWorking(ctx, 128<<10)
	if err != nil {
		_ = stream.Reader.Close()
		return ExecutionStream{}, err
	}
	decoder, err := textfile.NewUTF8Reader(stream.Reader, "")
	if err != nil {
		release()
		_ = stream.Reader.Close()
		return ExecutionStream{}, err
	}
	original := stream.Reader
	stream.Reader = &captureReadCloser{reader: textStreamReader{ctx: ctx, reader: decoder, path: stream.Path}, release: func() { _ = original.Close(); release() }}
	return stream, nil
}

type textStreamReader struct {
	ctx    context.Context
	reader io.Reader
	path   string
}

func (r textStreamReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && r.ctx.Err() == nil {
		if errors.Is(err, textfile.ErrBinary) || errors.Is(err, textfile.ErrUnsupported) {
			err = &toolrejection.ToolReject{Code: "READ_BINARY_DENIED", Data: map[string]any{"path": r.path, "reason": fmt.Sprint(err)}}
		}
	}
	return n, err
}
