package providerwire

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
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

const alphanumeric62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// IsAlphanumeric9ToolCallID reports whether id is strictly 9 ASCII alphanumeric characters.
func IsAlphanumeric9ToolCallID(id string) bool {
	if len(id) != 9 {
		return false
	}
	for i := 0; i < 9; i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// NormalizeAlphanumeric9ToolCallID returns id when it is already 9 ASCII alphanumeric
// characters, or a deterministic 9-character base62 digest otherwise.
func NormalizeAlphanumeric9ToolCallID(id string) string {
	trimmed := strings.TrimSpace(id)
	if IsAlphanumeric9ToolCallID(trimmed) {
		return trimmed
	}
	return hashAlphanumeric9(trimmed)
}

func hashAlphanumeric9(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	var out [9]byte
	for i := range out {
		val := binary.BigEndian.Uint16(sum[i*2 : i*2+2])
		out[i] = alphanumeric62[int(val)%len(alphanumeric62)]
	}
	return string(out[:])
}

// AddAlphanumeric9 binds a tool call to a 9-character ASCII alphanumeric wire identity.
func (p ToolCallPairs) AddAlphanumeric9(call api.ToolCall) string {
	candidate := strings.TrimSpace(call.WireID)
	if candidate == "" {
		candidate = strings.TrimSpace(call.ID)
	}
	wireID := NormalizeAlphanumeric9ToolCallID(candidate)
	for suffix := 1; p.hasWireID(wireID, call.ID); suffix++ {
		wireID = hashAlphanumeric9(fmt.Sprintf("%s#%d", candidate, suffix))
	}
	key := call.ID
	if key == "" {
		key = fmt.Sprintf("_anon_%d", len(p))
	}
	p[key] = wireID
	return wireID
}

func (p ToolCallPairs) hasWireID(wireID, hostID string) bool {
	for k, v := range p {
		if v == wireID && (hostID == "" || k != hostID) {
			return true
		}
	}
	return false
}

func (p ToolCallPairs) Take(result *api.ToolResult) (string, bool) {
	if result == nil || result.ToolCallID == "" {
		return "", false
	}
	id, ok := p[result.ToolCallID]
	delete(p, result.ToolCallID)
	return id, ok && id != ""
}
