package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Provider wire shapes preserve replay tokens in ToolCall.WireID.

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/png"
	Data      string `json:"data"`       // raw base64, no data: prefix
}

type ContentBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image (user attachments and tool results on vision models)
	Source *ImageSource `json:"source,omitempty"`
	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	// tool_result
	ToolUseID  string `json:"tool_use_id,omitempty"`
	ResultText string `json:"content,omitempty"`
	// ResultBlocks replaces ResultText when the result carries an image.
	ResultBlocks []ContentBlock `json:"-"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
	// cache_control (explicit_breakpoints projection)
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON writes input on every tool_use block, empty or not: the API
// rejects a tool_use without it, and omitempty drops an empty map.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	type plain ContentBlock
	if b.Type == "tool_result" && len(b.ResultBlocks) > 0 {
		b.ResultText = ""
		return marshalUnescaped(struct {
			plain
			Content []ContentBlock `json:"content"`
		}{plain(b), b.ResultBlocks})
	}
	if b.Type != "tool_use" {
		return marshalUnescaped(plain(b))
	}
	input := b.Input
	if input == nil {
		input = map[string]any{}
	}
	return marshalUnescaped(struct {
		plain
		Input map[string]any `json:"input"`
	}{plain(b), input})
}

// marshalUnescaped matches the request encoder, which leaves HTML characters as written.
func marshalUnescaped(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

type anthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type Request struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       []ContentBlock         `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice   `json:"tool_choice,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
	// CacheControl enables top-level automatic caching.
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicToolChoice selects how the model may use the request's tools.
type anthropicToolChoice struct {
	Type string `json:"type"`
}

type Usage struct {
	CacheCreation struct {
		Ephemeral1HInputTokens int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     int  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens"`
}

type Response struct {
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      *Usage         `json:"usage"`
}

func NormalizeUsage(u *Usage) modelcall.TokenUsage {
	if u == nil {
		return modelcall.TokenUsage{}
	}
	out := modelcall.TokenUsage{
		Present:                    u.InputTokens != nil || u.OutputTokens != nil,
		Incomplete:                 u.InputTokens == nil || u.OutputTokens == nil,
		PromptTokens:               u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CacheReadInputTokens:       u.CacheReadInputTokens,
		CacheCreationInputTokens:   u.CacheCreationInputTokens,
		CacheCreation1HInputTokens: u.CacheCreation.Ephemeral1HInputTokens,
	}
	if u.InputTokens != nil {
		out.PromptTokens += *u.InputTokens
	}
	if u.OutputTokens != nil {
		out.CompletionTokens = *u.OutputTokens
	}
	return out
}

// Each cache breakpoint lands on the last block its marked message emits,
// whatever the role; tool results pair with preceding tool-use ids.
// Vision-enabled requests convert user artifacts into image blocks.
func ProjectMessages(msgs []api.Message, breakpoints []providerwire.PromptCacheBreakpoint, vision bool, sessionID, providerID, model string) (system []ContentBlock, out []anthropicMessage) {
	pendingToolUseIDs := make(providerwire.ToolCallPairs)
	marked := providerwire.PromptCacheBreakpointSet(breakpoints)
	preambleEnd := providerwire.SystemPreambleEnd(msgs)

	add := func(role string, block ContentBlock) {
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, block)
			return
		}
		out = append(out, anthropicMessage{Role: role, Content: []ContentBlock{block}})
	}

	for i, m := range msgs {
		if bp, ok := marked[i-1]; i > 0 && ok {
			markPromptCacheBreakpoint(system, out, bp)
		}
		switch m.Role {
		case api.MessageRoleSystem:
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			block := ContentBlock{Type: "text", Text: m.Content}
			// Only the preamble becomes standing system content.
			if i >= preambleEnd {
				add("user", block)
				continue
			}
			system = append(system, block)
		case api.MessageRoleAssistant:
			if reasoning := providerwire.ReasoningForModel(m, providerID, model); reasoning != nil {
				for _, raw := range reasoning.Details {
					var block ContentBlock
					if json.Unmarshal(raw, &block) != nil || block.Type != "thinking" && block.Type != "redacted_thinking" {
						continue
					}
					add("assistant", block)
				}
			}
			if m.Content != "" {
				add("assistant", ContentBlock{Type: "text", Text: m.Content})
			}
			if len(m.ToolCalls) > 0 {
				clear(pendingToolUseIDs)
				for _, tc := range m.ToolCalls {
					id := pendingToolUseIDs.Add(tc, true)
					add("assistant", ContentBlock{
						Type:  "tool_use",
						ID:    id,
						Name:  tc.Name,
						Input: providerwire.ToolArgs(tc.Args),
					})
				}
			}
		case api.MessageRoleTool:
			content := m.Content
			if content == "" && m.ToolResult != nil {
				content = m.ToolResult.Content
			}
			toolUseID, paired := pendingToolUseIDs.Take(m.ToolResult)
			if !paired {
				continue
			}
			result := ContentBlock{Type: "tool_result", ToolUseID: toolUseID, ResultText: content}
			if image, ok := providerwire.ToolResultImage(m); ok {
				result.ResultBlocks = []ContentBlock{
					{Type: "text", Text: content},
					{Type: "image", Source: &ImageSource{Type: "base64", MediaType: image.Mime, Data: image.Base64}},
				}
			}
			add("user", result)
		default:
			for _, block := range anthropicUserContentBlocks(m.Content, m.ArtifactIDs, vision, sessionID) {
				add("user", block)
			}
		}
	}
	if bp, ok := marked[len(msgs)-1]; len(msgs) > 0 && ok {
		markPromptCacheBreakpoint(system, out, bp)
	}
	return system, out
}

