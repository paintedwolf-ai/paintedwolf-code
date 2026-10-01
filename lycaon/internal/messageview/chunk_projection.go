package messageview

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// ChunkProjection remembers an accepted replacement or an unproductive attempt
// for one exact source and policy revision. A nil Meta means retain the source.
type ChunkProjection struct {
	Revision string                  `json:"revision"`
	Content  string                  `json:"content,omitempty"`
	Meta     *api.CompactedChunkMeta `json:"meta,omitempty"`
}

// ChunkProjectionStore has transcript lifetime, independently of session views.
type ChunkProjectionStore interface {
	GetChunkProjection(context.Context, string, string) (ChunkProjection, bool, error)
	PutChunkProjection(context.Context, string, string, ChunkProjection) error
}
