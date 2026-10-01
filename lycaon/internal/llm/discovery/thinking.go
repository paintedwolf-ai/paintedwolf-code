package discovery

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

type openRouterThinking struct {
	SupportedEfforts  json.RawMessage `json:"supported_efforts"`
	DefaultEffort     string          `json:"default_effort"`
	Mandatory         bool            `json:"mandatory"`
	SupportsMaxTokens bool            `json:"supports_max_tokens"`
}

// capabilities reads declared reasoning metadata; no inference probe is needed.
func (r *openRouterThinking) capabilities(model string) *modelinfo.ThinkingCapabilities {
	if r == nil {
		return nil
	}
	c := &modelinfo.ThinkingCapabilities{State: "supported", CanDisable: !r.Mandatory, CanEnable: true}
	if string(r.SupportedEfforts) == "null" {
		c.Efforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	} else if len(r.SupportedEfforts) > 0 {
		var efforts []string
		if err := json.Unmarshal(r.SupportedEfforts, &efforts); err != nil {
			return &modelinfo.ThinkingCapabilities{State: "unknown"}
		}
		for _, effort := range efforts {
			if effort != "none" && effort != "off" {
				c.Efforts = append(c.Efforts, effort)
			}
		}
	}
	if r.DefaultEffort != "none" && r.DefaultEffort != "off" {
		c.DefaultEffort = r.DefaultEffort
	}
	// Effort defaults are not meaningful when the API omits effort selection.
	if len(c.Efforts) == 0 {
		c.DefaultEffort = ""
	}
	if r.SupportsMaxTokens {
		if rule, ok := modelinfo.MatchThinkingRule(model); ok && rule.Controls != nil && rule.Controls.Budget != nil {
			budget := *rule.Controls.Budget
			c.Budget = &budget
		}
	}
	if err := c.Validate(); err != nil {
		return &modelinfo.ThinkingCapabilities{State: "unknown"}
	}
	return c
}
