package sourceblob

import (
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var streamEncoders sync.Pool
var streamDecoders sync.Pool

// A file consumes one codec worker; operation admission controls parallelism.
func acquireEncoder(dst io.Writer) (*zstd.Encoder, error) {
	if held := streamEncoders.Get(); held != nil {
		encoder := held.(*zstd.Encoder)
		encoder.Reset(dst)
		return encoder, nil
	}
	return zstd.NewWriter(dst, zstd.WithEncoderConcurrency(1))
}

func releaseEncoder(encoder *zstd.Encoder) {
	encoder.Reset(io.Discard)
	streamEncoders.Put(encoder)
}

func acquireDecoder(src io.Reader) (*zstd.Decoder, error) {
	if held := streamDecoders.Get(); held != nil {
		decoder := held.(*zstd.Decoder)
		if err := decoder.Reset(src); err != nil {
			decoder.Close()
			return nil, err
		}
		return decoder, nil
	}
	return zstd.NewReader(src, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
}

func releaseDecoder(decoder *zstd.Decoder) {
	_ = decoder.Reset(nil)
	streamDecoders.Put(decoder)
}
