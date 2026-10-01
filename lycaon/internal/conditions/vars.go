package conditions

import (
	"strconv"
	"strings"
)

// BoolVar reads a top-level var as a bool, accepting bool/string/number forms.
func BoolVar(vars map[string]any, key string) bool {
	if vars == nil {
		return false
	}
	v, ok := vars[key]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && t != "false" && t != "0"
	default:
		return v != nil
	}
}

func stringVar(vars map[string]any, key string) string {
	if vars == nil {
		return ""
	}
	v, ok := vars[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func nestedMap(vars map[string]any, key string) map[string]any {
	if vars == nil {
		return nil
	}
	raw, ok := vars[key]
	if !ok {
		return nil
	}
	m, _ := raw.(map[string]any)
	return m
}

func phaseBucketPending(vars map[string]any, bucket, phaseID string) bool {
	b := nestedMap(vars, bucket)
	if b == nil {
		return false
	}
	entry, ok := b[phaseID].(map[string]any)
	if !ok {
		return false
	}
	return BoolVar(entry, "pending")
}

func phaseBucketReceived(vars map[string]any, bucket, phaseID string) bool {
	b := nestedMap(vars, bucket)
	if b == nil {
		return false
	}
	entry, ok := b[phaseID].(map[string]any)
	if !ok {
		return false
	}
	if BoolVar(entry, "pending") {
		return false
	}
	switch bucket {
	case "user_feedback":
		return stringVar(entry, "response") != ""
	case "user_decision":
		return stringVar(entry, "choice") != ""
	default:
		return false
	}
}

func phaseBucketChoice(vars map[string]any, phaseID, want string) bool {
	b := nestedMap(vars, "user_decision")
	if b == nil {
		return false
	}
	entry, ok := b[phaseID].(map[string]any)
	if !ok {
		return false
	}
	want = strings.TrimSpace(want)
	if strings.EqualFold(stringVar(entry, "choice"), want) {
		return true
	}
	// Multi-choice stores individual selections; match membership.
	if raw, ok := entry["choices"].([]any); ok {
		for _, c := range raw {
			if s, ok := c.(string); ok && strings.EqualFold(strings.TrimSpace(s), want) {
				return true
			}
		}
	}
	if raw, ok := entry["choices"].([]string); ok {
		for _, c := range raw {
			if strings.EqualFold(strings.TrimSpace(c), want) {
				return true
			}
		}
	}
	return false
}

func phaseSkipped(vars map[string]any, phaseID string) bool {
	skipped := nestedMap(vars, "phase_skipped")
	if skipped != nil {
		if v, ok := skipped[phaseID]; ok {
			return BoolVar(map[string]any{"v": v}, "v")
		}
	}
	return false
}

func topologyStageComplete(vars map[string]any, stage string) bool {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return false
	}
	stages := nestedMap(vars, "topology_stages")
	if stages == nil {
		return false
	}
	switch v := stages[stage].(type) {
	case bool:
		return v
	case map[string]any:
		return BoolVar(v, "complete")
	default:
		return false
	}
}

func parallelStagesComplete(vars map[string]any, group []string) bool {
	if len(group) == 0 {
		return false
	}
	for _, stage := range group {
		if !topologyStageComplete(vars, stage) {
			return false
		}
	}
	return true
}

func suffixAfter(id, prefix string) string {
	if !strings.HasPrefix(id, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(id, prefix))
}

// SetDotPath writes a dotted path into nested scaffold vars (host writers only).
func SetDotPath(vars map[string]any, path string, value any) map[string]any {
	path = strings.TrimSpace(path)
	if path == "" {
		return vars
	}
	if vars == nil {
		vars = map[string]any{}
	}
	parts := strings.Split(path, ".")
	cur := vars
	for i := 0; i < len(parts)-1; i++ {
		key := parts[i]
		next, ok := cur[key].(map[string]any)
		if !ok || next == nil {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
	return vars
}

// DeleteDotPath removes a dotted path, leaving the maps above it in place.
func DeleteDotPath(vars map[string]any, path string) {
	path = strings.TrimSpace(path)
	if path == "" || vars == nil {
		return
	}
	parts := strings.Split(path, ".")
	cur := vars
	for i := 0; i < len(parts)-1; i++ {
		next, ok := cur[parts[i]].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

// DotPathGet returns the value at a dotted path.
func DotPathGet(vars map[string]any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" || vars == nil {
		return nil, false
	}
	cur := any(vars)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[part]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// DotPathEquals compares a dotted path to an expected string value.
func DotPathEquals(vars map[string]any, path, want string) bool {
	v, ok := DotPathGet(vars, path)
	if !ok {
		return false
	}
	return scalarEquals(v, want)
}

// DotPathTruthy reports whether a dotted path is truthy.
func DotPathTruthy(vars map[string]any, path string) bool {
	v, ok := DotPathGet(vars, path)
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && t != "false" && t != "0"
	default:
		return v != nil
	}
}

func scalarEquals(got any, want string) bool {
	want = strings.TrimSpace(want)
	switch t := got.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(t), want)
	case bool:
		if want == "true" {
			return t
		}
		if want == "false" {
			return !t
		}
		return strconv.FormatBool(t) == want
	case int:
		return strconv.Itoa(t) == want
	case int64:
		return strconv.FormatInt(t, 10) == want
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64) == want
	default:
		return false
	}
}

// ParseVarEquals splits var_equals:path,value into path and expected value.
func ParseVarEquals(id string) (path, value string, ok bool) {
	if !strings.HasPrefix(id, "var_equals:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, "var_equals:")
	comma := strings.Index(rest, ",")
	if comma <= 0 || comma >= len(rest)-1 {
		return "", "", false
	}
	return strings.TrimSpace(rest[:comma]), strings.TrimSpace(rest[comma+1:]), true
}

// ParseVarTruthy splits var_truthy:path into path.
func ParseVarTruthy(id string) (path string, ok bool) {
	if !strings.HasPrefix(id, "var_truthy:") {
		return "", false
	}
	path = strings.TrimSpace(strings.TrimPrefix(id, "var_truthy:"))
	return path, path != ""
}
