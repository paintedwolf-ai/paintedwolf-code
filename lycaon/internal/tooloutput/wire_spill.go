package tooloutput

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// InjectWireSpillPath sets wire_spill_path on the JSON object body while preserving
// handle prefix and host banner suffix. Non-allowlisted paths are ignored so abs
// host leaks cannot re-enter the wire.
func InjectWireSpillPath(content, relPath string) string {
	relPath = strings.TrimSpace(relPath)
	if !IsAgentWireSpillRel(relPath) {
		return content
	}
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return content
	}
	var obj map[string]any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&obj); err != nil {
		return content
	}
	if retained := SpillPaths(content); len(retained) > 0 {
		obj["wire_spill_paths"] = retained
	}
	obj["wire_spill_path"] = relPath
	out, err := surveyjson.Marshal(obj)
	if err != nil {
		return content
	}
	return prefix + string(out) + suffix
}
