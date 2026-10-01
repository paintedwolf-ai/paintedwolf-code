// Package jsonvalue handles detached JSON-compatible values.
package jsonvalue

// CloneMap returns a detached JSON object.
func CloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = Clone(item)
	}
	return out
}

// Clone returns a detached JSON-compatible value.
func Clone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = Clone(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
