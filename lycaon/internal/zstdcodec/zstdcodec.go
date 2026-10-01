// Package zstdcodec is the host's one zstd buffer codec, shared by every
// store that compresses complete in-memory payloads before writing them.
package zstdcodec

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// Compress encodes r at the default level.
func Compress(r io.Reader) ([]byte, error) {
	return compressAt(r, nil)
}

// CompressLevel encodes r at an explicit encoder level.
func CompressLevel(r io.Reader, level zstd.EncoderLevel) ([]byte, error) {
	return compressAt(r, []zstd.EOption{zstd.WithEncoderLevel(level)})
}

func compressAt(r io.Reader, opts []zstd.EOption) ([]byte, error) {
	var out bytes.Buffer
	encoder, err := zstd.NewWriter(&out, opts...)
	if err != nil {
		return nil, fmt.Errorf("zstd writer: %w", err)
	}
	if _, err := io.Copy(encoder, r); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Decompress decodes a complete zstd object, regardless of the encoder level
// that produced it — zstd decoding is level-agnostic.
func Decompress(z []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	defer decoder.Close()
	out, err := decoder.DecodeAll(z, nil)
	if err != nil {
		return nil, fmt.Errorf("zstd decode: %w", err)
	}
	return out, nil
}

// ErrDecodedLimit identifies a decoded byte or decoder memory limit.
var ErrDecodedLimit = errors.New("zstd decoded resource limit exceeded")

// DecompressBounded refuses malformed or oversized durable payloads.
func DecompressBounded(z []byte, limit int64) ([]byte, error) {
	return ReadBounded(bytes.NewReader(z), limit)
}

// ReadBounded streams a compressed object without retaining the encoded body.
// Both output allocation and the decoder window are bounded; no partial output
// is returned on failure. The decoder's minimum window is independent of the
// accepted output length so small valid frames remain readable.
func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 || limit == int64(^uint64(0)>>1) {
		return nil, fmt.Errorf("invalid zstd decoded size limit")
	}
	memoryLimit := max(uint64(limit), 8<<20)
	decoder, err := zstd.NewReader(r, zstd.WithDecoderMaxMemory(memoryLimit), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	defer decoder.Close()
	out, err := io.ReadAll(io.LimitReader(decoder, limit+1))
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
		return nil, fmt.Errorf("%w: %w", ErrDecodedLimit, err)
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("%w: maximum %d bytes", ErrDecodedLimit, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("zstd decode: %w", err)
	}
	return out, nil
}
