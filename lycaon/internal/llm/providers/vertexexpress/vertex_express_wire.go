package vertexexpress

import (
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// These types project host messages onto the generateContent wire.
// Function calls pair by name, so tool-call identity stays host-assigned.

// vertexExpressInlineData is a base64 image part on a vision model.
type vertexExpressInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// vertexExpressFunctionCall is the model's request to invoke a tool. Args
// arrive as a complete object; the provider does not stream partial arguments.
type vertexExpressFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// vertexExpressFunctionResponse returns a tool result. Response is an object,
// so the host's string result is wrapped under a single key.
type vertexExpressFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type vertexExpressPart struct {
	Text             string                         `json:"text,omitempty"`
	InlineData       *vertexExpressInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *vertexExpressFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *vertexExpressFunctionResponse `json:"functionResponse,omitempty"`
	// Thought identifies reasoning emitted with includeThoughts enabled.
	Thought bool `json:"thought,omitempty"`
}

// vertexExpressContent carries user/model turns; system instructions are separate.
type vertexExpressContent struct {
	Role  string              `json:"role,omitempty"`
	Parts []vertexExpressPart `json:"parts"`
}

type vertexExpressFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type vertexExpressTool struct {
	FunctionDeclarations []vertexExpressFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

// vertexExpressThinkingConfig sets the reasoning allowance and visibility.
type vertexExpressThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

type vertexExpressGenerationConfig struct {
	Temperature     *float64                     `json:"temperature,omitempty"`
	MaxOutputTokens int                          `json:"maxOutputTokens,omitempty"`
	StopSequences   []string                     `json:"stopSequences,omitempty"`
	ResponseMIMEs   string                       `json:"responseMimeType,omitempty"`
	ThinkingConfig  *vertexExpressThinkingConfig `json:"thinkingConfig,omitempty"`
}

type Request struct {
	Contents          []vertexExpressContent         `json:"contents"`
	SystemInstruction *vertexExpressContent          `json:"systemInstruction,omitempty"`
	Tools             []vertexExpressTool            `json:"tools,omitempty"`
	ToolConfig        *vertexExpressToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *vertexExpressGenerationConfig `json:"generationConfig,omitempty"`
}

// vertexExpressToolConfig selects how the model may use declared functions.
type vertexExpressToolConfig struct {
	FunctionCallingConfig vertexExpressFunctionCallingConfig `json:"functionCallingConfig"`
}

type vertexExpressFunctionCallingConfig struct {
	Mode string `json:"mode"`
}

type vertexExpressUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	CachedContentTokens  int `json:"cachedContentTokenCount"`
}

type vertexExpressCandidate struct {
	Content      vertexExpressContent `json:"content"`
	FinishReason string               `json:"finishReason"`
}

type vertexExpressPromptFeedback struct {
	BlockReason        string `json:"blockReason"`
	BlockReasonMessage string `json:"blockReasonMessage"`
}

type vertexExpressResponse struct {
	Candidates     []vertexExpressCandidate     `json:"candidates"`
	UsageMetadata  *vertexExpressUsageMetadata  `json:"usageMetadata"`
	PromptFeedback *vertexExpressPromptFeedback `json:"promptFeedback"`
}

// vertexExpressToolResultKey wraps string results in the required response object.
const vertexExpressToolResultKey = "content"

// vertexExpressEmptyOutputErr includes the finish reason for accepted but empty responses.
func vertexExpressEmptyOutputErr(providerID, model, finishReason string, hasOutput bool) error {
	if hasOutput {
		return nil
	}
	finishReason = strings.TrimSpace(finishReason)
	return &failure.ProviderEmptyCompletionError{
		ProviderID: providerID,
		Model:      model,
		Retryable:  finishReason == "" || finishReason == "STOP",
		Reason:     finishReason,
		Terminal:   finishReason != "",
	}
}

// tokenUsageFromVertexExpress maps inclusive cached input and thought output.
func tokenUsageFromVertexExpress(u *vertexExpressUsageMetadata) modelcall.TokenUsage {
	if u == nil {
		return modelcall.TokenUsage{}
	}
	return modelcall.TokenUsage{
		Present:              true,
		PromptTokens:         u.PromptTokenCount,
		CompletionTokens:     u.CandidatesTokenCount + u.ThoughtsTokenCount,
		CacheReadInputTokens: u.CachedContentTokens,
	}
}

// ProjectMessages separates the standing preamble and keeps later host context
// in conversation order, merging consecutive same-role turns.
// Tool results pair by their host-stamped function names.
func ProjectMessages(msgs []api.Message, vision bool, sessionID string) (system *vertexExpressContent, out []vertexExpressContent) {
	var systemParts []vertexExpressPart
	pendingCalls := make(map[string]int)

	add := func(role string, parts ...vertexExpressPart) {
		if len(parts) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			return
		}
		out = append(out, vertexExpressContent{Role: role, Parts: parts})
	}

	// Images follow every function response of their run in the same turn.
	var toolImages []vertexExpressPart
	flushToolImages := func() {
		add("user", toolImages...)
		toolImages = nil
	}
	preambleEnd := providerwire.SystemPreambleEnd(msgs)
	for i, m := range msgs {
		if m.Role != api.MessageRoleTool {
			flushToolImages()
		}
		switch m.Role {
		case api.MessageRoleSystem:
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			if i < preambleEnd {
				systemParts = append(systemParts, vertexExpressPart{Text: m.Content})
			} else {
				add("user", vertexExpressPart{Text: m.Content})
			}
		case api.MessageRoleAssistant:
			if m.Content != "" {
				add("model", vertexExpressPart{Text: m.Content})
			}
			if len(m.ToolCalls) > 0 {
				clear(pendingCalls)
				for _, tc := range m.ToolCalls {
					name := strings.TrimSpace(tc.Name)
					if name == "" {
						continue
					}
					add("model", vertexExpressPart{FunctionCall: &vertexExpressFunctionCall{
						Name: name,
						Args: providerwire.ToolArgs(tc.Args),
					}})
					pendingCalls[name]++
				}
			}
		case api.MessageRoleTool:
			content := m.Content
			if content == "" && m.ToolResult != nil {
				content = m.ToolResult.Content
			}
			name := ""
			if m.ToolResult != nil {
				name = strings.TrimSpace(m.ToolResult.Tool)
			}
			if name == "" || pendingCalls[name] == 0 {
				// Only pending calls may produce tool results in user-shaped turns.
				continue
			}
			pendingCalls[name]--
			add("user", vertexExpressPart{FunctionResponse: &vertexExpressFunctionResponse{
				Name:     name,
				Response: map[string]any{vertexExpressToolResultKey: content},
			}})
			if image, ok := providerwire.ToolResultImage(m); ok {
				toolImages = append(toolImages, vertexExpressPart{InlineData: &vertexExpressInlineData{MimeType: image.Mime, Data: image.Base64}})
			}
		default:
			add("user", vertexExpressUserParts(m.Content, m.ArtifactIDs, vision, sessionID)...)
		}
	}

	flushToolImages()
	if len(systemParts) > 0 {
		system = &vertexExpressContent{Parts: systemParts}
	}
	return system, out
}

