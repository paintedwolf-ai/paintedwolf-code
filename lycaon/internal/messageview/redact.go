// Package messageview projects durable messages onto observer surfaces.
package messageview

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const RedactedToolArgPlaceholder = "[redacted]"

// Terminal input is masked on observer surfaces.
const maskedTerminalArg = "input"

// RedactToolCallArgs copies calls and records spans for masked fields.
func RedactToolCallArgs(calls []api.ToolCall) ([]api.ToolCall, []api.RedactedSpan) {
	if len(calls) == 0 {
		return calls, nil
	}
	out := make([]api.ToolCall, len(calls))
	var spans []api.RedactedSpan
	for i, call := range calls {
		out[i] = call
		args, masked := maskArgs(call.Name, call.Args)
		out[i].Args = args
		if masked {
			spans = append(spans, api.RedactedSpan{
				Field:  "tool_calls." + strconv.Itoa(i) + ".args." + maskedTerminalArg,
				Start:  0,
				Length: len([]rune(RedactedToolArgPlaceholder)),
				Kind:   api.RedactionKindObserverMask,
				Source: api.RedactionSourcePolicy,
			})
		}
	}
	return out, spans
}

// RedactMessage masks terminal input on a copy and records policy spans.
func RedactMessage(msg api.Message) api.Message {
	out := msg
	calls, spans := RedactToolCallArgs(msg.ToolCalls)
	out.ToolCalls = calls
	if msg.ToolResult != nil {
		args, masked := maskArgs(msg.ToolResult.Tool, msg.ToolResult.ToolArgs)
		if masked {
			result := *msg.ToolResult
			result.ToolArgs = args
			out.ToolResult = &result
			spans = append(spans, api.RedactedSpan{Field: "tool_result.tool_args.input", Start: 0, Length: len(RedactedToolArgPlaceholder), Kind: api.RedactionKindObserverMask, Source: api.RedactionSourcePolicy})
		}
	}
	if len(spans) > 0 {
		out.HostSecretRedaction = withMaskSpans(msg.HostSecretRedaction, spans)
	}
	return out
}

// withMaskSpans adds observer spans beside what the durable screen recorded.
func withMaskSpans(prior *api.HostSecretRedactionMeta, spans []api.RedactedSpan) *api.HostSecretRedactionMeta {
	merged := spans
	if kept := prior.SpanList(); len(kept) > 0 {
		merged = append(append([]api.RedactedSpan(nil), kept...), spans...)
	}
	unique := make([]api.RedactedSpan, 0, len(merged))
	seen := make(map[api.RedactedSpan]bool)
	for _, span := range merged {
		if !seen[span] {
			unique = append(unique, span)
			seen[span] = true
		}
	}
	return api.NewHostSecretRedactionMeta(unique)
}

// RedactMessages projects a message slice without changing its input.
func RedactMessages(msgs []api.Message) []api.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]api.Message, len(msgs))
	for i, msg := range msgs {
		out[i] = RedactMessage(msg)
	}
	return out
}

func maskArgs(tool string, args map[string]any) (map[string]any, bool) {
	if args == nil || strings.TrimSpace(tool) != "terminal_send" {
		return args, false
	}
	if _, ok := args[maskedTerminalArg]; !ok {
		return args, false
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	out[maskedTerminalArg] = RedactedToolArgPlaceholder
	return out, true
}
