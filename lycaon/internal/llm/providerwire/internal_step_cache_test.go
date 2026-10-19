package providerwire

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInternalStepProjectionKeepsCacheIdentityStable(t *testing.T) {
	canonical := api.Message{ID: "step", Role: api.MessageRoleAssistant, Content: "Inspect the failing case.", ToolCalls: []api.ToolCall{{ID: "read-call", Name: "read", Args: map[string]any{"path": "test.go"}}}, ModelReasoning: &api.ModelReasoning{Text: "Check its assertion."}}
	lossy := canonical
	lossy.ModelReasoning = nil
	restored := api.RehydrateTranscriptProjectionFields(lossy, canonical)
	req := modelcall.CompletionRequest{Messages: transcript.Project([]api.Message{restored})}
	policy := providerprofile.PromptCachePolicy{}
	first := cacheRequestIdentity(req, policy)
	req.Messages = transcript.Project(req.Messages)
	if second := cacheRequestIdentity(req, policy); !first.valid || !reflect.DeepEqual(first, second) {
		t.Fatal("repeated note framing changed cache identity")
	}
	last := &req.Messages[len(req.Messages)-1]
	if !reflect.DeepEqual(last.ModelReasoning, canonical.ModelReasoning) {
		t.Fatal("history framing lost rehydrated reasoning")
	}
	last.ModelReasoning = &api.ModelReasoning{Text: "Check a different assertion."}
	if changed := cacheRequestIdentity(req, policy); reflect.DeepEqual(first.history, changed.history) {
		t.Fatal("reasoning change failed to invalidate history identity")
	}
	if canonical.Content != "Inspect the failing case." || canonical.ModelReasoning.Text != "Check its assertion." {
		t.Fatal("projection mutated canonical history")
	}
}
