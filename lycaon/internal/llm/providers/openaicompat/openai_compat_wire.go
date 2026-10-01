package openaicompat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
	openai "github.com/sashabaranov/go-openai"
)

type toolCallWire struct {
	Index        *int             `json:"index,omitempty"`
	ID           string           `json:"id,omitempty"`
	Type         string           `json:"type,omitempty"`
	Function     functionCallWire `json:"function"`
	ExtraContent map[string]any   `json:"extra_content,omitempty"`
}

type functionCallWire struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	// PromptCacheBreakpoint ends a reusable prefix on this part for models
	// that take OpenAI's explicit breakpoint.
	PromptCacheBreakpoint *promptCacheBreakpointWire `json:"prompt_cache_breakpoint,omitempty"`
	// CacheControl ends a reusable prefix on this part for routes that take
	// cache_control.
	CacheControl *cacheControlWire `json:"cache_control,omitempty"`
}

// promptCacheBreakpointWire is the explicit breakpoint; the API accepts no other mode.
type promptCacheBreakpointWire struct {
	Mode string `json:"mode"`
}

const promptCacheBreakpointExplicit = "explicit"

// cacheControlWire is the ephemeral cache marker OpenRouter forwards to
// Anthropic-style caches, on a part or at the top level of the request.
type cacheControlWire struct {
	Type string `json:"type"`
}

const cacheControlEphemeral = "ephemeral"

type Message struct {
	Role      string         `json:"role"`
	Content   any            `json:"content,omitempty"`
	ToolCalls []toolCallWire `json:"tool_calls,omitempty"`
	// Reasoning fields preserve signed traces byte-for-byte.
	Reasoning        string            `json:"reasoning,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
}

// providerRoutingWire constrains endpoint selection.
type providerRoutingWire struct {
	RequireParameters bool `json:"require_parameters,omitempty"`
}

type Request struct {
	Model                string                   `json:"model"`
	Messages             []Message                `json:"messages"`
	Stream               bool                     `json:"stream,omitempty"`
	MaxTokens            int                      `json:"max_tokens,omitempty"`
	MaxCompletionTokens  int                      `json:"max_completion_tokens,omitempty"`
	Temperature          float32                  `json:"temperature,omitempty"`
	ReasoningEffort      string                   `json:"reasoning_effort,omitempty"`
	Thinking             *thinkingTypeWire        `json:"thinking,omitempty"`
	Reasoning            *modelcall.ReasoningWire `json:"reasoning,omitempty"`
	Provider             *providerRoutingWire     `json:"provider,omitempty"`
	Tools                []openai.Tool            `json:"tools,omitempty"`
	StreamOptions        *openai.StreamOptions    `json:"stream_options,omitempty"`
	PromptCacheKey       string                   `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string                   `json:"prompt_cache_retention,omitempty"`
	// CacheControl asks OpenRouter to place Anthropic's automatic breakpoint.
	CacheControl   *cacheControlWire             `json:"cache_control,omitempty"`
	ResponseFormat *modelcall.ResponseFormatWire `json:"response_format,omitempty"`
}

