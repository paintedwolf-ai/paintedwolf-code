package command

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// writeLimitRefusal presents only the typed program budget stop in shell voice.
// The structured rejection keeps the exact limit and source facts.
func writeLimitRefusal(streams commandprogram.Streams, command string, reject *toolrejection.ToolReject) {
	if streams.Error == nil || reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" {
		return
	}
	reason := "limit exceeded"
	if catalog, err := guidance.LoadHintConfigStock(); err == nil && catalog != nil {
		if entry, ok := catalog.HintCodes[reject.Code]; ok {
			if what, _, _ := guidance.RenderHintFields(reject.Code, entry, reject.Data); what != "" {
				reason = strings.TrimSuffix(strings.Join(strings.Fields(what), " "), ".")
			}
		}
	}
	_, _ = fmt.Fprintf(streams.Error, "%s: request: %s [%s]\n", command, reason, reject.Code)
}
