package providerwire

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

func CloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// StreamTool accumulates one tool call's stream deltas.
type StreamTool struct {
	ID           string
	WireID       string
	Name         string
	Args         strings.Builder
	ExtraContent map[string]any
}

// CollectToolCalls materializes accumulated stream deltas.
func CollectToolCalls(acc map[int]*StreamTool, lengthCapped, done bool) []api.ToolCall {
	if len(acc) == 0 {
		return nil
	}
	indices := make([]int, 0, len(acc))
	for i := range acc {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]api.ToolCall, 0, len(indices))
	for _, i := range indices {
		slot := acc[i]
		var args map[string]any
		var truncated, malformed bool
		raw := slot.Args.String()
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				switch {
				case lengthCapped:
					truncated = true
				case done:
					// Complete arguments require valid JSON.
					malformed = true
				}
			}
		}
		out = append(out, api.ToolCall{
			ID:            slot.ID,
			WireID:        slot.WireID,
			Name:          slot.Name,
			Args:          args,
			ArgsTruncated: truncated,
			ArgsMalformed: malformed,
			ExtraContent:  CloneMetadata(slot.ExtraContent),
		})
	}
	return out
}

// NewToolCallID generates a compact host id independent of provider-local counters.
func NewToolCallID() string {
	return "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
}

func ToolArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	return args
}

func ToolCallArgumentsJSON(args map[string]any) string {
	raw, err := surveyjson.Marshal(ToolArgs(args))
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// ToolCallPairs binds host result identities to the IDs offered on this wire.
type ToolCallPairs map[string]string

func (p ToolCallPairs) Add(call api.ToolCall, roundTrip bool) string {
	id := call.ID
	if roundTrip && call.WireID != "" {
		id = call.WireID
	}
	p[call.ID] = id
	return id
}

func (p ToolCallPairs) Take(result *api.ToolResult) (string, bool) {
	if result == nil || result.ToolCallID == "" {
		return "", false
	}
	id, ok := p[result.ToolCallID]
	delete(p, result.ToolCallID)
	return id, ok && id != ""
}