type chatCompletionResponseWire struct {
	Choices []struct {
		Message struct {
			Role             string            `json:"role"`
			Content          string            `json:"content"`
			Reasoning        string            `json:"reasoning"`
			ReasoningContent string            `json:"reasoning_content"`
			ReasoningDetails []json.RawMessage `json:"reasoning_details"`
			ToolCalls        []toolCallWire    `json:"tool_calls"`
		} `json:"message"`
		FinishReason openai.FinishReason `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

type chatCompletionStreamChunkWire struct {
	Choices []struct {
		Delta struct {
			Content          string                 `json:"content"`
			Reasoning        string                 `json:"reasoning"`
			ReasoningContent string                 `json:"reasoning_content"`
			ReasoningDetails []reasoningDetailDelta `json:"reasoning_details"`
			ToolCalls        []toolCallWire         `json:"tool_calls"`
		} `json:"delta"`
		FinishReason openai.FinishReason `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

func encodeChatCompletionRequest(req modelcall.CompletionRequest, p *Provider, stream bool, opts controlOpts) ([]byte, error) {
	model := p.ResolveModel(req)
	entry, _ := p.ModelEntry(model)
	vision := modelinfo.Supported(entry.EffectiveCapabilities().Vision)
	if p.profile.SystemPreambleOnly {
		req.Messages = providerwire.SystemPreamble(req.Messages)
	}
	req.Messages = providerwire.PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)
	controls := p.resolveRequestControls(req, model, opts)
	// Cache gates use the resolved wire model id and its catalog facts.
	req.Model = model
	proj := providerwire.ProjectPromptCache(req, p.profile.PromptCache, entry.EffectiveCapabilities())
	wire := Request{
		Model: model,
		Messages: ProjectMessages(req.Messages, MessageProjection{
			RoundTripToolCallIDs:    p.profile.RoundTripsToolCallID(),
			ReasoningWire:           p.profile.ReasoningWire,
			GoogleThoughtSignatures: p.profile.GoogleThoughtSignatures,
			ProviderID:              p.id,
			Model:                   model,
			Vision:                  vision,
			SessionID:               req.Debug.SessionID,
			PromptCache:             proj,
			ToolResultImages:        p.profile.ToolResultImages,
		}),
		Stream:               stream,
		ReasoningEffort:      controls.ReasoningEffort,
		Thinking:             controls.Thinking,
		Reasoning:            controls.Reasoning,
		PromptCacheKey:       proj.PromptCacheKey,
		PromptCacheRetention: proj.PromptCacheRetention,
	}
	if proj.RequestMarker {
		wire.CacheControl = &cacheControlWire{Type: cacheControlEphemeral}
	}
	if p.profile.RequireProviderParameters {
		wire.Provider = &providerRoutingWire{RequireParameters: true}
	}
	if p.http.maxCompletionTokens {
		wire.MaxCompletionTokens = controls.MaxTokens
	} else {
		wire.MaxTokens = controls.MaxTokens
	}
	if controls.Temperature != nil {
		wire.Temperature = float32(*controls.Temperature)
	}
	if stream {
		wire.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}
	if len(req.Tools) > 0 {
		wire.Tools = ProjectTools(req.Tools)
	}
	wire.ResponseFormat = modelcall.ResponseFormatToWire(req.ResponseFormat)
	// Keep syntax characters literal in tool JSON.
	return surveyjson.Marshal(wire)
}

// MessageProjection carries request-specific projection controls.
type MessageProjection struct {
	// RoundTripToolCallIDs selects wire-issued call identities.
	RoundTripToolCallIDs bool
	// ReasoningWire selects the assistant-history fields accepted by the provider.
	ReasoningWire           providerprofile.ReasoningWireStyle
	GoogleThoughtSignatures bool
	// ProviderID and Model gate that echo to reasoning this exact pair produced.
	ProviderID  string
	Model       string
	Vision      bool
	SessionID   string
	PromptCache providerwire.PromptCacheProjection
	// ToolResultImages places tool-result pixels.
	ToolResultImages providerprofile.ToolResultImagePlacement
}

// ProjectMessages projects host messages onto the compatible chat wire. The
// prompt cache breakpoint lands on the last content part the marked message
// emits, whatever its role; a message with nothing to mark hands it back to
// the message before it.
func ProjectMessages(msgs []api.Message, proj MessageProjection) []Message {
	out := make([]Message, 0, len(msgs))
	pendingToolCallIDs := make(providerwire.ToolCallPairs)
	vision := proj.Vision
	sessionID := proj.SessionID
	marker := proj.PromptCache.Marker
	var marked map[int]providerwire.PromptCacheBreakpoint
	if marker != providerprofile.PromptCacheMarkerNone {
		marked = providerwire.PromptCacheBreakpointSet(providerwire.TrailingPromptCacheBreakpoints(
			proj.PromptCache.Breakpoints, providerwire.OpenAIExplicitBreakpointLimit))
	}

	// Images held for the user message that follows a run of tool results.
	var toolImages []ContentPart
	flushToolImages := func() {
		if len(toolImages) > 0 {
			out = append(out, Message{Role: string(openai.ChatMessageRoleUser), Content: toolImages})
			toolImages = nil
		}
	}
	for i, m := range msgs {
		if _, ok := marked[i-1]; i > 0 && ok {
			markPromptCacheBreakpoint(out, marker)
		}
		if m.Role != api.MessageRoleTool {
			flushToolImages()
		}
		switch m.Role {
		case api.MessageRoleAssistant:
			wMsg := Message{
				Role:    string(openai.ChatMessageRoleAssistant),
				Content: m.Content,
			}
			if reasoning := providerwire.ReasoningForModel(m, proj.ProviderID, proj.Model); reasoning != nil {
				switch proj.ReasoningWire {
				case providerprofile.ReasoningWireContent:
					wMsg.ReasoningContent = reasoning.Text
				case providerprofile.ReasoningWireDetails:
					wMsg.Reasoning = reasoning.Text
					wMsg.ReasoningDetails = reasoning.Details
				case providerprofile.ReasoningWireNone:
				}
			}
			if len(m.ToolCalls) > 0 {
				clear(pendingToolCallIDs)
				wMsg.ToolCalls = make([]toolCallWire, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					wireID := pendingToolCallIDs.Add(tc, proj.RoundTripToolCallIDs)
					wMsg.ToolCalls = append(wMsg.ToolCalls, toolCallWire{
						ID:   wireID,
						Type: string(openai.ToolTypeFunction),
						Function: functionCallWire{
							Name:      tc.Name,
							Arguments: providerwire.ToolCallArgumentsJSON(tc.Args),
						},
						ExtraContent: providerwire.CloneMetadata(tc.ExtraContent),
					})
				}
			}
			if proj.GoogleThoughtSignatures {
				projectGoogleToolHistory(wMsg.ToolCalls)
			}
			out = append(out, wMsg)
		case api.MessageRoleTool:
			content := m.Content
			if content == "" && m.ToolResult != nil {
				content = m.ToolResult.Content
			}
			wireID, paired := pendingToolCallIDs.Take(m.ToolResult)
			if !paired {
				continue
			}
			var toolContent any = content
			if image, ok := providerwire.ToolResultImage(m); ok {
				part := imageURLPart(image.Mime, image.Base64)
				if proj.ToolResultImages == providerprofile.ToolResultImagesInline {
					toolContent = []ContentPart{{Type: "text", Text: content}, part}
				} else {
					toolImages = append(toolImages, ContentPart{Type: "text", Text: providerwire.ToolImageCaption(m, wireID)}, part)
				}
			}
			out = append(out, Message{
				Role:       string(openai.ChatMessageRoleTool),
				Content:    toolContent,
				ToolCallID: wireID,
			})
		case api.MessageRoleSystem:
			out = append(out, Message{
				Role:    string(openai.ChatMessageRoleSystem),
				Content: m.Content,
			})
		default:
			out = append(out, Message{
				Role:    string(openai.ChatMessageRoleUser),
				Content: userMessageWireContent(m.Content, m.ArtifactIDs, vision, sessionID),
			})
		}
	}
	if _, ok := marked[len(msgs)-1]; len(msgs) > 0 && ok {
		markPromptCacheBreakpoint(out, marker)
	}
	flushToolImages()
	return out
}

// markPromptCacheBreakpoint sets the marker on the last content part emitted
// so far. Text bodies become their one-part form so the part can carry it;
// content with no part, such as a tool-call-only assistant turn, passes the
// marker back to the message before it.
func markPromptCacheBreakpoint(out []Message, marker providerprofile.PromptCacheMarker) {
	for j := len(out) - 1; j >= 0; j-- {
		if parts, ok := promptCacheBreakpointParts(out[j].Content, marker); ok {
			out[j].Content = parts
			return
		}
	}
}

// promptCacheBreakpointParts builds the marked form of content. Parts that
// already end in a marker keep it, so two boundaries on one row mark once.

func promptCacheBreakpointParts(content any, marker providerprofile.PromptCacheMarker) ([]ContentPart, bool) {
	var parts []ContentPart
	switch c := content.(type) {
	case string:
		if strings.TrimSpace(c) == "" {
			return nil, false
		}
		parts = []ContentPart{{Type: "text", Text: c}}
	case []ContentPart:
		if len(c) == 0 {
			return nil, false
		}
		parts = append([]ContentPart(nil), c...)
	default:
		return nil, false
	}
	last := &parts[len(parts)-1]
	if last.PromptCacheBreakpoint != nil || last.CacheControl != nil {
		return parts, true
	}
	switch marker {
	case providerprofile.PromptCacheMarkerBreakpoint:
		last.PromptCacheBreakpoint = &promptCacheBreakpointWire{Mode: promptCacheBreakpointExplicit}
	case providerprofile.PromptCacheMarkerCacheControl:
		last.CacheControl = &cacheControlWire{Type: cacheControlEphemeral}
	case providerprofile.PromptCacheMarkerNone, providerprofile.PromptCacheMarkerCatalog:
		return nil, false
	}
	return parts, true
}

func mapChatCompletionWire(resp *chatCompletionResponseWire) *modelcall.Completion {
	if resp == nil || len(resp.Choices) == 0 {
		return nil
	}
	choice := resp.Choices[0]
	msg := choice.Message
	out := &modelcall.Completion{
		Content:          msg.Content,
		Reasoning:        reasoningWireText(msg.Reasoning, msg.ReasoningContent),
		ReasoningDetails: msg.ReasoningDetails,
		Usage:            NormalizeUsage(resp.Usage),
	}
	lengthCapped := choice.FinishReason == openai.FinishReasonLength
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, toolCallFromWire(tc, lengthCapped))
	}
	return out
}

