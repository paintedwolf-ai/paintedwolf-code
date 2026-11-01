package nativegen

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

type prefixRequest struct {
	RequestID    uint64   `json:"request_id"`
	PromptTokens []uint32 `json:"prompt_tokens"`
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

// PrepareContext evaluates the assembler's declared standing prefix while idle.
func (p *Provider) PrepareContext(ctx context.Context, req modelcall.CompletionRequest) (modelcall.ContextPreparation, error) {
	if p.runtime == nil {
		return modelcall.ContextPreparation{}, nil
	}
	render, err := prepareRender(req)
	if err != nil || render.CheckpointMessages == 0 {
		return modelcall.ContextPreparation{}, err
	}
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
	return p.runtime.prefill(ctx, measured.PromptTokens[:measured.CheckpointTokens])
}

func (p *Runtime) prefill(ctx context.Context, tokens []uint32) (modelcall.ContextPreparation, error) {
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
	if err := child.call(ctx, command{Method: "prepare", Request: prefixRequest{id, tokens}}, "begun", inference.Background, true, &begun); err != nil {
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
