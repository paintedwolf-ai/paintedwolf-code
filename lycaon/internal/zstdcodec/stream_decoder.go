package zstdcodec

import (
	"io"

	"github.com/klauspost/compress/zstd"
)

// StreamDecoderWindow bounds stream-reader history. The single-worker profile
// accepts existing 8 MiB recompression windows while fitting the separately
// admitted 32 MiB scratch envelope, including history growth and block buffers.
const StreamDecoderWindow uint64 = 8 << 20

// StreamReaderMemoryReservation covers bounded history growth and decoder block
// buffers for the selected single-worker profile, independently of encoder leases.
const StreamReaderMemoryReservation int64 = 32 << 20

// NewStreamDecoder disables eager whole-body decoding and constrains window,
// decoder working-memory and concurrency at the existing codec boundary.
// These logical capacity limits do not represent a process heap/RSS ceiling.
func NewStreamDecoder(r io.Reader) (*zstd.Decoder, error) {
	return zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(StreamDecoderWindow), zstd.WithDecoderMaxWindow(StreamDecoderWindow), zstd.WithDecodeBuffersBelow(0))
}
