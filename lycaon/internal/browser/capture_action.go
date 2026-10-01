package browser

import (
	"encoding/json"
	"fmt"
	"strings"
)

func driverOK(res json.RawMessage) bool {
	var parsed map[string]any
	if json.Unmarshal(res, &parsed) != nil {
		return true
	}
	if v, has := parsed["ok"]; has {
		if b, isBool := v.(bool); isBool {
			return b
		}
	}
	return true
}

func actionFailedData(index int, act CaptureAction, res json.RawMessage, runErr error) map[string]any {
	data := map[string]any{
		"index": index,
		"type":  act.Type,
	}
	if locs := locatorSummary(act); locs != "" {
		locators, clipped := truncateUTF8Bytes(locs, 256)
		data["locators"] = locators
		data["locators_truncated"] = clipped
	}
	if runErr != nil {
		data["error"] = runErr.Error()
	}
	var parsed map[string]any
	if len(res) > 0 && json.Unmarshal(res, &parsed) == nil {
		if errStr, ok := parsed["error"].(string); ok && strings.TrimSpace(errStr) != "" {
			data["error"] = errStr
		}
		if state, ok := parsed["state"].(map[string]any); ok {
			controls, truncated := actionControls(state["interactive"])
			if len(controls) > 0 {
				data["interactive"] = formatInteractive(controls)
				data["interactive_controls"] = controls
			}
			upstreamTruncated, _ := state["interactive_truncated"].(bool)
			data["interactive_truncated"] = truncated || upstreamTruncated
		}
	}
	return data
}

func locatorSummary(act CaptureAction) string {
	var parts []string
	if s := strings.TrimSpace(act.Testid); s != "" {
		parts = append(parts, "testid="+s)
	}
	if s := strings.TrimSpace(act.Selector); s != "" {
		parts = append(parts, "selector="+s)
	}
	if s := strings.TrimSpace(act.Role); s != "" {
		parts = append(parts, "role="+s)
	}
	if s := strings.TrimSpace(act.Label); s != "" {
		parts = append(parts, fmt.Sprintf("label=%q", s))
	}
	if s := strings.TrimSpace(act.Text); s != "" {
		parts = append(parts, fmt.Sprintf("text=%q", s))
	}
	return strings.Join(parts, " ")
}

type actionControl struct {
	Tag      string `json:"tag"`
	Role     string `json:"role,omitempty"`
	Testid   string `json:"testid,omitempty"`
	Name     string `json:"name,omitempty"`
	Text     string `json:"text,omitempty"`
	Disabled *bool  `json:"disabled,omitempty"`
}

func actionControls(raw any) ([]actionControl, bool) {
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	controls := make([]actionControl, 0, 12)
	truncated := false
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := m["tag"].(string)
		if tag == "" {
			continue
		}
		if len(controls) == 12 {
			return controls, true
		}
		read := func(key string, limit int) string {
			value, _ := m[key].(string)
			value, clipped := truncateUTF8Bytes(value, limit)
			truncated = truncated || clipped
			return value
		}
		control := actionControl{
			Tag: read("tag", 32), Role: read("role", 32), Testid: read("testid", 160),
			Name: read("name", 160), Text: read("text", 160),
		}
		if disabled, ok := m["disabled"].(bool); ok {
			control.Disabled = &disabled
		}
		controls = append(controls, control)
	}
	return controls, truncated
}

func formatInteractive(controls []actionControl) string {
	parts := make([]string, 0, len(controls))
	for _, control := range controls {
		label := control.Name
		if label == "" {
			label = control.Text
		}
		part := control.Role
		if part == "" {
			part = control.Tag
		}
		if label != "" {
			part += fmt.Sprintf(" %q", label)
		}
		if control.Text != "" && control.Text != label {
			part += fmt.Sprintf(" (text %q)", control.Text)
		}
		if control.Disabled != nil && *control.Disabled {
			part += " (disabled)"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}