// vertexExpressUserParts adds inline image data when vision is available.
func vertexExpressUserParts(text string, artifactIDs []string, vision bool, sessionID string) []vertexExpressPart {
	text = strings.TrimSpace(text)
	if !vision || len(artifactIDs) == 0 || !providerwire.HasVisualResolver() || strings.TrimSpace(sessionID) == "" {
		return []vertexExpressPart{{Text: text}}
	}
	out := make([]vertexExpressPart, 0, 1+len(artifactIDs))
	if text != "" {
		out = append(out, vertexExpressPart{Text: text})
	}
	for _, id := range artifactIDs {
		mime, data, ok := providerwire.UserArtifactWireImage(sessionID, id)
		if !ok {
			continue
		}
		out = append(out, vertexExpressPart{InlineData: &vertexExpressInlineData{MimeType: mime, Data: data}})
	}
	if len(out) == 0 {
		if text == "" {
			return nil
		}
		return []vertexExpressPart{{Text: text}}
	}
	return out
}

func ProjectTools(metas []tools.ToolMeta) []vertexExpressTool {
	if len(metas) == 0 {
		return nil
	}
	decls := make([]vertexExpressFunctionDeclaration, 0, len(metas))
	for _, raw := range metas {
		t := providerwire.ProjectToolMetaForModel(raw)
		decls = append(decls, vertexExpressFunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.ArgsSchema,
		})
	}
	// The API takes one tool object holding every declaration, not one per tool.
	return []vertexExpressTool{{FunctionDeclarations: decls}}
}

// vertexExpressPartsToCompletion separates reasoning from answer content.
func vertexExpressPartsToCompletion(parts []vertexExpressPart) (content, reasoning string, toolCalls []api.ToolCall) {
	var body, thoughts strings.Builder
	for _, part := range parts {
		switch {
		case part.FunctionCall != nil:
			toolCalls = append(toolCalls, api.ToolCall{
				ID:   providerwire.NewToolCallID(),
				Name: part.FunctionCall.Name,
				Args: part.FunctionCall.Args,
			})
		case part.Thought:
			thoughts.WriteString(part.Text)
		default:
			body.WriteString(part.Text)
		}
	}
	return body.String(), thoughts.String(), toolCalls
}

// mapVertexExpressResponse projects a non-streaming generateContent response
// onto a Completion.
func mapVertexExpressResponse(resp *vertexExpressResponse) *modelcall.Completion {
	if resp == nil {
		return nil
	}
	var parts []vertexExpressPart
	if len(resp.Candidates) > 0 {
		parts = resp.Candidates[0].Content.Parts
	}
	content, reasoning, toolCalls := vertexExpressPartsToCompletion(parts)
	return &modelcall.Completion{
		Content:   content,
		Reasoning: reasoning,
		ToolCalls: toolCalls,
		Usage:     tokenUsageFromVertexExpress(resp.UsageMetadata),
	}
}
