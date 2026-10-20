package blobstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func compressStagedFile(tmp *os.File) error {
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
	if err := zstdcodec.CompressTo(compressed, tmp); err != nil {
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
	if _, err := io.Copy(tmp, compressed); err != nil {
		return fmt.Errorf("write compressed body: %w", err)
	}
	return nil
}
