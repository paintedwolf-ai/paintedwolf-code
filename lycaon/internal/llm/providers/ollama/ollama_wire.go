package ollama

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Request struct {
	Model     string              `json:"model"`
	Messages  []ollamaChatMessage `json:"messages"`
	Tools     []ollamaTool        `json:"tools,omitempty"`
	Stream    bool                `json:"stream"`
	KeepAlive string              `json:"keep_alive,omitempty"`
	Options   ollamaOptions       `json:"options"`
	// Format accepts the string "json" or a JSON Schema as a decoding constraint.
	Format json.RawMessage `json:"format,omitempty"`
	// Think accepts a boolean or level string; nil preserves the model default.
	Think any `json:"think,omitempty"`
}

type ollamaOptions struct {
	NumCtx      int      `json:"num_ctx"`
	NumPredict  int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type ollamaChatMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolName  string           `json:"tool_name,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	Images    []string         `json:"images,omitempty"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolCallFunction `json:"function"`
}

type ollamaToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ollamaChatResponse struct {
	Message            ollamaChatMessage `json:"message"`
	Done               bool              `json:"done"`
	DoneReason         string            `json:"done_reason"`
	PromptEvalCount    int               `json:"prompt_eval_count"`
	PromptEvalDuration int64             `json:"prompt_eval_duration"`
	EvalCount          int               `json:"eval_count"`
	Error              string            `json:"error"`
}

func ProjectMessages(msgs []api.Message, vision bool, sessionID string) []ollamaChatMessage {
	msgs = providerwire.SystemPreamble(msgs)
	out := make([]ollamaChatMessage, 0, len(msgs))
	// Model templates render images on user turns, so a run of tool results
	// is followed by one user message carrying their images.
	var toolImages ollamaChatMessage
	flushToolImages := func() {
		if len(toolImages.Images) > 0 {
			toolImages.Role = "user"
			out = append(out, toolImages)
		}
		toolImages = ollamaChatMessage{}
	}
	for _, m := range msgs {
		// Replaying a note's UI echo as assistant text invites a continuation.
		if api.IsAgentNoteMessage(m) {
			continue
		}
		if m.Role != api.MessageRoleTool {
			flushToolImages()
		}
		switch m.Role {
		case api.MessageRoleAssistant:
			wMsg := ollamaChatMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				wMsg.ToolCalls = append(wMsg.ToolCalls, ollamaToolCall{
					Function: ollamaToolCallFunction{Name: tc.Name, Arguments: providerwire.ToolArgs(tc.Args)},
				})
			}
			out = append(out, wMsg)
		case api.MessageRoleTool:
			content := m.Content
			toolName := ""
			if m.ToolResult != nil {
				toolName = m.ToolResult.Tool
			}
			if content == "" && m.ToolResult != nil {
				content = m.ToolResult.Content
			}
			out = append(out, ollamaChatMessage{Role: "tool", Content: content, ToolName: toolName})
			if image, ok := providerwire.ToolResultImage(m); ok {
				toolImages.Content = strings.TrimSpace(toolImages.Content + "\n\n" + providerwire.ToolImageCaption(m, ""))
				toolImages.Images = append(toolImages.Images, image.Base64)
			}
		case api.MessageRoleSystem:
			out = append(out, ollamaChatMessage{Role: "system", Content: m.Content})
		default:
			wire := ollamaChatMessage{Role: "user", Content: m.Content}
			if vision {
				for _, artifactID := range m.ArtifactIDs {
					_, encoded, ok := providerwire.UserArtifactWireImage(sessionID, artifactID)
					if ok {
						wire.Images = append(wire.Images, encoded)
					}
				}
			}
			out = append(out, wire)
		}
	}
	flushToolImages()
	return out
}

func toolsToOllama(toolMetas []tools.ToolMeta) []ollamaTool {
	out := make([]ollamaTool, 0, len(toolMetas))
	for _, t := range toolMetas {
		params := any(t.ArgsSchema)
		if t.ArgsSchema == nil {
			params = map[string]any{"type": "object"}
		}
		out = append(out, ollamaTool{
			Type:     "function",
			Function: ollamaToolFunction{Name: t.Name, Description: t.Description, Parameters: params},
		})
	}
	return out
}

func ollamaToolCallsToAPI(calls []ollamaToolCall) []api.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	// The wire pairs calls positionally; host-minted IDs stay off the wire.
	out := make([]api.ToolCall, 0, len(calls))
	for _, tc := range calls {
		out = append(out, api.ToolCall{
			ID:   providerwire.NewToolCallID(),
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}
	return out
}

// Tool calls accumulate across events and join usage on the terminal chunk.
func decodeOllamaStream(ctx context.Context, body io.Reader, providerID, model string, contextTokens int, onDone func(promptTokens int, promptEvalDuration int64)) <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var rawToolCalls []ollamaToolCall
		var usage modelcall.TokenUsage
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: ctx.Err(), Done: true})
				return
			default:
			}
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var resp ollamaChatResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("ollama: malformed stream payload: %w", err), Done: true})
				return
			}
			if resp.Error != "" {
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("ollama: %s", resp.Error), Done: true})
				return
			}
			if resp.Message.Content != "" {
				if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: resp.Message.Content}) {
					return
				}
			}
			if resp.Message.Thinking != "" {
				// Reasoning events keep the stall guard alive without becoming answer text.
				if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Reasoning: resp.Message.Thinking}) {
					return
				}
			}
			if len(resp.Message.ToolCalls) > 0 {
				rawToolCalls = append(rawToolCalls, resp.Message.ToolCalls...)
			}
			if resp.Done {
				usage = modelcall.TokenUsage{PromptTokens: resp.PromptEvalCount, CompletionTokens: resp.EvalCount}
				if onDone != nil {
					onDone(resp.PromptEvalCount, resp.PromptEvalDuration)
				}
				terminal := modelcall.StreamChunk{ToolCalls: ollamaToolCallsToAPI(rawToolCalls), Usage: usage, Done: true}
				if resp.DoneReason == "length" {
					terminal.Err = &failure.ProviderOutputTruncatedError{
						ProviderID: providerID, Model: model, FinishReason: resp.DoneReason,
						PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, ContextTokens: contextTokens,
					}
				}
				modelcall.SendChunk(ctx, ch, terminal)
				return
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			// Partial output does not make a failed stream complete.
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: err, Done: true})
			return
		}
		modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("ollama: stream ended before done"), Done: true})
	}()
	return ch
}
