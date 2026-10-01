package bindings

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// getPointer resolves an RFC 6901 JSON Pointer against a decoded JSON value.
// pointer must be non-empty and start with '/'.
func getPointer(doc any, pointer string) (any, error) {
	if pointer == "" {
		return nil, fmt.Errorf("empty JSON Pointer")
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("JSON Pointer must start with /: %q", pointer)
	}
	if pointer == "/" {
		return doc, nil
	}
	cur := doc
	for _, raw := range strings.Split(pointer[1:], "/") {
		part := unescapePointerToken(raw)
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, fmt.Errorf("pointer %q: missing key %q", pointer, part)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("pointer %q: array index %q: %w", pointer, part, err)
			}
			if idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("pointer %q: index %d out of range", pointer, idx)
			}
			cur = node[idx]
		default:
			return nil, fmt.Errorf("pointer %q: cannot traverse %T", pointer, cur)
		}
	}
	return cur, nil
}

func unescapePointerToken(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	s = strings.ReplaceAll(s, "~0", "~")
	return s
}

func coerceField(spec FieldSpec, raw any) (any, error) {
	if spec.Equals != "" {
		if spec.Type != TypeBool {
			return nil, fmt.Errorf("equals only allowed on bool fields")
		}
		s, ok := rawAsString(raw)
		if !ok {
			return false, nil
		}
		return s == spec.Equals, nil
	}
	switch spec.Type {
	case TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("want bool, got %T", raw)
		}
		return b, nil
	case TypeString:
		s, ok := rawAsString(raw)
		if !ok {
			return nil, fmt.Errorf("want string, got %T", raw)
		}
		return s, nil
	case TypeInt:
		n, ok := rawAsInt(raw)
		if !ok {
			return nil, fmt.Errorf("want int, got %T", raw)
		}
		return n, nil
	default:
		return nil, fmt.Errorf("unknown type %q", spec.Type)
	}
}

func rawAsString(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

func rawAsInt(raw any) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
