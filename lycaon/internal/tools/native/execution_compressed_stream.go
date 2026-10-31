package native

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// Compressed storage has the same published byte identity as its decoded
// contents. Hash and consumption share the held descriptor and codec profile.
func (e ExecutionRevisions) openCompressedStream(ctx context.Context, _ tools.ToolContext, resolved projectpaths.Resolved) (ExecutionStream, error) {
	release, err := e.reserveWorking(ctx, zstdcodec.StreamReaderMemoryReservation)
	if err != nil {
		return ExecutionStream{}, err
	}
	file, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		release()
		return ExecutionStream{}, err
	}
	decoder, err := zstdcodec.NewStreamDecoder(file)
	if err != nil {
		_ = file.Close()
		release()
		return ExecutionStream{}, err
	}
	info, err := file.Stat()
	if err != nil {
		decoder.Close()
		_ = file.Close()
		release()
		return ExecutionStream{}, err
	}
	reader := &compressedExecutionReader{decoder: decoder, file: file, release: release, originalSize: info.Size()}
	captured, err := e.scanSource(ctx, resolved, decoder, true)
	if err == nil {
		reader.verifiedSource = captured
		err = captured.checkFile(file, info)
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = decoder.Reset(file)
	}
	if err != nil {
		_ = reader.Close()
		return ExecutionStream{}, err
	}
	return ExecutionStream{Path: resolved.DisplayPath, Reader: reader, Revision: captured.revision, FirstNullOffset: captured.firstNull}, nil
}

type compressedExecutionReader struct {
	*verifiedSource
	decoder      *zstd.Decoder
	originalSize int64
	checkedEnd   bool
	file         *os.File
	release      func()
}

func (r *compressedExecutionReader) Close() error {
	if r.release == nil {
		return nil
	}
	if r.verifiedSource != nil {
		r.verifiedSource.Close()
	}
	r.decoder.Close()
	err := r.file.Close()
	r.release()
	r.release = nil
	return err
}

func (r *compressedExecutionReader) Read(p []byte) (int, error) {
	if r.release == nil {
		return 0, io.ErrClosedPipe
	}
	info, err := r.file.Stat()
	if err != nil {
		return 0, r.conflict(err)
	}
	if info.Size() != r.originalSize {
		return 0, r.conflict(nil)
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.position >= r.revision.ContentBytes && !r.checkedEnd {
		var probe [1]byte
		n, end := r.decoder.Read(probe[:])
		if n != 0 || !errors.Is(end, io.EOF) {
			return 0, r.conflict(end)
		}
		r.checkedEnd = true
	}
	return r.read(p, func(_ int64, block []byte) (int, error) { return io.ReadFull(r.decoder, block) })
}
