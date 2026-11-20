package wiring

import (
	"context"
	"sync"

	wire "github.com/lycaon/lycaon/pkg/api"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// CitableStep is one coordinator response. Its text may read the request it answers.
type CitableStep struct {
	Text    func(req modelcall.CompletionRequest) string
	Command string
	// Tool calls a tool other than command with Args.
	Tool string
	Args map[string]any
}

// CitableScriptLLM answers each turn that offers command with its next step.
type CitableScriptLLM struct {
	mu    sync.Mutex
	Steps []CitableStep
	next  int
}

func (m *CitableScriptLLM) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	offersCommand := false
	for _, tool := range req.Tools {
		offersCommand = offersCommand || tool.Name == "command"
	}
	if !offersCommand {
		return &modelcall.Completion{Content: "Chat."}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.next >= len(m.Steps) {
		return &modelcall.Completion{Content: "Finished."}, nil
	}
	step := m.Steps[m.next]
	m.next++
	out := &modelcall.Completion{}
	if step.Text != nil {
		out.Content = step.Text(req)
	}
	if step.Tool != "" {
		out.ToolCalls = []wire.ToolCall{{ID: "call-" + string(rune('a'+m.next)), Name: step.Tool, Args: step.Args}}
	}
	if step.Command != "" {
		out.ToolCalls = []wire.ToolCall{{ID: "call-" + string(rune('a'+m.next)), Name: "command", Args: map[string]any{"command": step.Command}}}
	}
	return out, nil
}

func (m *CitableScriptLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	out, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: out.Content, ToolCalls: out.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}