func reasoningWireText(reasoning, reasoningContent string) string {
	if reasoning != "" {
		return reasoning
	}
	return reasoningContent
}

func toolCallFromWire(tc toolCallWire, lengthCapped bool) api.ToolCall {
	var args map[string]any
	var truncated, malformed bool
	if raw := tc.Function.Arguments; raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			// The finish reason distinguishes truncation from malformed JSON.
			if lengthCapped {
				truncated = true
			} else {
				malformed = true
			}
		}
	}
	return api.ToolCall{
		ID:            providerwire.NewToolCallID(),
		WireID:        tc.ID,
		Name:          tc.Function.Name,
		Args:          args,
		ArgsTruncated: truncated,
		ArgsMalformed: malformed,
		ExtraContent:  providerwire.CloneMetadata(tc.ExtraContent),
	}
}

func imageURLPart(mime, base64Data string) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: "data:" + mime + ";base64," + base64Data}}
}

// userMessageWireContent attaches user-supplied ArtifactIDs as image_url parts on vision models.
func userMessageWireContent(text string, artifactIDs []string, vision bool, sessionID string) any {
	text = strings.TrimSpace(text)
	if !vision || len(artifactIDs) == 0 || !providerwire.HasVisualResolver() || strings.TrimSpace(sessionID) == "" {
		return text
	}
	parts := make([]ContentPart, 0, 1+len(artifactIDs))
	if text != "" {
		parts = append(parts, ContentPart{Type: "text", Text: text})
	}
	for _, id := range artifactIDs {
		mime, data, ok := providerwire.UserArtifactWireImage(sessionID, id)
		if !ok {
			continue
		}
		parts = append(parts, imageURLPart(mime, data))
	}
	if len(parts) == 0 {
		return text
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		return parts[0].Text
	}
	return parts
}

