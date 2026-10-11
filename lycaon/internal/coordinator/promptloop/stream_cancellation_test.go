package promptloop

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

type stoppedStreamClient struct {
	cancel      context.CancelFunc
	beforeStart bool
	content     string
	err         error
}

func (s stoppedStreamClient) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, s.err
}
func (s stoppedStreamClient) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if s.beforeStart {
		s.cancel()
		return nil, s.err
	}
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		if s.content != "" {
			ch <- modelcall.StreamChunk{Content: s.content}
		}
		s.cancel()
	}()
	return ch, nil
}

func TestStoppedPromptStreamPreservesCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		beforeStart bool
		content     string
		err         error
	}{
		{name: "empty stream"},
		{name: "partial stream", content: "partial answer"},
		{name: "provider error during cancellation", beforeStart: true, err: errors.New("provider failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := stoppedStreamClient{cancel: cancel, beforeStart: tc.beforeStart, content: tc.content, err: tc.err}
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Model: ModelDeps{
					LLM: client,
				},
			})
			_, err := loop.Model.collectPromptStream(ctx, &api.Session{ID: "worker"}, client, modelcall.CompletionRequest{}, nil, "", "provider", "model")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("stopped stream error=%v want cancellation", err)
			}
		})
	}
}
