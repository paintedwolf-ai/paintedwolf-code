package nativegen

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type cancelOnRead struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelOnRead) Read(bytes []byte) (int, error) {
	r.once.Do(r.cancel)
	return r.ReadCloser.Read(bytes)
}

func TestCompletedStepCancellationKeepsResidentProcess(t *testing.T) {
	child, device, budget := fixtureProcess(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	child.output = &cancelOnRead{ReadCloser: child.output, cancel: cancel}
	if err := child.call(ctx, command{Method: "advance"}, "cleared", inference.Foreground, true, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("completed canceled step: %v", err)
	}
	select {
	case <-child.done:
		t.Fatal("completed cancellation discarded the model process")
	default:
	}
	if device.Snapshot().Active != nil || budget.Snapshot().ReservedBytes != 100 {
		t.Fatal("completed cancellation changed residency or retained compute admission")
	}
	testutil.FailErr(t, "reuse canceled process", child.call(t.Context(), command{Method: "clear_prefix"}, "cleared", inference.Foreground, true, nil))
}

func TestCanceledStepSettlesProgressBeforeRequestCleanup(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	testutil.FailErr(t, "warm cancellation fixture", runtime.Warm(t.Context(), DefaultModelID))
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Compute 2 + 2"}}, MaxTokens: 2}
	render, err := prepareRender(req)
	testutil.FailErr(t, "render cancellation fixture", err)
	cursor, err := runtime.prepare(t.Context(), render, nil, 2, modelcall.CompositionConversation)
	testutil.FailErr(t, "begin cancellation fixture", err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runtime.loaded.process.output = &cancelOnRead{ReadCloser: runtime.loaded.process.output, cancel: cancel}
	out := make(chan modelcall.StreamChunk, 1)
	provider.generate(ctx, req, DefaultModelID, cursor, out)
	if terminal := <-out; !terminal.Done || !errors.Is(terminal.Err, context.Canceled) {
		t.Fatalf("canceled generation: %+v", terminal)
	}
	if cursor.generated != 1 || cursor.processed != 3 {
		t.Fatal("canceled step lost acknowledged progress")
	}
	runtime.finishRequest(cursor)
	if _, err := provider.Complete(t.Context(), req); err != nil {
		t.Fatalf("generate after canceled request: %v", err)
	}
	if runtime.generation != 1 {
		t.Fatal("request cleanup forced a model reload")
	}
}

func TestCanceledNonStepCannotLeaveUnacknowledgedState(t *testing.T) {
	child, device, budget := fixtureProcess(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	child.output = &cancelOnRead{ReadCloser: child.output, cancel: cancel}
	if err := child.call(ctx, command{Method: "clear_prefix"}, "cleared", inference.Foreground, true, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled state transition: %v", err)
	}
	select {
	case <-child.done:
	default:
		t.Fatal("canceled state transition retained the process")
	}
	if device.Snapshot().Active != nil || budget.Snapshot().ReservedBytes != 0 {
		t.Fatal("canceled state transition retained admission")
	}
}
