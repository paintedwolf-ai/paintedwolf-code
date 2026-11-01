package nativegen

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

type beginRequest struct {
	RequestID        uint64          `json:"request_id"`
	PromptTokens     []uint32        `json:"prompt_tokens"`
	MaxNewTokens     int             `json:"max_new_tokens"`
	Temperature      float32         `json:"temperature"`
	TopP             float32         `json:"top_p"`
	TopK             uint32          `json:"top_k"`
	Seed             uint64          `json:"seed"`
	CheckpointTokens int             `json:"checkpoint_tokens"`
	OutputSchema     json.RawMessage `json:"output_schema,omitempty"`
}

func (p *Provider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	stream, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	completion, _, err := modelcall.CollectStream(stream)
	return completion, err
}

func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	render, err := prepareRender(req)
	if err != nil {
		return nil, err
	}
	schema, err := outputSchema(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	model := p.ResolveModel(req)
	if p.runtime == nil {
		return nil, errors.New("native generation: local runtime is not configured")
	}
	if _, ok := p.ModelEntry(model); !ok {
		return nil, errors.New("native generation: model is not configured")
	}
	ctx, release, err := p.runtime.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.runtime.ensureLoaded(ctx, model); err != nil {
		release()
		return nil, err
	}
	maximum := outputLimit(req)
	if maximum <= 0 || maximum >= p.runtime.ContextWindow() {
		release()
		return nil, ErrContextCapacityExceeded
	}
	req.RecordBudget(maximum, nil, nil)
	cursor, err := p.runtime.prepare(ctx, render, schema, maximum)
	if err != nil {
		release()
		return nil, err
	}
	out := make(chan modelcall.StreamChunk, 8)
	go func() {
		defer close(out)
		defer release()
		defer p.runtime.finishRequest(cursor)
		p.generate(ctx, req, model, cursor, out)
	}()
	return out, nil
}

func (p *Runtime) prepare(ctx context.Context, render renderRequest, schema json.RawMessage, maximum int) (*progressCursor, error) {
	child := p.loaded.process
	var rendered renderedResult
	if err := child.call(ctx, command{Method: "render", Request: render, MaxPromptTokens: p.ContextWindow() - maximum}, "rendered", inference.Foreground, false, &rendered); err != nil {
		return nil, err
	}
	if len(rendered.PromptTokens) == 0 || len(rendered.PromptTokens)+maximum > p.ContextWindow() || rendered.CheckpointTokens < 0 || rendered.CheckpointTokens > 4096 || rendered.CheckpointTokens >= len(rendered.PromptTokens) {
		child.close()
		return nil, errors.New("native generation: invalid rendered context")
	}
	for _, token := range rendered.PromptTokens {
		if token >= 262144 {
			child.close()
			return nil, errors.New("native generation: invalid rendered token")
		}
	}
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	p.loaded.request++
	sampling := p.loaded.sampling
	request := beginRequest{RequestID: p.loaded.request, PromptTokens: rendered.PromptTokens, MaxNewTokens: maximum, Temperature: sampling.Temperature, TopP: sampling.TopP, TopK: sampling.TopK, Seed: binary.LittleEndian.Uint64(entropy[:]), CheckpointTokens: rendered.CheckpointTokens, OutputSchema: schema}
	var begun begunResult
	if err := child.call(ctx, command{Method: "begin", Request: request}, "begun", inference.Foreground, true, &begun); err != nil {
		return nil, err
	}
	if begun.RequestID != request.RequestID || begun.ReusedPrefixTokens != 0 && begun.ReusedPrefixTokens != rendered.CheckpointTokens {
		child.close()
		return nil, errors.New("native generation: invalid prefix acknowledgement")
	}
	return &progressCursor{requestID: request.RequestID, prompt: len(rendered.PromptTokens), processed: begun.ReusedPrefixTokens, reused: begun.ReusedPrefixTokens, maximum: maximum, chunk: p.config.PrefillChunk, schema: len(schema) > 0}, nil
}

func (p *Runtime) finishRequest(cursor *progressCursor) {
	if cursor.terminal {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cursor.cancel = true
	result := progressResult{expected: cursor}
	if err := p.loaded.process.call(ctx, command{Method: "cancel", RequestID: cursor.requestID}, "progress", inference.Foreground, true, &result); err != nil {
		p.loaded.process.close()
	}
}

func (p *Provider) generate(ctx context.Context, req modelcall.CompletionRequest, model string, cursor *progressCursor, out chan modelcall.StreamChunk) {
	decoder := newOutputDecoder(req)
	send := func(chunk modelcall.StreamChunk) bool {
		chunk.ProviderID, chunk.Model = p.id, model
		return sendGenerationChunk(ctx, out, chunk)
	}
	for {
		result := progressResult{expected: cursor}
		err := p.runtime.loaded.process.call(ctx, command{Method: "advance", RequestID: cursor.requestID}, "progress", inference.Foreground, true, &result)
		usage := modelcall.TokenUsage{Present: true, PromptTokens: cursor.processed, CompletionTokens: cursor.generated, CacheReadInputTokens: cursor.reused}
		if err != nil {
			send(modelcall.StreamChunk{Err: err, Done: true, Usage: usage})
			return
		}
		text, err := decoder.step(result.Progress.Token, result.Text)
		if err != nil {
			send(modelcall.StreamChunk{Err: err, Done: true, Usage: usage})
			return
		}
		chunk := modelcall.StreamChunk{Content: text, Usage: usage, Done: cursor.terminal}
		if cursor.terminal {
			capped := result.Progress.StopReason != nil && *result.Progress.StopReason == "max_tokens"
			chunk.ToolCalls, chunk.Err = decoder.finish(capped)
			if chunk.Err == nil && capped {
				chunk.Err = modelcall.OutputTruncation(req, p.id, model, "length", usage, len(chunk.ToolCalls) > 0)
			}
		}
		if !send(chunk) || chunk.Done {
			return
		}
	}
}

func sendGenerationChunk(ctx context.Context, out chan modelcall.StreamChunk, chunk modelcall.StreamChunk) bool {
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		for {
			select {
			case <-out:
			default:
				out <- modelcall.StreamChunk{ProviderID: chunk.ProviderID, Model: chunk.Model, Err: ctx.Err(), Done: true, Usage: chunk.Usage}
				return false
			}
		}
	}
}
