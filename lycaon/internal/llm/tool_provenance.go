package llm

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

type ToolReasoningPolicy struct {
	Intent    string `json:"intent"`
	Style     string `json:"style"`
	AlwaysOn  bool   `json:"always_on"`
	DefaultOn bool   `json:"default_on"`
}

func toolDriverIdentity(profile providerprofile.Profile) (string, error) {
	data, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func toolReasoningPolicy(profile providerprofile.Profile, model modelinfo.Entry) ToolReasoningPolicy {
	thinking := modelcall.ResolveModelThinking(profile, model, model.ID)
	intent := "medium"
	switch modelcall.ResolveThinkLevel(modelcall.CompletionRequest{}, model.ReasoningEffort, false) {
	case modelcall.ThinkOff:
		intent = "off"
	case modelcall.ThinkLow:
		intent = "low"
	case modelcall.ThinkHigh:
		intent = "high"
	case modelcall.ThinkUnset, modelcall.ThinkMedium:
	}
	return ToolReasoningPolicy{Intent: intent, Style: string(thinking.Style), AlwaysOn: thinking.AlwaysOn, DefaultOn: thinking.DefaultOn}
}

func toolRequestIdentity(profile providerprofile.Profile, model modelinfo.Entry) (string, error) {
	data, err := json.Marshal(struct {
		Context     int
		Output      int
		Temperature *float64
		Effort      string
		Style       string
		Thinking    modelcall.ModelThinking
	}{model.ContextLength, model.MaxTokens, model.Temperature, model.ReasoningEffort, model.ThinkStyle, modelcall.ResolveModelThinking(profile, model, model.ID)})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
