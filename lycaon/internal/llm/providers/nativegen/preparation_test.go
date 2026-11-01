package nativegen

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreparationRequiresAnIdleResidentModelAndDeclaredBoundary(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	request := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleSystem, Content: "Instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "Question"},
	}}
	result, err := provider.PrepareContext(t.Context(), request)
	testutil.FailErr(t, "skip unloaded model", err)
	if result.PreparedTokens != 0 || runtime.loaded != nil {
		t.Fatal("preparation loaded a model")
	}
	testutil.FailErr(t, "warm model", runtime.Warm(t.Context(), DefaultModelID))
	child := runtime.loaded.process
	result, err = provider.PrepareContext(t.Context(), request)
	testutil.FailErr(t, "prepare context", err)
	if result.PreparedTokens != 1 || result.ReusedTokens != 0 || runtime.loaded.process != child {
		t.Fatalf("preparation: %+v", result)
	}
	_, release, err := runtime.acquire(t.Context())
	testutil.FailErr(t, "admit foreground", err)
	result, err = provider.PrepareContext(t.Context(), request)
	release()
	testutil.FailErr(t, "skip foreground", err)
	if result.PreparedTokens != 0 {
		t.Fatal("preparation displaced foreground")
	}
	request.Messages[0].PromptCacheBreakpoint = api.PromptCacheTierNone
	result, err = provider.PrepareContext(t.Context(), request)
	testutil.FailErr(t, "skip absent boundary", err)
	if result.PreparedTokens != 0 {
		t.Fatal("prepared an undeclared prefix")
	}
}

func TestPreparationCancellationSettlesWithoutReload(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	testutil.FailErr(t, "warm model", runtime.Warm(t.Context(), DefaultModelID))
	child := runtime.loaded.process
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	child.output = &cancelOnRead{ReadCloser: child.output, cancel: cancel}
	_, err := runtime.prefill(ctx, []uint32{2, 5, 6, 7, 8, 9})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation: %v", err)
	}
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}, MaxTokens: 2}
	_, err = provider.Complete(t.Context(), req)
	testutil.FailErr(t, "generate after canceled preparation", err)
	if runtime.loaded.process != child || runtime.generation != 1 {
		t.Fatal("preemption reloaded the model")
	}
}

func TestPreparationAdvancesAcrossBoundedChunks(t *testing.T) {
	_, runtime := fixtureProvider(t)
	testutil.FailErr(t, "warm model", runtime.Warm(t.Context(), DefaultModelID))
	result, err := runtime.prefill(t.Context(), []uint32{2, 5, 6, 7, 8, 9, 10, 11, 12})
	testutil.FailErr(t, "prepare multiple chunks", err)
	if result.PreparedTokens != 9 || result.ReusedTokens != 0 {
		t.Fatalf("preparation: %+v", result)
	}
}
