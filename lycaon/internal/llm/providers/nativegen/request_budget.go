package nativegen

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

type measuredResult struct {
	renderedResult
	InputTokens int `json:"input_tokens"`
}

type preparedPrompt struct {
	key         [32]byte
	generation  uint64
	measurement measuredResult
}

func (p *Provider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	budget := modelcall.RequestBudget{Output: outputLimit(req)}
	if p.runtime == nil {
		budget.Err = errors.New("native generation: host runtime is unavailable")
		return budget
	}
	if len(req.Messages) == 0 {
		// Frame-only measurement retains the template's empty user turn.
		req.Messages = []api.Message{{Role: api.MessageRoleUser}}
	}
	render, err := prepareRender(req)
	if err != nil {
		budget.Err = err
		return budget
	}
	ctx, release, err := p.runtime.acquire(ctx)
	if err != nil {
		budget.Err = err
		return budget
	}
	defer release()
	if err := p.runtime.ensureLoaded(ctx, p.ResolveModel(req)); err != nil {
		budget.Err = err
		return budget
	}
	measurement, err := p.runtime.measure(ctx, render)
	budget.Err = err
	if err == nil {
		budget.Input, budget.Window, budget.Exact = measurement.InputTokens, p.runtime.ContextWindow(), true
	}
	return budget
}

func (p *Runtime) measure(ctx context.Context, render renderRequest) (measuredResult, error) {
	body, err := json.Marshal(render)
	if err != nil {
		return measuredResult{}, err
	}
	key := sha256.Sum256(body)
	if cached := p.prepared; cached != nil && cached.key == key && cached.generation == p.generation {
		return cached.measurement, nil
	}
	var measured measuredResult
	if err := p.loaded.process.call(ctx, command{Method: "measure", Request: render}, "measured", inference.Foreground, false, &measured); err != nil {
		return measuredResult{}, err
	}
	p.prepared = &preparedPrompt{key: key, generation: p.generation, measurement: measured}
	return measured, nil
}

func (m *measuredResult) validate() error {
	if m.InputTokens < 1 || m.InputTokens > 4<<20 || m.CheckpointTokens < 0 || m.CheckpointTokens > 4096 || m.CheckpointTokens >= m.InputTokens {
		return errors.New("native generation: invalid measured context")
	}
	if m.InputTokens > DefaultContextWindowTokens {
		if len(m.PromptTokens) != 0 || m.CheckpointTokens != 0 {
			return errors.New("native generation: oversized measurement retained tokens")
		}
		return nil
	}
	if len(m.PromptTokens) != m.InputTokens {
		return errors.New("native generation: measurement count differs from tokens")
	}
	for _, token := range m.PromptTokens {
		if token >= 262144 {
			return errors.New("native generation: invalid measured token")
		}
	}
	return nil
}
