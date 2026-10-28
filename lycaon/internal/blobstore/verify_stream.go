package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// VerifyStream validates durable recovery bytes with the selected bounded
// decoder. The caller reserves its decoder and supplied scratch before entry.
func (s Store) VerifyStream(ctx context.Context, blob Blob, scratch []byte) error {
	if ctx == nil || len(scratch) == 0 || blob.Size < 0 {
		return fmt.Errorf("blob verification requires bounded scratch and size")
	}
	reader, err := s.OpenStream(blob)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	digest := sha256.New()
	var count int64
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.Read(scratch)
		if n < 0 || n > len(scratch) {
			return fmt.Errorf("invalid blob decoder read")
		}
		if int64(n) > blob.Size-count {
			return fmt.Errorf("retained blob size mismatch")
		}
		count += int64(n)
		_, _ = digest.Write(scratch[:n])
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			break
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	if count != blob.Size || attachmentID(hex.EncodeToString(digest.Sum(nil)), blob.Name) != blob.ID {
		return fmt.Errorf("retained blob content identity mismatch")
	}
	return reader.Close()
}
