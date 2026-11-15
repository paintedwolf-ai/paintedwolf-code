//go:build darwin || linux

package adapter

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
	"golang.org/x/sys/unix"
)

// copyData copies in's data regions to the same offsets in out and leaves its holes as holes, so a
// sparse file stays sparse. It reads at most limit bytes when limit is positive, and reports the bytes
// it read. A volume that cannot report holes is copied whole.
func copyData(ctx context.Context, in, out *os.File, limit int64) (int64, error) {
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	var read int64
	for offset := int64(0); offset < size; {
		data, err := unix.Seek(int(in.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			break
		}
		if err != nil {
			if offset == 0 && (errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY)) {
				return copyWhole(ctx, in, out, limit)
			}
			return read, err
		}
		hole, err := unix.Seek(int(in.Fd()), data, unix.SEEK_HOLE)
		if err != nil {
			return read, err
		}
		span := hole - data
		if limit > 0 && read+span > limit {
			span = limit - read
		}
		n, err := io.Copy(io.NewOffsetWriter(out, data), contextio.Reader{Context: ctx, Source: io.NewSectionReader(in, data, span)})
		read += n
		if err != nil || (limit > 0 && read >= limit) {
			return read, err
		}
		offset = hole
	}
	return read, out.Truncate(size)
}

func copyWhole(ctx context.Context, in, out *os.File, limit int64) (int64, error) {
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var reader io.Reader = contextio.Reader{Context: ctx, Source: in}
	if limit > 0 {
		reader = io.LimitReader(reader, limit)
	}
	return io.Copy(out, reader)
}
