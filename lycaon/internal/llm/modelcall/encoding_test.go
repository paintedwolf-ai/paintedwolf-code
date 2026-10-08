package modelcall

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

func TestInternalAttemptControlsStayOutOfEncodedRequests(t *testing.T) {
	request := CompletionRequest{
		Model: "fixture", MaxTokens: 73,
		Messages:     []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		StrictBudget: true, AttemptBudget: &CompletionBudget{Strict: true, MaxTokens: 91, CanReduceReasoning: true},
		ThinkingOverride:      &ThinkingOverride{Mode: "fixed", Effort: "high"},
		ThinkingOverrideStyle: modelinfo.ThinkStyleEffortLevels,
		ControlCapture:        &RequestControlCapture{},
	}
	chunk := StreamChunk{ResetReasoning: true, Content: "delta", Reasoning: "reason", Done: true}
	for _, codec := range []struct {
		name   string
		encode func(any) ([]byte, error)
		decode func([]byte, any) error
		key    func(string) string
	}{
		{"json", json.Marshal, json.Unmarshal, func(s string) string { return s }},
		{"yaml", yaml.Marshal, yaml.Unmarshal, strings.ToLower},
	} {
		t.Run(codec.name, func(t *testing.T) {
			for _, fixture := range []struct {
				name    string
				value   any
				keys    []string
				visible map[string]any
			}{
				{"request", request, []string{"Model", "Messages", "Tools", "ToolUse", "Debug", "Composition", "Think", "MaxTokens", "ResponseFormat"}, map[string]any{"Model": "fixture"}},
				{"chunk", chunk, []string{"Scripted", "Content", "ProviderID", "Model", "Fallback", "Reasoning", "ReasoningDetails", "ToolCalls", "Usage", "Done", "Progress", "Err"}, map[string]any{"Content": "delta", "Reasoning": "reason", "Done": true}},
				{"budget", *request.AttemptBudget, []string{}, nil},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					encoded, err := codec.encode(fixture.value)
					testutil.FailErr(t, "encode completion state", err)
					var fields map[string]any
					testutil.FailErr(t, "decode completion state", codec.decode(encoded, &fields))
					got := make([]string, 0, len(fields))
					for key := range fields {
						got = append(got, key)
					}
					want := make([]string, 0, len(fixture.keys))
					for _, key := range fixture.keys {
						want = append(want, codec.key(key))
					}
					slices.Sort(got)
					slices.Sort(want)
					if !slices.Equal(got, want) {
						t.Fatalf("encoded fields = %v, want original public fields %v", got, want)
					}
					for key, want := range fixture.visible {
						if got := fields[codec.key(key)]; !reflect.DeepEqual(got, want) {
							t.Fatalf("public field %s = %v, want %v", key, got, want)
						}
					}
				})
			}
		})
	}
}
