package security

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// scriptStep describes one scripted assistant turn.
type scriptStep struct {
	text  string
	calls []api.ToolCall
	err   error
}

func textStep(text string) scriptStep { return scriptStep{text: text} }

// errStep injects a failure at its scripted round.
func errStep(err error) scriptStep { return scriptStep{err: err} }

func toolStep(text string, calls ...api.ToolCall) scriptStep {
	return scriptStep{text: text, calls: calls}
}

// loadSchemasStep loads deferred schemas the way a model must before calling them.
func loadSchemasStep(id string, names ...string) scriptStep {
	// Naming the schemas outright loads them without a decision engine.
	return toolStep("", call(id, "request_tools", map[string]any{"need": strings.Join(names, " ")}))
}

func call(id, name string, args map[string]any) api.ToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return api.ToolCall{ID: id, Name: name, Args: args}
}

// scriptedLLM is a turn-aware deterministic LLMClient.
type scriptedLLM struct {
	mu    sync.Mutex
	steps map[string][]scriptStep
}

func newScriptedLLM() *scriptedLLM {
	return &scriptedLLM{steps: map[string][]scriptStep{}}
}

func (s *scriptedLLM) on(marker string, steps ...scriptStep) *scriptedLLM {
	s.steps[marker] = steps
	return s
}

var scriptMarkerRE = regexp.MustCompile(`\[\[scn:([a-zA-Z0-9_]+)\]\]`)

// Complete returns the scripted step for the current scenario and round.
func (s *scriptedLLM) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	marker := scriptedMarker(req.Messages)
	round := scriptedRound(req.Messages)

	step := s.pick(marker, round)
	if step == nil {
		return &modelcall.Completion{Content: "Done.", Usage: scriptUsage()}, nil
	}
	if step.err != nil {
		return nil, step.err
	}
	calls := append([]api.ToolCall(nil), step.calls...)
	text := step.text
	if text == "" && len(calls) == 0 {
		text = "Done."
	}
	return &modelcall.Completion{Content: text, ToolCalls: calls, Usage: scriptUsage()}, nil
}

// Stream tokenizes scripted completions for streaming tests.
func (s *scriptedLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := s.Complete(ctx, req)
		if err != nil {
			// Surface failures on the terminal chunk.
			ch <- modelcall.StreamChunk{Done: true, Err: err}
			return
		}
		if len(completion.ToolCalls) > 0 {
			if strings.TrimSpace(completion.Content) != "" {
				ch <- modelcall.StreamChunk{Content: completion.Content}
			}
			ch <- modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Usage: completion.Usage, Done: true}
			return
		}
		tokens := strings.Fields(completion.Content)
		if len(tokens) == 0 {
			ch <- modelcall.StreamChunk{Usage: completion.Usage, Done: true}
			return
		}
		for i, token := range tokens {
			suffix := " "
			if i == len(tokens)-1 {
				suffix = ""
			}
			chunk := modelcall.StreamChunk{Content: token + suffix, Done: i == len(tokens)-1}
			if chunk.Done {
				chunk.Usage = completion.Usage
			}
			ch <- chunk
		}
	}()
	return ch, nil
}

func (s *scriptedLLM) pick(marker string, round int) *scriptStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	steps, ok := s.steps[marker]
	if !ok {
		return nil
	}
	if round < 0 || round >= len(steps) {
		return nil
	}
	return &steps[round]
}

// scriptUsage reports a fixed non-zero token count so cost tracking has
// something to accumulate across a scenario.
func scriptUsage() modelcall.TokenUsage {
	return modelcall.TokenUsage{PromptTokens: 120, CompletionTokens: 40}
}

// scriptedMarker returns the scenario marker from the most recent non-internal
// user message that carries one.
func scriptedMarker(messages []api.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != api.MessageRoleUser || msg.Visibility == api.MessageVisibilityInternal {
			continue
		}
		if m := scriptMarkerRE.FindStringSubmatch(msg.Content); m != nil {
			return m[1]
		}
	}
	return ""
}

// Each assistant turn selects one script step, regardless of its tool-call count.
func scriptedRound(messages []api.Message) int {
	lastUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == api.MessageRoleUser && msg.Visibility != api.MessageVisibilityInternal {
			lastUser = i
			break
		}
	}
	round := 0
	for i := lastUser + 1; i < len(messages); i++ {
		if messages[i].Role == api.MessageRoleAssistant {
			round++
		}
	}
	return round
}
