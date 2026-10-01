package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ManualProvider supplies completions over /harness/llm control endpoints.
type ManualProvider struct {
	mu          sync.Mutex
	queue       []*ManualPending
	waiters     []chan *ManualPending
	autoEnabled bool
	autoText    string
	timeout     time.Duration
	seq         atomic.Uint64
}

// ManualPending is one in-flight completion awaiting a supplied response.
type ManualPending struct {
	ID string
	// SessionID names the session whose turn is waiting.
	SessionID string
	Model     string
	Messages  []api.Message
	Tools     []tools.ToolMeta
	respond   chan manualResponse
}

type manualResponse struct {
	content   string
	toolCalls []api.ToolCall
	// chunks, when non-empty, are emitted by Stream instead of tokenizing content.
	chunks []modelcall.StreamChunk
}

const defaultManualTimeout = 120 * time.Second

// NewManualProvider returns a manual provider with auto-reply enabled.
func NewManualProvider() *ManualProvider {
	return &ManualProvider{
		autoEnabled: true,
		autoText:    "I understand. How can I help you further?",
		timeout:     defaultManualTimeout,
	}
}

// Complete blocks until a response is supplied (manual mode) or returns the
// auto-reply immediately (auto mode); falls back to the auto-reply on timeout.
func (p *ManualProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.autoEnabled {
		text := p.autoText
		p.mu.Unlock()
		return &modelcall.Completion{Content: text}, nil
	}
	pend := p.enqueueLocked(req)
	timeout := p.timeout
	p.mu.Unlock()

	select {
	case resp := <-pend.respond:
		content, tools := collapseManualResponse(resp)
		return &modelcall.Completion{Content: content, ToolCalls: tools}, nil
	case <-time.After(timeout):
		p.dropFromQueue(pend.ID)
		return &modelcall.Completion{Content: "(harness manual mode: no response within timeout)"}, nil
	case <-ctx.Done():
		p.dropFromQueue(pend.ID)
		return nil, ctx.Err()
	}
}

// enqueueLocked registers one waiting completion; the caller holds p.mu.
func (p *ManualProvider) enqueueLocked(req modelcall.CompletionRequest) *ManualPending {
	pend := &ManualPending{
		ID:        fmt.Sprintf("manual-%d", p.seq.Add(1)),
		SessionID: req.Debug.SessionID,
		Model:     req.Model,
		Messages:  req.Messages,
		Tools:     req.Tools,
		respond:   make(chan manualResponse, 1),
	}
	p.queue = append(p.queue, pend)
	p.notifyWaitersLocked(pend)
	return pend
}

func collapseManualResponse(resp manualResponse) (string, []api.ToolCall) {
	if len(resp.chunks) == 0 {
		return resp.content, resp.toolCalls
	}
	var b strings.Builder
	var tools []api.ToolCall
	for _, c := range resp.chunks {
		b.WriteString(c.Content)
		if len(c.ToolCalls) > 0 {
			tools = append(tools, c.ToolCalls...)
		}
	}
	if len(tools) == 0 {
		tools = resp.toolCalls
	}
	content := b.String()
	if content == "" {
		content = resp.content
	}
	return content, tools
}

// Stream emits chunks derived from Complete, or chunks supplied via RespondWithChunks.
func (p *ManualProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.autoEnabled {
		text := p.autoText
		p.mu.Unlock()
		return streamFromCompletion(ctx, &modelcall.Completion{Content: text}), nil
	}
	pend := p.enqueueLocked(req)
	timeout := p.timeout
	p.mu.Unlock()

	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		var resp manualResponse
		select {
		case resp = <-pend.respond:
		case <-time.After(timeout):
			p.dropFromQueue(pend.ID)
			_ = modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: "(harness manual mode: no response within timeout)", Done: true})
			return
		case <-ctx.Done():
			p.dropFromQueue(pend.ID)
			_ = modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: ctx.Err(), Done: true})
			return
		}
		if len(resp.chunks) > 0 {
			for i, c := range resp.chunks {
				chunk := c
				if i == len(resp.chunks)-1 {
					chunk.Done = true
				}
				if !modelcall.SendChunk(ctx, ch, chunk) {
					return
				}
			}
			return
		}
		completion := &modelcall.Completion{Content: resp.content, ToolCalls: resp.toolCalls}
		emitCompletionChunks(ctx, ch, completion)
	}()
	return ch, nil
}

func streamFromCompletion(ctx context.Context, completion *modelcall.Completion) <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		emitCompletionChunks(ctx, ch, completion)
	}()
	return ch
}

func emitCompletionChunks(ctx context.Context, ch chan<- modelcall.StreamChunk, completion *modelcall.Completion) {
	if len(completion.ToolCalls) > 0 {
		if strings.TrimSpace(completion.Content) != "" {
			if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: completion.Content}) {
				return
			}
		}
		_ = modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Done: true})
		return
	}
	// Plain content streams line by line.
	if strings.TrimSpace(completion.Content) == "" {
		_ = modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Done: true})
		return
	}
	lines := strings.SplitAfter(completion.Content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: line, Done: i == len(lines)-1}) {
			return
		}
	}
}

// Pending returns the oldest waiting request, blocking up to wait for one.
func (p *ManualProvider) Pending(ctx context.Context, wait time.Duration) (*ManualPending, bool) {
	p.mu.Lock()
	if len(p.queue) > 0 {
		pend := p.queue[0]
		p.mu.Unlock()
		return pend, true
	}
	if wait <= 0 {
		p.mu.Unlock()
		return nil, false
	}
	notify := make(chan *ManualPending, 1)
	p.waiters = append(p.waiters, notify)
	p.mu.Unlock()

	select {
	case pend := <-notify:
		return pend, true
	case <-time.After(wait):
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

// RespondWithChunks fulfills a pending request, optionally with explicit stream chunks.
func (p *ManualProvider) RespondWithChunks(id, content string, toolCalls []api.ToolCall, chunks []modelcall.StreamChunk) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, pend := range p.queue {
		if pend.ID != id {
			continue
		}
		p.queue = append(p.queue[:i], p.queue[i+1:]...)
		pend.respond <- manualResponse{content: content, toolCalls: toolCalls, chunks: chunks}
		return nil
	}
	return fmt.Errorf("no pending completion %q", id)
}

// SetAuto toggles auto-reply and optionally updates the auto text.
func (p *ManualProvider) SetAuto(enabled bool, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.autoEnabled = enabled
	if text != "" {
		p.autoText = text
	}
}

func (p *ManualProvider) notifyWaitersLocked(pend *ManualPending) {
	for _, w := range p.waiters {
		select {
		case w <- pend:
		default:
		}
	}
	p.waiters = nil
}

func (p *ManualProvider) dropFromQueue(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, pend := range p.queue {
		if pend.ID == id {
			p.queue = append(p.queue[:i], p.queue[i+1:]...)
			return
		}
	}
}
