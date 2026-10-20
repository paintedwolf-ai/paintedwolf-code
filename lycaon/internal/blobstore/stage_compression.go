package blobstore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// compressStagedFile rewrites unpublished plaintext as a zstd frame through the
// existing staging boundary. Cancellation leaves cleanup with the caller.
func compressStagedFile(ctx context.Context, tmp *os.File) error {
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind plaintext: %w", err)
	}
	// Preserve the one staging/commit boundary while avoiding an encoded body
	// proportional to stream size in memory. Both files remain unpublished.
	compressed, err := os.CreateTemp(filepath.Dir(tmp.Name()), ".compressed-*")
	if err != nil {
		return fmt.Errorf("create compressed stage: %w", err)
	}
	defer func() { _ = compressed.Close(); _ = os.Remove(compressed.Name()) }()
	if err := zstdcodec.CompressTo(contextWriter{ctx: ctx, target: compressed}, contextReader{ctx: ctx, source: tmp}); err != nil {
		return fmt.Errorf("compress: %w", err)
	}
	if _, err := compressed.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind compressed stage: %w", err)
	}
	if err := tmp.Truncate(0); err != nil {
		return fmt.Errorf("truncate plaintext: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind for compressed write: %w", err)
	}
	if _, err := io.Copy(contextWriter{ctx: ctx, target: tmp}, contextReader{ctx: ctx, source: compressed}); err != nil {
		return fmt.Errorf("write compressed body: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

type contextWriter struct {
	ctx    context.Context
	target io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.target.Write(p)
}
