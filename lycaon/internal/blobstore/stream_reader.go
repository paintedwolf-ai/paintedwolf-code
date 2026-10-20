package blobstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// OpenStream uses the selected bounded single-worker decoder profile. The
// caller reserves StreamReaderMemoryReservation before opening and keeps that
// lease until Close. Generic Open consumers retain their existing profile.
func (s Store) OpenStream(b Blob) (io.ReadCloser, error) { return s.openReader(b, true) }

func (s Store) openReader(b Blob, bounded bool) (io.ReadCloser, error) {
	if !s.Available() {
		return nil, ErrNoStore
	}
	rel, _, err := s.findEntry(b.ID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.Root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	var dec *zstd.Decoder
	if bounded {
		dec, err = zstdcodec.NewStreamDecoder(f)
	} else {
		dec, err = zstd.NewReader(f)
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("blobstore: open blob decoder: %w", err)
	}
	return &decompressingBlob{file: f, dec: dec}, nil
}
