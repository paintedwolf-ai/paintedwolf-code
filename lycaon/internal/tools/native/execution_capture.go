package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	"io"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/zstdcodec"
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

// readCapturedBytes owns whole-input capacity before allocation and preserves
// one descriptor, decoder and capture lifetime for every command caller.
func (e ExecutionRevisions) readCapturedBytes(ctx context.Context, resolved projectpaths.Resolved, maximum int64) (data []byte, release func(), finalErr error) {
	if maximum <= 0 {
		return nil, nil, fmt.Errorf("whole input capture requires a positive bound")
	}
	file, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		return nil, nil, err
	}
	var observation *sourcecatalog.ContentRead
	defer func() {
		defer observation.Close()
		if closeErr := file.Close(); closeErr != nil {
			if release != nil {
				release()
				data, release = nil, nil
			}
			if finalErr == nil {
				finalErr = closeErr
			} else {
				finalErr = errors.Join(finalErr, closeErr)
			}
		}
		if finalErr == nil {
			observation.Commit()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, &toolrejection.ToolReject{Code: "READ_IS_DIRECTORY", Data: map[string]any{"path": resolved.DisplayPath}}
	}
	if !resolved.Compressed && info.Size() > maximum {
		return nil, nil, sourceview.AccessCommand.SizeReject("read", resolved.DisplayPath, info.Size(), maximum)
	}
	var reader io.Reader = file
	if resolved.Compressed {
		release, err := e.reserveWorking(ctx, zstdcodec.StreamReaderMemoryReservation)
		if err != nil {
			return nil, nil, err
		}
		defer release()
		decoder, err := zstdcodec.NewStreamDecoder(file)
		if err != nil {
			return nil, nil, err
		}
		defer decoder.Close()
		reader = decoder
	}
	observation = e.observeFile(resolved, info)
	return e.readWholeInput(ctx, observation.Wrap(ctx, reader), maximum, resolved.DisplayPath)
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
		return 0, tools.CommandLimitExceeded(b.limitID, b.buffer.maximum, int64(len(b.Bytes))+int64(len(p)))
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

func (b *BufferedBytes) Close() {
	if !b.closed {
		b.closed = true
		b.buffer.Close()
		b.Bytes = nil
	}
}

// ReadWholeInput gathers a genuinely buffered operand using the same capacity
// owner as file capture. Admission precedes allocation and growth overlap.
func (e ExecutionRevisions) ReadWholeInput(ctx context.Context, reader io.Reader, maximum int64) (*BufferedBytes, error) {
	return e.readBufferedBytes(ctx, reader, maximum)
}

func (e ExecutionRevisions) readBufferedBytes(ctx context.Context, reader io.Reader, maximum int64) (*BufferedBytes, error) {
	buffer, err := e.NewBufferedBytes(ctx, maximum, "whole_file_bytes")
	if err != nil {
		return nil, err
	}
	release, err := e.reserveWorking(ctx, 32<<10)
	if err != nil {
		buffer.Close()
		return nil, err
	}
	defer release()
	var chunk [32 << 10]byte
	_, err = io.CopyBuffer(buffer, io.LimitReader(reader, maximum+1), chunk[:])
	if err != nil {
		buffer.Close()
		return nil, err
	}
	return buffer, nil
}

func (e ExecutionRevisions) readWholeInput(ctx context.Context, reader io.Reader, maximum int64, displayPath string) ([]byte, func(), error) {
	buffer, err := e.readBufferedBytes(ctx, reader, maximum)
	if err != nil {
		if reject := toolrejection.AsToolReject(err); reject != nil && reject.Code == "COMMAND_LIMIT_EXCEEDED" {
			reject.Data["path"] = displayPath
		}
		return nil, nil, err
	}
	return buffer.Bytes, buffer.Close, nil
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
