package nativegen

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

type prefixRequest struct {
	RequestID        uint64   `json:"request_id"`
	PromptTokens     []uint32 `json:"prompt_tokens"`
	CheckpointTokens int      `json:"checkpoint_tokens"`
}

type preparationResult struct {
	Kind     string `json:"kind"`
	Progress struct {
		RequestID uint64 `json:"request_id"`
		Processed int    `json:"processed_prompt_tokens"`
		Reused    int    `json:"reused_prefix_tokens"`
		Complete  bool   `json:"complete"`
		Canceled  bool   `json:"canceled"`
	} `json:"progress"`
}

// PrepareContext evaluates the assembler's declared known prefix while idle.
func (p *Provider) PrepareContext(ctx context.Context, req modelcall.CompletionRequest) (modelcall.ContextPreparation, error) {
	if p.runtime == nil {
		return modelcall.ContextPreparation{}, nil
	}
	render, err := prepareRender(req)
	if err != nil || render.KnownPrefixMessages == 0 {
		return modelcall.ContextPreparation{}, err
	}
	standingMessages := render.CheckpointMessages
	render.CheckpointMessages = render.KnownPrefixMessages
	model := p.ResolveModel(req)
	if _, ok := p.ModelEntry(model); !ok {
		return modelcall.ContextPreparation{}, errors.New("native generation: model is not configured")
	}
	ctx, release, ok := p.runtime.acquirePreparation(ctx)
	if !ok {
		return modelcall.ContextPreparation{}, nil
	}
	defer release()
	// Preparation does not load or replace the user's active model.
	if p.runtime.loaded == nil || p.runtime.loaded.model != model {
		return modelcall.ContextPreparation{}, nil
	}
	measured, err := p.runtime.measure(ctx, render)
	if err != nil || measured.CheckpointTokens == 0 {
		return modelcall.ContextPreparation{}, err
	}
	if measured.CheckpointTokens >= p.runtime.ContextWindow() {
		return modelcall.ContextPreparation{}, ErrContextCapacityExceeded
	}
	checkpoint := measured.CheckpointTokens
	if standingMessages != render.KnownPrefixMessages {
		checkpoint = 0
		if standingMessages > 0 {
			render.CheckpointMessages = standingMessages
			standing, err := p.runtime.measure(ctx, render)
			if err != nil {
				return modelcall.ContextPreparation{}, err
			}
			if !slices.Equal(standing.PromptTokens, measured.PromptTokens) || standing.CheckpointTokens > measured.CheckpointTokens {
				return modelcall.ContextPreparation{}, errors.New("native generation: inconsistent preparation boundaries")
			}
			checkpoint = standing.CheckpointTokens
		}
	}
	return p.runtime.prefill(ctx, measured.PromptTokens[:measured.CheckpointTokens], checkpoint)
}

func (p *Runtime) prefill(ctx context.Context, tokens []uint32, checkpoint int) (modelcall.ContextPreparation, error) {
	p.loaded.request++
	id := p.loaded.request
	child := p.loaded.process
	done := false
	defer func() {
		if done {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var result preparationResult
		if err := child.call(cleanup, command{Method: "cancel_preparation", RequestID: id}, "preparation_progress", inference.Foreground, true, &result); err != nil {
			var service *ServiceError
			if !errors.As(err, &service) || service.Code != "invalid_request" {
				child.close()
			}
		}
	}()
	var begun begunResult
	if err := child.call(ctx, command{Method: "prepare", Request: prefixRequest{RequestID: id, PromptTokens: tokens, CheckpointTokens: checkpoint}}, "begun", inference.Background, true, &begun); err != nil {
		return modelcall.ContextPreparation{}, err
	}
	if begun.RequestID != id || begun.ReusedPrefixTokens < 0 || begun.ReusedPrefixTokens > len(tokens) {
		child.close()
		return modelcall.ContextPreparation{}, errors.New("native generation: invalid preparation acknowledgement")
	}
	processed := begun.ReusedPrefixTokens
	for {
		var result preparationResult
		if err := child.call(ctx, command{Method: "advance_preparation", RequestID: id}, "preparation_progress", inference.Background, true, &result); err != nil {
			return modelcall.ContextPreparation{}, err
		}
		next := result.Progress
		expected := min(processed+p.config.PrefillChunk, len(tokens))
		if processed < checkpoint {
			expected = min(expected, checkpoint)
		}
		if next.RequestID != id || next.Processed != expected || next.Reused != begun.ReusedPrefixTokens || next.Canceled || next.Complete != (expected == len(tokens)) {
			child.close()
			return modelcall.ContextPreparation{}, errors.New("native generation: invalid preparation progress")
		}
		processed = next.Processed
		if next.Complete {
			done = true
			return modelcall.ContextPreparation{PreparedTokens: processed, ReusedTokens: next.Reused}, nil
		}
	}
}
