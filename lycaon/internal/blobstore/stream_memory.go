package blobstore

import (
	"context"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/bytebound"
)

// StreamMemoryAdmission reserves codec working storage against the enclosing
// program. It must refuse unavailable capacity rather than wait for a lease
// held by the same program's blocked producer or consumer.
type StreamMemoryAdmission interface {
	Reserve(context.Context, int64) (func(), error)
}

// PutStream uses the existing staging and commit path, with cancellation and
// codec admission after the input reaches EOF. This lets a completed upstream
// screen release its scratch before compression starts. It never revokes an
// already completed filesystem commit.
func (s Store) PutStream(ctx context.Context, name string, r io.Reader, limit bytebound.Materialization, memory StreamMemoryAdmission) (Blob, error) {
	if memory == nil {
		return Blob{}, fmt.Errorf("blobstore: stream codec admission required")
	}
	return s.put(ctx, name, r, limit, memory)
}
