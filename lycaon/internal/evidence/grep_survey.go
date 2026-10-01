package evidence

import (
	"encoding/json"
)

type grepWireResponse struct {
	Matches      []struct{} `json:"matches"`
	Highlights   []struct{} `json:"highlights"`
	Distribution []struct{} `json:"distribution"`
	Truncated    bool       `json:"truncated"`
}

// GrepEvidenceSurvey decides survey-grade capture for a grep tool result.
func GrepEvidenceSurvey(args map[string]any, content string) bool {
	if grepArgsExpressedScope(args) {
		return false
	}
	var wire grepWireResponse
	if err := json.Unmarshal([]byte(stripToolHostSuffix(content)), &wire); err != nil {
		return ActiveBinding().IsSurveyKind("grep")
	}
	if len(wire.Matches) > 0 {
		return false
	}
	if len(wire.Highlights) > 0 {
		return true
	}
	return len(wire.Distribution) > 0 && wire.Truncated
}

func grepArgsExpressedScope(args map[string]any) bool {
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["max_matches"]; ok {
		return true
	}
	return false
}
