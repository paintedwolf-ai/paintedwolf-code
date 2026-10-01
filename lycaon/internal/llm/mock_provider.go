package llm

import (
	"context"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// MockProvider implements LLMClient with pattern-based fixture responses.
type MockProvider struct {
	config MockConfig
}

// NewMockProvider returns a mock LLM client.
func NewMockProvider(cfg *MockConfig) *MockProvider {
	if cfg == nil {
		cfg = &MockConfig{}
	}
	return &MockProvider{config: *cfg}
}

// Complete returns a completion matched against the last user message.
func (p *MockProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	vision := p.modelVision(req.Model)
	req.Messages = providerwire.PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)

	lastUser := lastUserMessage(req.Messages)
	if lastUser == "" && !hasToolResultsSinceLastUser(req.Messages) {
		return p.defaultCompletion("I received an empty prompt."), nil
	}

	afterTools := hasToolResultsSinceLastUser(req.Messages)
	if afterTools {
		if digest := perceiveImageDigest(req.Messages); digest != "" {
			return &modelcall.Completion{Content: "image-echo:" + digest}, nil
		}
	}

	if lastUser == "" {
		return p.defaultCompletion("Done."), nil
	}
	if verify := mockVerifyForSourceEvidenceHold(req.Messages, req.Tools); verify != nil {
		return verify, nil
	}
	entry, _ := p.matchEntryFromMessages(req.Messages, req.Tools)
	if entry == nil {
		return p.defaultCompletion("I received: " + lastUser), nil
	}

	if afterTools && !entry.AlwaysTool {
		text := entry.FollowUpText
		if text == "" {
			text = entry.Text
		}
		if text == "" {
			text = "Done."
		}
		return &modelcall.Completion{Content: text}, nil
	}

	if len(entry.ToolCalls) > 0 {
		return &modelcall.Completion{Content: entry.Text, ToolCalls: mockToolCalls(entry.ToolCalls)}, nil
	}
	if entry.Text != "" {
		return &modelcall.Completion{Content: entry.Text}, nil
	}
	return p.defaultCompletion("I received: " + lastUser), nil
}

// Stream returns token chunks derived from Complete.
func (p *MockProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := p.Complete(ctx, req)
		if err != nil {
			return
		}
		if len(completion.ToolCalls) > 0 {
			if strings.TrimSpace(completion.Content) != "" {
				if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: completion.Content}) {
					return
				}
			}
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Done: true})
			return
		}
		tokens := strings.Fields(completion.Content)
		if len(tokens) == 0 {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Done: true})
			return
		}
		for i, token := range tokens {
			suffix := " "
			if i == len(tokens)-1 {
				suffix = ""
			}
			if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{
				Content: token + suffix,
				Done:    i == len(tokens)-1,
			}) {
				return
			}
		}
	}()
	return ch, nil
}

func (p *MockProvider) matchEntryFromMessages(messages []api.Message, available []tools.ToolMeta) (*MockResponseEntry, string) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !isMockUserPrompt(msg) {
			continue
		}
		content := msg.Content
		if entry := p.matchEntry(content, available); entry != nil {
			return entry, content
		}
	}
	return nil, ""
}

func (p *MockProvider) matchEntry(lastUser string, available []tools.ToolMeta) *MockResponseEntry {
	for i := range p.config.Responses {
		entry := &p.config.Responses[i]
		if entry.Pattern == "" {
			continue
		}
		re, err := regexp.Compile("(?is)" + entry.Pattern)
		if err != nil {
			continue
		}
		if re.MatchString(lastUser) && entryToolsAvailable(entry, available) {
			return entry
		}
	}
	return nil
}

func entryToolsAvailable(entry *MockResponseEntry, available []tools.ToolMeta) bool {
	if entry == nil || len(entry.ToolCalls) == 0 {
		return true
	}
	availableNames := make(map[string]struct{}, len(available))
	for _, tool := range available {
		availableNames[tool.Name] = struct{}{}
	}
	for _, tc := range entry.ToolCalls {
		if _, ok := availableNames[tc.Name]; !ok {
			return false
		}
	}
	return true
}

func (p *MockProvider) modelVision(model string) bool {
	model = strings.TrimSpace(model)
	for _, id := range p.config.VisionModels {
		if modelinfo.EquivalentID("mock", id, model) {
			return true
		}
	}
	return false
}

func perceiveImageDigest(messages []api.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.Visual == nil {
			continue
		}
		v := msg.ToolResult.Visual
		if !v.Perceive || len(v.Bytes) == 0 {
			continue
		}
		return providerwire.PerceiveImageFixtureDigest(v.Bytes)
	}
	return ""
}

func mockVerifyForSourceEvidenceHold(messages []api.Message, available []tools.ToolMeta) *modelcall.Completion {
	if !toolNamed(available, "verify") {
		return nil
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil &&
			strings.TrimSpace(msg.ToolResult.Tool) == "verify" {
			return nil
		}
		if strings.Contains(msg.Content, "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT") {
			return &modelcall.Completion{ToolCalls: []api.ToolCall{{
				ID: "mock-source-evidence-verify", Name: "verify",
				Args: map[string]any{"command": "true"},
			}}}
		}
	}
	return nil
}

func toolNamed(available []tools.ToolMeta, name string) bool {
	for _, tool := range available {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func (p *MockProvider) defaultCompletion(text string) *modelcall.Completion {
	return &modelcall.Completion{Content: text}
}

func mockToolCalls(entries []MockToolCall) []api.ToolCall {
	out := make([]api.ToolCall, 0, len(entries))
	for _, e := range entries {
		args := e.Args
		if args == nil {
			args = map[string]any{}
		}
		out = append(out, api.ToolCall{
			ID:   e.ID,
			Name: e.Name,
			Args: args,
		})
	}
	return out
}

func lastUserMessage(messages []api.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if isMockUserPrompt(msg) {
			return msg.Content
		}
	}
	return ""
}

func hasToolResultsSinceLastUser(messages []api.Message) bool {
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if isMockUserPrompt(msg) {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return false
	}
	for i := lastUserIdx + 1; i < len(messages); i++ {
		if messages[i].Role == api.MessageRoleTool {
			return true
		}
	}
	return false
}

func isMockUserPrompt(msg api.Message) bool {
	if msg.Role != api.MessageRoleUser || msg.Visibility == api.MessageVisibilityInternal {
		return false
	}
	if msg.Origin == "" && msg.Authority == "" {
		return true
	}
	return msg.Origin == api.MessageOriginUser || msg.Authority == api.ContentAuthorityUser
}
