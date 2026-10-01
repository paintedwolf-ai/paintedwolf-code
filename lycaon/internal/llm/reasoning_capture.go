package llm

import (
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/observability"
)

const maxReasoningCaptureBytes = 512 * 1024

// Buffer complete reasoning for redaction before taking a tail. If the buffer
// fills, omit the text: clipping raw bytes could expose part of a credential.
type reasoningCapture struct {
	bytes int
	text  strings.Builder
}

func (c *reasoningCapture) append(text string) {
	c.bytes += len(text)
	if c.bytes > maxReasoningCaptureBytes {
		c.text.Reset()
		return
	}
	c.text.WriteString(text)
}

func (c *reasoningCapture) summarize(summary *observability.LLMCompletionCapture, err error) *observability.LLMCompletionCapture {
	if c.bytes == 0 {
		return summary
	}
	if summary == nil {
		summary = &observability.LLMCompletionCapture{}
	}
	summary.ReasoningBytes = c.bytes
	_, empty := failure.AsProviderEmptyCompletion(err)
	_, truncated := failure.AsProviderOutputTruncated(err)
	if !empty && !(truncated && summary.ContentChars == 0 && len(summary.ToolCalls) == 0) {
		return summary
	}
	summary.ReasoningCaptureOmitted = c.bytes > maxReasoningCaptureBytes
	if summary.ReasoningCaptureOmitted {
		return summary
	}
	runes := []rune(observability.RedactCaptureText(c.text.String()))
	if len(runes) > 2048 {
		runes = runes[len(runes)-2048:]
	}
	summary.ReasoningTail = string(runes)
	return summary
}
