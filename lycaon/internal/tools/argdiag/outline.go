package argdiag

import (
	"sort"
	"strings"
)

// Outline renders a schema's members and nesting in one line: object
// members sorted, `?` after optional ones, and `[…]` around array items. A
// first-level member's enum shows as `a|b`; deeper enums, types, and
// descriptions stay in the schema.
func Outline(schema map[string]any) string {
	return outlineNode(schema, 0)
}

func outlineNode(schema map[string]any, depth int) string {
	if schema == nil {
		return ""
	}
	if props := declaredProperties(schema); len(props) > 0 {
		required := map[string]bool{}
		if list, ok := schema["required"].([]any); ok {
			for _, v := range list {
				if name, ok := v.(string); ok {
					required[name] = true
				}
			}
		} else if list, ok := schema["required"].([]string); ok {
			for _, name := range list {
				required[name] = true
			}
		}
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			part := name
			if !required[name] {
				part += "?"
			}
			if child := outlineNode(props[name], depth+1); child != "" {
				part += ": " + child
			}
			parts = append(parts, part)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	if items, ok := schema["items"].(map[string]any); ok {
		return "[" + outlineNode(items, depth) + "]"
	}
	if t, _ := schema["type"].(string); t == "array" {
		return "[]"
	}
	if enum := outlineEnum(schema["enum"]); depth == 1 && len(enum) > 0 {
		return strings.Join(enum, "|")
	}
	return ""
}

func outlineEnum(raw any) []string {
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, v := range values {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
