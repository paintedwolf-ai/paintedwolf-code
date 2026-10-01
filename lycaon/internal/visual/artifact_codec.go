package visual

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// MaxDurableBodyBytes is the fixed decoded limit of the durable visual format.
const MaxDurableBodyBytes int64 = 128 * 1024 * 1024

// VerifyArtifactBody checks stored zstd bytes against the original media identity.
// Decoding streams with bounded memory and rejects bodies exceeding byteSize.
func VerifyArtifactBody(stored io.Reader, contentHash string, byteSize int64) error {
	return copyArtifactBody(io.Discard, stored, contentHash, byteSize)
}

func copyArtifactBody(dst io.Writer, stored io.Reader, contentHash string, byteSize int64) error {
	if byteSize < 0 || byteSize > MaxDurableBodyBytes {
		return fmt.Errorf("invalid artifact byte size")
	}
	decoder, err := zstd.NewReader(stored, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20), zstd.WithDecodeBuffersBelow(0))
	if err != nil {
		return fmt.Errorf("artifact decoder: %w", err)
	}
	defer decoder.Close()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, digest), io.LimitReader(decoder, byteSize))
	if err != nil {
		return fmt.Errorf("decode artifact: %w", err)
	}
	if n != byteSize {
		return fmt.Errorf("artifact byte size mismatch")
	}
	var extra [1]byte
	nExtra, err := io.ReadFull(decoder, extra[:])
	if nExtra != 0 {
		return fmt.Errorf("artifact exceeds recorded byte size")
	}
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("artifact trailing bytes: %w", err)
	}
	if hex.EncodeToString(digest.Sum(nil)) != contentHash {
		return fmt.Errorf("artifact content hash mismatch")
	}
	return nil
}
