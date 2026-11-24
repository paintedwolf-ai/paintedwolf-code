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
	_, err := runtime.prefill(ctx, []uint32{2, 5, 6, 7, 8, 9}, 0)
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
	result, err := runtime.prefill(t.Context(), []uint32{2, 5, 6, 7, 8, 9, 10, 11, 12}, 3)
	testutil.FailErr(t, "prepare multiple chunks", err)
	if result.PreparedTokens != 9 || result.ReusedTokens != 0 {
		t.Fatalf("preparation: %+v", result)
	}
}

func TestPreparationPassesStandingBoundaryInsideKnownHistory(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_PREPARATION_BOUNDARIES", "1")
	provider, runtime := fixtureProvider(t)
	runtime.config.PrefillChunk = 1
	testutil.FailErr(t, "warm model", runtime.Warm(t.Context(), DefaultModelID))
	request := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleSystem, Content: "Instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "Known history", PromptCacheBreakpoint: api.PromptCacheTierHistory},
		{Role: api.MessageRoleAssistant, Content: "Answer"},
	}}
	result, err := provider.PrepareContext(t.Context(), request)
	testutil.FailErr(t, "prepare known history with standing checkpoint", err)
	if result.PreparedTokens != 2 {
		t.Fatalf("prepared tokens = %d, want 2", result.PreparedTokens)
	}
}

func TestPreparationLeavesAnIncompleteHistoryChunkForForeground(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_PREPARATION_BOUNDARIES", "1")
	t.Setenv("PW_NATIVEGEN_PREPARATION_STANDING_ONLY", "1")
	provider, runtime := fixtureProvider(t)
	testutil.FailErr(t, "warm model", runtime.Warm(t.Context(), DefaultModelID))
	request := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleSystem, Content: "Instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "Known history", PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}}
	result, err := provider.PrepareContext(t.Context(), request)
	testutil.FailErr(t, "prepare aligned prefix", err)
	if result.PreparedTokens != 1 {
		t.Fatalf("prepared tokens = %d, want standing boundary before partial history chunk", result.PreparedTokens)
	}
}

func TestPreparationRetainsStandingWhenHistoryExceedsContext(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_PREPARATION_BOUNDARIES", "1")
	t.Setenv("PW_NATIVEGEN_PREPARATION_STANDING_ONLY", "1")
	provider, runtime := fixtureProvider(t)
	testutil.FailErr(t, "warm model", runtime.Warm(t.Context(), DefaultModelID))
	runtime.window.Store(2)
	request := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleSystem, Content: "Instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "Known history", PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}}
	result, err := provider.PrepareContext(t.Context(), request)
	testutil.FailErr(t, "prepare standing before history fitting", err)
	if result.PreparedTokens != 1 {
		t.Fatalf("prepared tokens = %d, want 1", result.PreparedTokens)
	}
	runtime.window.Store(1)
	_, err = provider.PrepareContext(t.Context(), request)
	if !errors.Is(err, ErrContextCapacityExceeded) {
		t.Fatalf("oversized standing prefix: %v", err)
	}
}

func TestPreparationReusesPreHistoryRootGuidanceAndHistoryOnCompletion(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_CONTENT_TOKENS", "1")
	provider, runtime := fixtureProvider(t)
	runtime.config.PrefillChunk = 6
	testutil.FailErr(t, "warm model", runtime.Warm(t.Context(), DefaultModelID))

	rootGuidance := api.Message{
		ID:        "host-agent-policy-root",
		Role:      api.MessageRoleSystem,
		Content:   "## Project agent guidance\n\nAlways verify with ./task.",
		Origin:    api.MessageOriginProject,
		Authority: api.ContentAuthorityDeveloper,
		TrustTier: api.ContentTrustTierTrusted,
	}
	prepared := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleSystem, Content: "Standing system instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		rootGuidance,
		{Role: api.MessageRoleUser, Content: "Earlier question"},
		{Role: api.MessageRoleAssistant, Content: "Earlier answer", PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}}
	wire, err := prepareRender(prepared)
	testutil.FailErr(t, "render prepared request", err)
	if wire.CheckpointMessages != 1 || wire.KnownPrefixMessages != 4 {
		t.Fatalf("unexpected boundaries: checkpoint=%d known=%d", wire.CheckpointMessages, wire.KnownPrefixMessages)
	}
	if wire.Messages[0].Role != "system" || wire.Messages[1].Role != "user" {
		t.Fatalf("expected standing system then user-role project guidance, got roles %q and %q", wire.Messages[0].Role, wire.Messages[1].Role)
	}

	prepResult, err := provider.PrepareContext(t.Context(), prepared)
	testutil.FailErr(t, "prepare context with root guidance", err)
	if prepResult.PreparedTokens != 8 {
		t.Fatalf("prepared tokens = %d, want 8 (standing + root guidance + 2 history rows)", prepResult.PreparedTokens)
	}

	live := modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "Standing system instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding},
			rootGuidance,
			{Role: api.MessageRoleUser, Content: "Earlier question"},
			{Role: api.MessageRoleAssistant, Content: "Earlier answer"},
			{Role: api.MessageRoleUser, Content: "Follow-up question", PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Content: "Dynamic execution state"},
		},
		MaxTokens: 2,
	}
	comp, err := provider.Complete(t.Context(), live)
	testutil.FailErr(t, "complete after preparation", err)
	if comp.Usage.CacheReadInputTokens != 8 {
		t.Fatalf("reused prefix tokens = %d, want 8 (standing + root guidance + prior history)", comp.Usage.CacheReadInputTokens)
	}
}
