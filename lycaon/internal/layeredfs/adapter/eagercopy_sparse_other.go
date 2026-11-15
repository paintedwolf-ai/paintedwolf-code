//go:build !darwin && !linux

package adapter

import (
	"context"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
)

// copyData copies in to out whole; this platform reports no holes.
func copyData(ctx context.Context, in, out *os.File, limit int64) (int64, error) {
	var reader io.Reader = contextio.Reader{Context: ctx, Source: in}
	if limit > 0 {
		reader = io.LimitReader(reader, limit)
	}
	return io.Copy(out, reader)
}
