// Package toolkit provides shared argument parsing, survey envelopes, and path guards.
package toolkit

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
)

// BoundedInt is the effective value of an integer tool argument after defaults
// and min/max caps have been applied.
type BoundedInt struct {
	Effective int
	Requested *int // set when the caller supplied the key
	Clamped   bool // true when Requested was outside [min, max]
}

// BoundedIntArg reads an integer tool argument with bounds. JSON numbers arrive as
// float64; tests may pass int literals directly.
func BoundedIntArg(args map[string]any, key string, defaultVal, min, max int) BoundedInt {
	var requested *int
	effective := defaultVal
	switch raw := args[key].(type) {
	case float64:
		v := int(raw)
		requested = &v
		effective = v
	case int:
		requested = &raw
		effective = raw
	case int64:
		v := int(raw)
		requested = &v
		effective = v
	}
	clamped := false
	if requested != nil {
		if effective < min {
			effective = min
			clamped = true
		}
		if effective > max {
			effective = max
			clamped = true
		}
	}
	return BoundedInt{Effective: effective, Requested: requested, Clamped: clamped}
}

// ClampIntArg is BoundedIntArg for callers that only need the effective value.
func ClampIntArg(args map[string]any, key string, defaultVal, min, max int) int {
	return BoundedIntArg(args, key, defaultVal, min, max).Effective
}

// AppendClampBanner appends a "capped at N (requested M)" line when the caller's
// value was clamped, so the response tells the agent its argument was rewritten.
func AppendClampBanner(parts []string, label string, arg BoundedInt) []string {
	if !arg.Clamped || arg.Requested == nil {
		return parts
	}
	return append(parts, fmt.Sprintf("%s capped at %d (requested %d)", label, arg.Effective, *arg.Requested))
}

// BoolArg reads a boolean tool argument, falling back to defaultVal when the key
// is absent or not a JSON boolean.
func BoolArg(args map[string]any, key string, defaultVal bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return defaultVal
}

// ParseStringSliceArg reads a []string tool argument, trimming entries and
// capping the result at maxItems.
func ParseStringSliceArg(args map[string]any, key string, maxItems int) ([]string, error) {
	raw, ok := args[key].([]any)
	if !ok || len(raw) == 0 {
		return nil, MissingArg(key)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("invalid %q entry", key)
		}
		out = append(out, strings.TrimSpace(s))
	}
	if len(out) > maxItems {
		out = out[:maxItems]
	}
	return out, nil
}

// MissingArg is the fail-closed last line when a catalog schema should have
// rejected the call before the subsystem owner ran.
func MissingArg(key string) error {
	return toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
		"reason": fmt.Sprintf("missing %q argument", key),
		"field":  key,
	})
}

// PathEscapeReject is the shared rejection for a tool argument that resolves
// outside every attached root.
func PathEscapeReject(path string) error {
	return &toolrejection.ToolReject{
		Code: "SURVEY_PATH_ESCAPE",
		Data: map[string]any{"path": strings.TrimSpace(path)},
	}
}
