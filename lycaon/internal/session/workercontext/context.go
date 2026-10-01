// Package workercontext carries the active worker job through transcript writes and closeout reads.
package workercontext

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type workerJobContextKey struct{}

// WithJob binds an active worker run.
func WithJob(ctx context.Context, workerJobID string) context.Context {
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return ctx
	}
	return context.WithValue(ctx, workerJobContextKey{}, workerJobID)
}

func Job(ctx context.Context) string {
	workerJobID, _ := ctx.Value(workerJobContextKey{}).(string)
	return strings.TrimSpace(workerJobID)
}

func Stamp(ctx context.Context, messages []api.Message) error {
	workerJobID := Job(ctx)
	if workerJobID == "" {
		return nil
	}
	for i := range messages {
		existing := strings.TrimSpace(messages[i].WorkerID)
		if existing != "" && existing != workerJobID {
			return fmt.Errorf("message worker job mismatch: %s != %s", existing, workerJobID)
		}
		messages[i].WorkerID = workerJobID
	}
	return nil
}
