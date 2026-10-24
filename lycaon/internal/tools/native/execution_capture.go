package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"io"
)

// Program captures use their existing aggregate accounts. Standalone host
// revision checks retain the same bounded reader without a program lifetime.
func (e ExecutionRevisions) reserveCapture(ctx context.Context, n int64) (func(), error) {
	if e.Memory == nil {
		return func() {}, ctx.Err()
	}
	return e.Memory.ReserveProgramState(ctx, n)
}
func (e ExecutionRevisions) reserveWorking(ctx context.Context, n int64) (func(), error) {
	if e.Memory == nil {
		return func() {}, ctx.Err()
	}
	return e.Memory.Reserve(ctx, n)
}

// BufferedBytes owns one admitted whole operand or staged result. It carries
// no source identity; CaptureBytes and OpenStream own file pins.
type BufferedBytes struct {
	Bytes   []byte
	buffer  captureBuffer
	limitID string
	closed  bool
}

// NewBufferedBytes uses the capture owner's existing capacity admission for a
// genuinely buffered result. Every growth includes old/new capacity overlap.
func (e ExecutionRevisions) NewBufferedBytes(ctx context.Context, maximum int64, limitID string) (*BufferedBytes, error) {
	if maximum <= 0 || limitID == "" {
		return nil, fmt.Errorf("buffered bytes require a positive named limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &BufferedBytes{buffer: captureBuffer{revisions: e, ctx: ctx, maximum: maximum}, limitID: limitID}, nil
}

func (b *BufferedBytes) Write(p []byte) (int, error) {
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if err := b.buffer.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > b.buffer.maximum-int64(len(b.Bytes)) {
		return 0, nativeutility.LimitExceeded(b.limitID, b.buffer.maximum, int64(len(b.Bytes))+int64(len(p)))
	}
	if err := b.buffer.Append(p); err != nil {
		return 0, err
	}
	b.Bytes = b.buffer.bytes
	return len(p), nil
}

// Reset starts another record in the same admitted backing capacity. Existing
// byte views must be discarded before calling it; the lease survives until Close.
func (b *BufferedBytes) Reset() error {
	if b.closed {
		return io.ErrClosedPipe
	}
	if err := b.buffer.ctx.Err(); err != nil {
		return err
	}
	b.buffer.bytes = b.buffer.bytes[:0]
	b.Bytes = b.buffer.bytes
	return nil
}

func (b *BufferedBytes) Close() error {
	if !b.closed {
		b.closed = true
		b.buffer.Close()
		b.Bytes = nil
	}
	return nil
}

type captureBuffer struct {
	revisions ExecutionRevisions
	ctx       context.Context
	bytes     []byte
	maximum   int64
	release   func()
}

func (b *captureBuffer) Append(p []byte) error {
	wanted := len(b.bytes) + len(p)
	if wanted > cap(b.bytes) {
		floor := int64(64)
		if cap(b.bytes) > 0 {
			floor = max(64<<10, 2*int64(cap(b.bytes)))
		}
		capacity := min(b.maximum, max(int64(wanted), floor))
		reserved := min(b.maximum, max(int64(wanted), max(64<<10, 2*int64(cap(b.bytes)))))
		release, err := b.revisions.reserveCapture(b.ctx, reserved)
		if err != nil {
			return err
		}
		bytes := make([]byte, len(b.bytes), int(capacity))
		copy(bytes, b.bytes)
		if b.release != nil {
			b.release()
		}
		b.bytes, b.release = bytes, release
	}
	b.bytes = append(b.bytes, p...)
	return nil
}
func (b *captureBuffer) Close() {
	if b.release != nil {
		b.release()
		b.release = nil
	}
	b.bytes = nil
}
