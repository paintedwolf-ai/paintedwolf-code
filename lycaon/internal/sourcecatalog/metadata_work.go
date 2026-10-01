package sourcecatalog

import (
	"context"

	"github.com/lycaon/lycaon/internal/backgroundwork"
)

const metadataWorkBatch = 128

type metadataWorkKey struct{}

type metadataWork struct {
	broker    *backgroundwork.Broker
	request   backgroundwork.Request
	release   func()
	remaining int
}

// admitMetadata makes long discoveries yield their lane between bounded batches.
func admitMetadata(ctx context.Context, broker *backgroundwork.Broker, request backgroundwork.Request) (context.Context, func(), error) {
	w := &metadataWork{broker: broker, request: request}
	if err := w.next(ctx); err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, metadataWorkKey{}, w), func() {
		if w.release != nil {
			w.release()
		}
	}, nil
}

func (w *metadataWork) next(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.remaining > 0 {
		w.remaining--
		return nil
	}
	if w.release != nil {
		w.release()
		w.release = nil
	}
	release, err := w.broker.Acquire(ctx, w.request)
	if err != nil {
		return err
	}
	w.release = release
	w.remaining = metadataWorkBatch - 1
	return nil
}

func nextMetadataEntry(ctx context.Context) error {
	if w, ok := ctx.Value(metadataWorkKey{}).(*metadataWork); ok {
		return w.next(ctx)
	}
	return ctx.Err()
}
