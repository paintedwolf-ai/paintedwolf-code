package pagedview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type commandResult[T any] struct {
	Result   T
	Revision string
}

// Commands serializes intent and retains receipts until the view is released.
type Commands[T any] struct {
	mu        sync.Mutex
	revision  string
	namespace string
	receipts  *Receipts
	private   bool
	closed    bool
}

func NewCommands[T any](receipts *Receipts) *Commands[T] {
	private := receipts == nil
	if private {
		receipts = &Receipts{}
	}
	return &Commands[T]{revision: uuid.NewString(), namespace: uuid.NewString(), receipts: receipts, private: private}
}

func (c *Commands[T]) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.private {
		c.receipts.Close()
	} else if err := c.receipts.Release(c.namespace); err != nil {
		slog.Warn("source view receipt cleanup failed", "error", err)
	}
}

func (c *Commands[T]) Revision() string { c.mu.Lock(); defer c.mu.Unlock(); return c.revision }

// Apply accepts canonical intent bytes. Commit installs intent atomically before returning.
func (c *Commands[T]) Apply(ctx context.Context, id, expected string, canonical []byte, commit func(string) (T, error)) (T, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	if c.closed {
		return zero, c.revision, ErrExpired
	}
	digest := sha256.Sum256(canonical)
	saved, ok, err := c.receipts.Lookup(ctx, c.namespace, id)
	if err != nil {
		return zero, c.revision, err
	}
	if ok {
		if !bytes.Equal(saved.Digest, digest[:]) {
			return zero, c.revision, ErrOperationConflict
		}
		var result commandResult[T]
		if err := json.Unmarshal(saved.Value, &result); err != nil {
			return zero, c.revision, ErrExpired
		}
		return result.Result, result.Revision, nil
	}
	if expected != c.revision {
		return zero, c.revision, ErrRevision
	}
	if id == "" {
		return zero, c.revision, ErrOperationConflict
	}
	if err := c.receipts.Reserve(ctx, c.namespace, id, digest[:]); err != nil {
		return zero, c.revision, err
	}
	next := uuid.NewString()
	result, err := commit(next)
	if err != nil {
		if abortErr := c.receipts.Abort(ctx, c.namespace, id); abortErr != nil {
			return zero, c.revision, abortErr
		}
		return zero, c.revision, err
	}
	c.revision = next
	payload, encodeErr := json.Marshal(commandResult[T]{Result: result, Revision: next})
	if encodeErr != nil {
		return zero, next, ErrExpired
	}
	if err := c.receipts.Complete(ctx, c.namespace, id, payload); err != nil {
		return zero, next, err
	}
	return result, next, nil
}
