package tooloutput

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// FitWireJSON bounds the model view without changing outcome, identity, or pagination scalars.
func FitWireJSON(content string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content, false
	}
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return content, false
	}
	var original map[string]any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&original) != nil {
		return content, false
	}
	if out, fitted, declared := fitPageJSON(original, prefix, suffix, maxBytes); declared {
		if fitted {
			return out, true
		}
		return content, false
	}
	for _, textBytes := range []int{1024, 256, 64, 0} {
		for _, window := range []int{40, 20, 10, 5, 2, 1, 0} {
			projected := budgetObject(original, window, textBytes, 0)
			projected["wire_compacted"] = true
			raw, err := surveyjson.Marshal(projected)
			if err != nil {
				return content, false
			}
			out := prefix + string(raw) + suffix
			if len(out) <= maxBytes {
				return out, true
			}
		}
	}
	return content, false
}

// budgetObject keeps window items from each end of the arrays at depth,
// halving the window at every level below. A deep tree then gives way before
// the top-level lists that carry a result's outcome.
func budgetObject(original map[string]any, window, textBytes, depth int) map[string]any {
	out := make(map[string]any, len(original))
	for key, value := range original {
		out[key] = value
	}
	keep := window >> min(depth, 31)
	for key, value := range original {
		switch v := value.(type) {
		case []any:
			if key == "wire_spill_paths" {
				continue
			}
			kept := v
			if len(v) > keep*2 {
				kept = append(append([]any{}, v[:keep]...), v[len(v)-keep:]...)
				if _, exists := out[key+"_total"]; !exists {
					out[key+"_total"] = len(v)
				}
				out[key+"_truncated"] = true
				out[key+"_shown"] = len(kept)
			}
			items := make([]any, 0, len(kept))
			for _, item := range kept {
				if object, ok := item.(map[string]any); ok {
					item = budgetObject(object, window, textBytes, depth+1)
				}
				items = append(items, item)
			}
			out[key] = items
		case map[string]any:
			out[key] = budgetObject(v, window, textBytes, depth+1)
		case string:
			if isBodyField(key) && len(v) > textBytes {
				out[key] = runeclamp.CutBytes(v, textBytes)
				out[key+"_truncated"] = true
				if _, exists := out[key+"_bytes"]; !exists {
					out[key+"_bytes"] = len(v)
				}
			}
		}
	}
	return out
}

func isBodyField(key string) bool {
	switch key {
	case "diff", "content", "body", "text", "output", "stdout", "stderr", "message", "error", "description":
		return true
	default:
		return false
	}
}
