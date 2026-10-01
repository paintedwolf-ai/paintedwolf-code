package secretmatch

import (
	"strconv"
	"strings"
)

// HTTPArgumentConsumed names protocol data, excluding local paths and execution controls.
func HTTPArgumentConsumed(args map[string]any, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch parts[0] {
	case "url", "body_text", "body_json":
		return true
	case "headers", "query", "body_form":
		return len(parts) == 3 && (parts[2] == "name" || parts[2] == "value")
	case "auth":
		if len(parts) != 2 {
			return false
		}
		auth, _ := args["auth"].(map[string]any)
		scheme, _ := auth["scheme"].(string)
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "basic":
			return parts[1] == "username" || parts[1] == "password"
		case "bearer":
			return parts[1] == "token"
		default:
			return false
		}
	case "form":
		if len(parts) != 3 {
			return false
		}
		if parts[2] == "name" || parts[2] == "value" {
			return true
		}
		items, _ := args["form"].([]any)
		i, err := strconv.Atoi(parts[1])
		if err != nil || i < 0 || i >= len(items) {
			return false
		}
		part, _ := items[i].(map[string]any)
		_, file := part["path"]
		return file && (parts[2] == "filename" || parts[2] == "content_type")
	default:
		return false
	}
}
