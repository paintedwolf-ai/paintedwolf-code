package transcript

import (
	"encoding/json"

	"github.com/lycaon/lycaon/pkg/api"
)

const (
	toolFeedbackEnvelopeReserve = 256
	maxInvocationFeedbackBytes  = 2 << 10
)

// ToolFeedbackFitsFirst reports whether the first feedback entry can retain its
// details even beside the largest permitted invocation envelope. Later entries
// share the remaining budget and are not covered by this guarantee.
func ToolFeedbackFitsFirst(feedback api.ToolFeedback) bool {
	raw, err := json.Marshal(feedback)
	return err == nil && len(raw)+1 <= maxToolFeedbackBytes-toolFeedbackEnvelopeReserve-maxInvocationFeedbackBytes
}
