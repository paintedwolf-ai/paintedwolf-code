package llm

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveControlsOmitsEffortForStructuredThinkOff(t *testing.T) {
	modelinfo.SetThinkingRules([]modelinfo.ThinkingRule{{Match: []string{"gpt-oss"}, Style: string(modelinfo.ThinkStyleEffortLevels)}})
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })

	provider := openaicompat.New("fireworks", "http://localhost", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/gpt-oss-20b", MaxTokens: 4096},
	})
	req := modelcall.CompletionRequest{
		Model:          "accounts/fireworks/models/gpt-oss-20b",
		Think:          modelcall.ThinkOff,
		ResponseFormat: CurateResponseFormat(),
	}
	body, err := provider.Prepare(req, false)
	testutil.FailErr(t, "prepare structured thinking", err)
	var built openaicompat.Request
	testutil.FailErr(t, "decode structured thinking", json.Unmarshal(body, &built))
	if built.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q want omitted for utility json_schema", built.ReasoningEffort)
	}
	if built.Thinking != nil {
		t.Fatalf("thinking = %+v want nil", built.Thinking)
	}
}
