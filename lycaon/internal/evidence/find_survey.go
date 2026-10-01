package evidence

import (
	"encoding/json"
	"strings"
)

type findWireResponse struct {
	Results      []struct{} `json:"results"`
	Highlights   []struct{} `json:"highlights"`
	Distribution []struct{} `json:"distribution"`
	Truncated    bool       `json:"truncated"`
}

// FindEvidenceSurvey decides survey-grade capture for a find tool result.
func FindEvidenceSurvey(args map[string]any, content string) bool {
	if FindArgsExpressedScope(args) {
		return false
	}
	var wire findWireResponse
	if err := json.Unmarshal([]byte(stripToolHostSuffix(content)), &wire); err != nil {
		return ActiveBinding().IsSurveyKind("find")
	}
	if len(wire.Results) > 0 {
		return false
	}
	if len(wire.Highlights) > 0 {
		return true
	}
	return len(wire.Distribution) > 0 && wire.Truncated
}

// FindArgsExpressedScope reports whether arguments narrow the walk.
func FindArgsExpressedScope(args map[string]any) bool {
	if args == nil {
		return false
	}
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["max_results"]; ok {
		return true
	}
	if raw, ok := args["name_glob"].(string); ok && strings.TrimSpace(raw) != "" {
		return true
	}
	if raw, ok := args["path"].(string); ok {
		p := strings.TrimSpace(raw)
		if p != "" && p != "." {
			return true
		}
	}
	return false
}
