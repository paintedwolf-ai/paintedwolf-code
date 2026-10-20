package projectpaths

import (
	"context"
	"io"
)

const pathOutputChunkBytes = 32 * 1024

// pathOutput meters attempted ingress before bounded writes; the surrounding
// producer decides completeness independently from any bytes already delivered.
type pathOutput struct {
	ctx              context.Context
	destination      io.Writer
	maximum, written int64
}

func newPathOutput(ctx context.Context, destination io.Writer, maximum int64) *pathOutput {
	if destination == nil {
		destination = io.Discard
	}
	return &pathOutput{ctx: ctx, destination: destination, maximum: maximum}
}
func (w *pathOutput) Write(data []byte) (int, error) {
	observed := w.written + int64(len(data))
	if observed > w.maximum {
		return 0, pathLimit("stream_bytes", "bytes", w.maximum, observed)
	}
	sent := 0
	for len(data) > 0 {
		if err := w.ctx.Err(); err != nil {
			return sent, err
		}
		length := min(len(data), pathOutputChunkBytes)
		n, err := w.destination.Write(data[:length])
		if n < 0 || n > length {
			return sent, io.ErrShortWrite
		}
		sent += n
		w.written += int64(n)
		if err != nil {
			return sent, err
		}
		if n != length {
			return sent, io.ErrShortWrite
		}
		data = data[length:]
	}
	return sent, nil
}
