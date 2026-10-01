package modelcall

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// CollectStream assembles a stream and returns partial usage on failure.
func CollectStream(ch <-chan StreamChunk) (*Completion, []string, error) {
	var content strings.Builder
	var reasoning strings.Builder
	var tokens []string
	var toolCalls []api.ToolCall
	var reasoningDetails []json.RawMessage
	var usage TokenUsage
	var providerID, model string
	var fallback, scripted bool
	for chunk := range ch {
		if chunk.ResetReasoning {
			reasoning.Reset()
			reasoningDetails = nil
		}
		if chunk.ProviderID != "" {
			providerID = chunk.ProviderID
			model = chunk.Model
		}
		fallback = fallback || chunk.Fallback
		scripted = scripted || chunk.Scripted
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			tokens = append(tokens, chunk.Content)
		}
		if chunk.Reasoning != "" {
			reasoning.WriteString(chunk.Reasoning)
		}
		if len(chunk.ReasoningDetails) > 0 {
			reasoningDetails = chunk.ReasoningDetails
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = chunk.ToolCalls
		}
		if chunk.Usage.Reported() {
			usage = chunk.Usage
		}
		if chunk.Err != nil {
			return &Completion{
				Content:          content.String(),
				Reasoning:        reasoning.String(),
				ReasoningDetails: reasoningDetails,
				ToolCalls:        toolCalls,
				Usage:            usage,
				ProviderID:       providerID,
				Model:            model,
				Fallback:         fallback,
				Scripted:         scripted,
			}, tokens, chunk.Err
		}
		if chunk.Done {
			break
		}
	}
	return &Completion{
		Content:          content.String(),
		Reasoning:        reasoning.String(),
		ReasoningDetails: reasoningDetails,
		ToolCalls:        toolCalls,
		Usage:            usage,
		ProviderID:       providerID,
		Model:            model,
		Fallback:         fallback,
		Scripted:         scripted,
	}, tokens, nil
}

// CollectStreamWithProgress publishes accumulated stream snapshots.
func CollectStreamWithProgress(ch <-chan StreamChunk, onProgress func(*Completion)) (*Completion, []string, error) {
	var content strings.Builder
	var reasoning strings.Builder
	var tokens []string
	var toolCalls []api.ToolCall
	var reasoningDetails []json.RawMessage
	var usage TokenUsage
	var providerID, model string
	var fallback, scripted bool
	snapshot := func() *Completion {
		return &Completion{
			Content:          content.String(),
			Reasoning:        reasoning.String(),
			ReasoningDetails: reasoningDetails,
			ToolCalls:        append([]api.ToolCall(nil), toolCalls...),
			Usage:            usage,
			ProviderID:       providerID,
			Model:            model,
			Fallback:         fallback,
			Scripted:         scripted,
		}
	}
	for chunk := range ch {
		if chunk.ResetReasoning {
			reasoning.Reset()
			reasoningDetails = nil
		}
		if chunk.ProviderID != "" {
			providerID = chunk.ProviderID
			model = chunk.Model
		}
		fallback = fallback || chunk.Fallback
		scripted = scripted || chunk.Scripted
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			tokens = append(tokens, chunk.Content)
		}
		if chunk.Reasoning != "" {
			reasoning.WriteString(chunk.Reasoning)
		}
		if len(chunk.ReasoningDetails) > 0 {
			reasoningDetails = chunk.ReasoningDetails
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = chunk.ToolCalls
		}
		if chunk.Usage.Reported() {
			usage = chunk.Usage
		}
		if chunk.Err != nil {
			return snapshot(), tokens, chunk.Err
		}
		if onProgress != nil && (chunk.Content != "" || chunk.Reasoning != "" || len(chunk.ToolCalls) > 0) {
			onProgress(snapshot())
		}
		if chunk.Done {
			break
		}
	}
	out := snapshot()
	if onProgress != nil {
		onProgress(out)
	}
	return out, tokens, nil
}

// SendChunk delivers chunk to ch, returning false if ctx is canceled.
func SendChunk(ctx context.Context, ch chan<- StreamChunk, chunk StreamChunk) bool {
	select {
	case ch <- chunk:
		return true
	default:
	}
	select {
	case ch <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

// ErrStreamStall indicates no chunk arrived within the stall timeout.
var ErrStreamStall = errors.New("llm stream stalled: no activity within the stall timeout")

// GuardStreamStall forwards chunks from ch and invokes onStall if no chunk arrives within stall.
func GuardStreamStall(ctx context.Context, ch <-chan StreamChunk, stall time.Duration, onStall func()) <-chan StreamChunk {
	if ch == nil || stall <= 0 {
		return ch
	}
	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		timer := time.NewTimer(stall)
		defer timer.Stop()
		for {
			select {
			case chunk, ok := <-ch:
				if !ok {
					return
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(stall)
				select {
				case out <- chunk:
				case <-ctx.Done():
					DrainStream(ch)
					return
				}
			case <-timer.C:
				if onStall != nil {
					onStall()
				}
				DrainStream(ch)
				return
			}
		}
	}()
	return out
}

// DrainStream reads ch to close so the provider goroutine behind it — already
// unblocked by the canceled stream context — can run its deferred cleanup.
func DrainStream(ch <-chan StreamChunk) {
	for range ch {
	}
}
