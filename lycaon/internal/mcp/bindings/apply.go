package bindings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Apply runs matching bindings against MCP result text.
// Invalid JSON or no successful schema match returns (false, nil, nil).
// A host-level error is returned only for extract conflicts that violate load uniqueness.
func Apply(list []Binding, providerID, toolName, resultText string) (matched bool, fields FieldStore, err error) {
	if providerID == "" || toolName == "" {
		return false, nil, nil
	}
	var candidates []Binding
	for _, b := range list {
		if b.ProviderID == providerID && b.ToolName == toolName {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return false, nil, nil
	}
	obj, ok := parseJSONObject(resultText)
	if !ok {
		return false, nil, nil
	}
	out := FieldStore{}
	anyOK := false
	for _, b := range candidates {
		sch := b.schema()
		if sch == nil {
			compiled, cerr := compileSchema(b.ID, b.Schema)
			if cerr != nil {
				continue
			}
			sch = compiled
		}
		if err := validateInstance(sch, obj); err != nil {
			continue
		}
		anyOK = true
		for _, f := range b.Fields {
			raw, perr := getPointer(obj, f.Path)
			if perr != nil {
				continue
			}
			val, cerr := coerceField(f, raw)
			if cerr != nil {
				continue
			}
			if _, exists := out[f.Key]; exists {
				return false, nil, fmt.Errorf("duplicate projected key %q from binding %q", f.Key, b.ID)
			}
			out[f.Key] = val
		}
	}
	if !anyOK {
		return false, nil, nil
	}
	return true, out, nil
}

func parseJSONObject(text string) (map[string]any, bool) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '{' {
		return nil, false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return nil, false
	}
	return obj, true
}
