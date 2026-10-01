package llm

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

type dispatchObserverKey struct{}
type dispatchObserver struct {
	once   sync.Once
	notify func()
}

// WithDispatchObserver observes the first dispatch after preparation and screening.
func WithDispatchObserver(ctx context.Context, notify func()) context.Context {
	return context.WithValue(ctx, dispatchObserverKey{}, &dispatchObserver{notify: notify})
}

func notifyDispatch(ctx context.Context) {
	observer, _ := ctx.Value(dispatchObserverKey{}).(*dispatchObserver)
	if observer != nil && observer.notify != nil {
		observer.once.Do(observer.notify)
	}
}

type dispatchClient struct{ modelcall.LLMClient }

// WithClientDispatch supplies the dispatch boundary for directly injected clients.
// Registry clients publish at their screened provider boundary instead.
func WithClientDispatch(client modelcall.LLMClient) modelcall.LLMClient {
	if client == nil {
		return nil
	}
	return &dispatchClient{LLMClient: client}
}

func (c *dispatchClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	notifyDispatch(ctx)
	return c.LLMClient.Complete(ctx, req)
}

func (c *dispatchClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	notifyDispatch(ctx)
	return c.LLMClient.Stream(ctx, req)
}
