package messageview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/pkg/api"
)

const InlineContentBytes = 8192
const InlineArgumentsBytes = 4096

// ContentReference identifies the screened text revision.
func ContentReference(field, callID, text string) api.ChatContentReference {
	hash := sha256.Sum256([]byte(text))
	return api.ChatContentReference{Field: field, ToolCallID: callID, SHA256: hex.EncodeToString(hash[:]), TotalRunes: utf8.RuneCountInString(text), SizeBytes: len(text)}
}

// projectContent keeps ordinary interactions ready and references oversized bodies.
func projectContent(msg api.Message) api.Message {
	out := msg
	if len(out.ToolCalls) != 0 {
		out.ToolCalls = append([]api.ToolCall(nil), out.ToolCalls...)
		for i := range out.ToolCalls {
			call := &out.ToolCalls[i]
			if call.ArgsRef == nil {
				call.DisplayTitle = toolpresentation.Title(call.Name, call.Args)
				call.Args, call.ArgsRef = argumentPreview(call.ID, call.Args, "tool_calls."+strconv.Itoa(i)+".args", out.HostSecretRedaction)
			}
			call.ExtraContent = nil
		}
	}
	if out.ToolResult != nil {
		result := *out.ToolResult
		result.ToolArgs, _ = maskArgs(result.Tool, result.ToolArgs)
		if result.ToolArgsRef == nil {
			subject := result.DisplaySubject
			if subject == "" && result.Visual != nil {
				subject = result.Visual.Caption
			}
			result.DisplayTitle = toolpresentation.SubjectTitle(result.Tool, result.ToolArgs, subject)
			result.ToolArgs, result.ToolArgsRef = argumentPreview(result.ToolCallID, result.ToolArgs, "tool_result.tool_args", out.HostSecretRedaction)
		}
		if result.ContentRef == nil && len(result.Content) > InlineContentBytes && !protectedOutput(result.Tool) {
			text, spans := readableContent(result.Content, contentSpans(msg, "tool_result.content"))
			ref := prepareContentReference("tool_output", result.ToolCallID, text, spans)
			result.ContentRef = &ref
			result.Content = ""
		}
		out.ToolResult = &result
		// Tool output is carried only by ToolResult.
		out.Content, out.ContentParts = "", nil
	}
	return out
}

func argumentPreview(callID string, args map[string]any, prefix string, meta *api.HostSecretRedactionMeta) (map[string]any, *api.ChatContentReference) {
	body, err := json.Marshal(args)
	if err != nil || len(body) <= InlineArgumentsBytes {
		return args, nil
	}
	text, spans, err := argumentContent(args, prefix, meta)
	if err != nil {
		return args, nil
	}
	ref := prepareContentReference("tool_args", callID, text, spans)
	// The host subtitle carries orientation; the reader carries exact arguments.
	return map[string]any{}, &ref
}

var ErrContentNotFound = errors.New("chat content not found")

// RetainedContent uses the same observer mask as the transcript, before previewing.
func RetainedContent(msg api.Message, field, callID string) (string, []api.RedactedSpan, error) {
	msg = RedactMessage(msg)
	switch field {
	case "content":
		if callID != "" {
			return "", nil, ErrContentNotFound
		}
		return msg.Content, contentSpans(msg, "content"), nil
	case "tool_output":
		if msg.ToolResult == nil || msg.ToolResult.ToolCallID != callID || protectedOutput(msg.ToolResult.Tool) {
			return "", nil, ErrContentNotFound
		}
		text, spans := readableContent(msg.ToolResult.Content, contentSpans(msg, "tool_result.content"))
		return text, spans, nil
	case "tool_args":
		for i, call := range msg.ToolCalls {
			if call.ID == callID {
				return argumentContent(call.Args, "tool_calls."+strconv.Itoa(i)+".args", msg.HostSecretRedaction)
			}
		}
		if msg.ToolResult != nil && msg.ToolResult.ToolCallID == callID {
			return argumentContent(msg.ToolResult.ToolArgs, "tool_result.tool_args", msg.HostSecretRedaction)
		}
	}
	return "", nil, ErrContentNotFound
}

func protectedOutput(tool string) bool {
	return tool == "secret_generate" || tool == "secret_list" || tool == "secret_revoke"
}

func contentSpans(msg api.Message, field string) []api.RedactedSpan {
	var spans []api.RedactedSpan
	for _, span := range msg.HostSecretRedaction.SpanList() {
		if span.Field == field {
			spans = append(spans, span)
		}
	}
	return spans
}
