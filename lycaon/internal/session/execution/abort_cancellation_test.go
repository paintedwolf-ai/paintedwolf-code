package execution

import (
	"context"
	"errors"
	"testing"
)

func TestPromptCancelPropagatesAbort(t *testing.T) {
	ctx := context.Background()
	parent, cancel := context.WithCancel(ctx)
	defer cancel()

	lifetime := &Lifetime{}
	promptCtx := lifetime.Attach(parent, "sess-1")
	done := make(chan error, 1)
	go func() {
		<-promptCtx.Done()
		done <- promptCtx.Err()
	}()

	lifetime.Cancel("sess-1")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
