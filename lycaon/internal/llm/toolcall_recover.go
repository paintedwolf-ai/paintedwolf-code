package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/actionenvelope"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Text recovery accepts only declared envelope markers.
var (
	toolCallTagRE   = regexp.MustCompile(`(?is)<tool_call>\s*(.*?)\s*</tool_call>`)
	toolCallsPrefix = "[TOOL_CALLS]"
)

// proseToolCall accepts the declared argument field variants.
type proseToolCall struct {
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
	Parameters json.RawMessage `json:"parameters"`
}

// ToolResponse retains the delivered completion and the non-executable action
// observation. Only a native call or a request-selected protocol supplies calls.
type ToolResponse struct {
	Completion      *modelcall.Completion
	TextActionTools []string
}

// ClassifyToolResponse is the action/content boundary for every model role.
func ClassifyToolResponse(profile providerprofile.Profile, req modelcall.CompletionRequest, c *modelcall.Completion, providerID, model string) (*ToolResponse, error) {
	out := &ToolResponse{Completion: c}
	if c == nil || len(c.ToolCalls) > 0 || !req.ToolsCallable() {
		return out, nil
	}
	switch req.ToolProtocol {
	case modelcall.ToolProtocolNative:
		offered := make([]string, 0, len(req.Tools))
		for _, meta := range req.Tools {
			offered = append(offered, meta.Name)
		}
		out.TextActionTools = actionenvelope.Names(c.Content, offered)
		return out, nil
	case modelcall.ToolProtocolHarmony:
		if !profile.TextToolCallGrammars.Supports(providerprofile.TextToolCallGrammarHarmony) {
			return nil, fmt.Errorf("provider does not support the selected Harmony action protocol")
		}
		recovered := *c
		out.Completion = RecoverHarmonyCompletion(&recovered, true)
		if len(recovered.ToolCalls) > 0 && !validRecoveredToolCalls(recovered.ToolCalls, req.Tools) {
			return nil, &failure.ProviderToolCallsInProseError{ProviderID: providerID, Model: model}
		}
		return out, nil
	case modelcall.ToolProtocolEnvelope:
		if !profile.TextToolCallGrammars.Supports(providerprofile.TextToolCallGrammarBoundedEnvelope) {
			return nil, fmt.Errorf("provider does not support the selected envelope action protocol")
		}
	default:
		return nil, fmt.Errorf("unknown action protocol %q", req.ToolProtocol)
	}
	calls, detected := recoverToolCallsFromProse(c.Content)
	if !detected {
		return out, nil
	}
	if len(calls) > 0 && validRecoveredToolCalls(calls, req.Tools) {
		recovered := *c
		recovered.ToolCalls, recovered.Content = calls, ""
		out.Completion = &recovered
		return out, nil
	}
	if !profile.ToolCalls.SupportsToolCalls() {
		return nil, &failure.ProviderToolCallsUnsupportedError{ProviderID: providerID, Model: model}
	}
	return nil, &failure.ProviderToolCallsInProseError{ProviderID: providerID, Model: model}
}

func validRecoveredToolCalls(calls []api.ToolCall, offered []tools.ToolMeta) bool {
	byName := make(map[string]tools.ToolMeta, len(offered))
	for _, meta := range offered {
		byName[meta.Name] = meta
	}
	for _, call := range calls {
		meta, ok := byName[call.Name]
		if !ok || tools.ValidateToolArgs(meta.ArgsSchema, call.Args) != nil {
			return false
		}
	}
	return true
}

// recoverToolCallsFromProse parses declared envelope forms.
func recoverToolCallsFromProse(content string) (calls []api.ToolCall, detected bool) {
	content = strings.TrimSpace(content)
	if matches := toolCallTagRE.FindAllStringSubmatch(content, -1); len(matches) > 0 && strings.TrimSpace(toolCallTagRE.ReplaceAllString(content, "")) == "" {
		detected = true
		for _, m := range matches {
			var valid bool
			calls, valid = appendParsed(calls, m[1])
			if !valid {
				return nil, true
			}
		}
		return calls, detected
	}

	if strings.HasPrefix(content, toolCallsPrefix) {
		detected = true
		calls, _ = appendParsed(calls, content[len(toolCallsPrefix):])
		return calls, detected
	}

	if looksLikeToolCall(strings.TrimSpace(content)) {
		detected = true
		calls, _ = appendParsed(calls, content)
	}
	return calls, detected
}

// looksLikeToolCall reports JSON that names a tool and its args.
func looksLikeToolCall(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return false
	}
	if !strings.Contains(s, `"name"`) {
		return false
	}
	return strings.Contains(s, `"arguments"`) || strings.Contains(s, `"parameters"`)
}

// appendParsed rejects the whole batch if any call is malformed.
func appendParsed(calls []api.ToolCall, raw string) ([]api.ToolCall, bool) {
	raw = strings.TrimSpace(raw)
	var batch []proseToolCall
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &batch); err != nil {
			return nil, false
		}
	} else {
		var single proseToolCall
		if err := json.Unmarshal([]byte(raw), &single); err != nil {
			return nil, false
		}
		batch = []proseToolCall{single}
	}
	if len(batch) == 0 {
		return nil, false
	}
	for _, item := range batch {
		call, ok := toolCallFromProse(item, len(calls))
		if !ok {
			return nil, false
		}
		calls = append(calls, call)
	}
	return calls, true
}

func toolCallFromProse(p proseToolCall, index int) (api.ToolCall, bool) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return api.ToolCall{}, false
	}
	raw := p.Arguments
	if len(raw) == 0 {
		raw = p.Parameters
	}
	args := parseProseArgs(raw)
	if args == nil {
		return api.ToolCall{}, false
	}
	return api.ToolCall{
		ID:   fmt.Sprintf("recovered-%d", index+1),
		Name: name,
		Args: args,
	}, true
}

// parseProseArgs accepts object and encoded-object arguments.
func parseProseArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		var nested map[string]any
		if err := json.Unmarshal([]byte(encoded), &nested); err == nil {
			return nested
		}
	}
	return nil
}
