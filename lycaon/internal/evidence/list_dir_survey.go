package evidence

import (
	"encoding/json"
	"strings"
)

// ListEvidenceSurvey decides survey-grade capture for a list_dir tool result.
func ListEvidenceSurvey(args map[string]any, content string) bool {
	if listArgsExpressedScope(args) {
		return false
	}
	var wire struct {
		View string `json:"view"`
	}
	if err := json.Unmarshal([]byte(stripToolHostSuffix(content)), &wire); err != nil {
		return ActiveBinding().IsSurveyKind("list")
	}
	return strings.TrimSpace(wire.View) != ""
}

func listArgsExpressedScope(args map[string]any) bool {
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["max_depth"]; ok {
		return true
	}
	if _, ok := args["max_entries"]; ok {
		return true
	}
	path := strings.TrimSpace(strings.ReplaceAll(stringArg(args, "path"), "\\", "/"))
	if path == "" || path == "." || path == "./" {
		return false
	}
	return true
}
