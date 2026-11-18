// Package consumertest supplies typed source-index fixtures.
package consumertest

import (
	"context"

	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
)

// IndexFunc supplies source-index results for one fixture.
type IndexFunc func(context.Context, consumer.SourceIndexQuery) (consumer.SourceIndexResult, error)

func (fn IndexFunc) QueryIndex(ctx context.Context, query consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
	return fn(ctx, query)
}