func mergeStreamToolDeltas(acc map[int]*providerwire.StreamTool, calls []toolCallWire) error {
	for _, tc := range calls {
		idx, err := streamToolIndex(acc, tc)
		if err != nil {
			return err
		}
		slot, ok := acc[idx]
		if !ok {
			// Keep the host identity stable across streamed deltas.
			slot = &providerwire.StreamTool{ID: providerwire.NewToolCallID()}
			acc[idx] = slot
		}
		if tc.ID != "" {
			slot.WireID = tc.ID
		}
		if tc.Function.Name != "" {
			slot.Name = tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			slot.Args.WriteString(tc.Function.Arguments)
		}
		if len(tc.ExtraContent) > 0 {
			slot.ExtraContent = providerwire.CloneMetadata(tc.ExtraContent)
		}
	}
	return nil
}

// streamToolIndex resolves a protocol identity; unidentified parallel fragments are ambiguous.
func streamToolIndex(acc map[int]*providerwire.StreamTool, call toolCallWire) (int, error) {
	if call.Index != nil {
		idx := *call.Index
		if idx < 0 {
			return 0, fmt.Errorf("negative tool-call index")
		}
		for existing, slot := range acc {
			if call.ID != "" && slot.WireID == call.ID && existing != idx {
				return 0, fmt.Errorf("tool-call identity changed index")
			}
		}
		if slot := acc[idx]; slot != nil && call.ID != "" && slot.WireID != "" && slot.WireID != call.ID {
			return 0, fmt.Errorf("tool-call index changed identity")
		}
		return idx, nil
	}
	if call.ID != "" {
		next := 0
		for idx, slot := range acc {
			if slot.WireID == call.ID {
				return idx, nil
			}
			if idx >= next {
				next = idx + 1
			}
		}
		return next, nil
	}
	if len(acc) > 1 {
		return 0, fmt.Errorf("parallel tool-call fragment has neither index nor id")
	}
	for idx := range acc {
		return idx, nil
	}
	return 0, nil
}

func (p *Provider) Prepare(req modelcall.CompletionRequest, stream bool) ([]byte, error) {
	return encodeChatCompletionRequest(req, p, stream, controlOpts{})
}
