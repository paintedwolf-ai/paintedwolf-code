package backup

import (
	"context"
	"fmt"
	"io"
)

type contextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

type archiveLimitWriter struct {
	dest      io.Writer
	remaining int64
}

func (w *archiveLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("backup: compressed archive exceeds operation budget")
	}
	n, err := w.dest.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func validateCaptureManifest(manifest Manifest, raw []byte) error {
	if len(raw) > maxManifestBytes || len(manifest.Files)+1 > maxArchiveEntries {
		return fmt.Errorf("backup: archive metadata exceeds restore limits")
	}
	expanded := uint64(len(raw))
	for _, file := range manifest.Files {
		if file.Size < 0 || expanded > MaxExpandedArchiveBytes || uint64(file.Size) > MaxExpandedArchiveBytes-expanded {
			return fmt.Errorf("backup: expanded archive exceeds operation budget")
		}
		if file.Kind == fileKindSymlink && (file.Size == 0 || file.Size > maxSymlinkTargetBytes) {
			return fmt.Errorf("backup: symlink target exceeds restore limits: %s", file.RelPath)
		}
		expanded += uint64(file.Size)
	}
	return nil
}
