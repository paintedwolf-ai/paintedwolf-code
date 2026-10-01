package compaction

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// pairToolResultsToCalls maps each tool-result index to its originating call
// using FIFO assistant→tool pairing.
func pairToolResultsToCalls(messages []api.Message) map[int]api.ToolCall {
	out := map[int]api.ToolCall{}
	var pending []api.ToolCall
	for i, m := range messages {
		switch m.Role {
		case api.MessageRoleAssistant:
			if len(m.ToolCalls) > 0 {
				pending = append(pending[:0], m.ToolCalls...)
			}
		case api.MessageRoleTool:
			if len(pending) == 0 {
				continue
			}
			out[i] = pending[0]
			pending = pending[1:]
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return out
}

func normalizeReadPath(v any) string {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.TrimPrefix(s, "./")
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
