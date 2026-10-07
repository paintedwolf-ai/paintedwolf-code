package promptloop

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ProseTurnToolCallError ends a final, tool-less turn the model answered with
// tool calls instead of a summary. The calls did not run; the provider answered.
type ProseTurnToolCallError struct {
	ProviderID string
	Model      string
	Tools      []string
	// CloseoutReason is why the turn had become its final one.
	CloseoutReason string
}

func (e *ProseTurnToolCallError) Error() string {
	return fmt.Sprintf("final turn answered with %s instead of a summary; the call was not run", strings.Join(e.Tools, ", "))
}

// NoticeCode routes the turn failure to its own notice.
func (e *ProseTurnToolCallError) NoticeCode() api.NoticeCode { return api.NoticeCodeTurnCloseoutToolCall }

// NoticeProseTurnToolCalls supplies the notice's facts.
func (e *ProseTurnToolCallError) NoticeProseTurnToolCalls() (tools []string, closeoutReason string) {
	return append([]string(nil), e.Tools...), e.CloseoutReason
}

func proseTurnToolNames(calls []api.ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if name := strings.TrimSpace(call.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}