// markPromptCacheBreakpoint sets cache_control, with the boundary's
// lifetime, on the last cacheable block emitted so far. Thinking blocks
// cannot carry the marker, so it walks back past them; with no message
// blocks yet it lands on the standing system.
func markPromptCacheBreakpoint(system []ContentBlock, out []anthropicMessage, bp providerwire.PromptCacheBreakpoint) {
	control := &anthropicCacheControl{Type: "ephemeral", TTL: providerwire.LifetimeToken(bp.Lifetime)}
	for m := len(out) - 1; m >= 0; m-- {
		if markLastCacheableBlock(out[m].Content, control) {
			return
		}
	}
	markLastCacheableBlock(system, control)
}

func markLastCacheableBlock(blocks []ContentBlock, control *anthropicCacheControl) bool {
	for j := len(blocks) - 1; j >= 0; j-- {
		if blocks[j].Type == "thinking" || blocks[j].Type == "redacted_thinking" {
			continue
		}
		blocks[j].CacheControl = control
		return true
	}
	return false
}

func anthropicUserContentBlocks(text string, artifactIDs []string, vision bool, sessionID string) []ContentBlock {
	text = strings.TrimSpace(text)
	if !vision || len(artifactIDs) == 0 || !providerwire.HasVisualResolver() || strings.TrimSpace(sessionID) == "" {
		return []ContentBlock{{Type: "text", Text: text}}
	}
	out := make([]ContentBlock, 0, 1+len(artifactIDs))
	if text != "" {
		out = append(out, ContentBlock{Type: "text", Text: text})
	}
	for _, id := range artifactIDs {
		mime, data, ok := providerwire.UserArtifactWireImage(sessionID, id)
		if !ok {
			continue
		}
		out = append(out, ContentBlock{
			Type: "image",
			Source: &ImageSource{
				Type:      "base64",
				MediaType: mime,
				Data:      data,
			},
		})
	}
	if len(out) == 0 {
		if text == "" {
			return nil
		}
		return []ContentBlock{{Type: "text", Text: text}}
	}
	return out
}

func ProjectTools(metas []tools.ToolMeta) []anthropicTool {
	out := make([]anthropicTool, 0, len(metas))
	for _, raw := range metas {
		t := providerwire.ProjectToolMetaForModel(raw)
		schema := t.ArgsSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

func mapAnthropicResponse(resp *Response) (*modelcall.Completion, error) {
	if resp == nil {
		return nil, nil
	}
	var content, reasoning strings.Builder
	var toolCalls []api.ToolCall
	var reasoningDetails []json.RawMessage
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
			raw, err := json.Marshal(block)
			if err != nil {
				return nil, fmt.Errorf("encode thinking block: %w", err)
			}
			reasoningDetails = append(reasoningDetails, raw)
		case "redacted_thinking":
			raw, err := json.Marshal(block)
			if err != nil {
				return nil, fmt.Errorf("encode redacted thinking block: %w", err)
			}
			reasoningDetails = append(reasoningDetails, raw)
		case "tool_use":
			toolCalls = append(toolCalls, api.ToolCall{
				ID:     providerwire.NewToolCallID(),
				WireID: block.ID,
				Name:   block.Name,
				Args:   block.Input,
			})
		}
	}
	return &modelcall.Completion{
		Content:          content.String(),
		Reasoning:        reasoning.String(),
		ReasoningDetails: reasoningDetails,
		ToolCalls:        toolCalls,
		Usage:            NormalizeUsage(resp.Usage),
	}, nil
}

func anthropicReasoningDetails(blocks map[int]*ContentBlock) []json.RawMessage {
	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	out := make([]json.RawMessage, 0, len(indices))
	for _, index := range indices {
		raw, err := json.Marshal(blocks[index])
		if err == nil {
			out = append(out, raw)
		}
	}
	return out
}
