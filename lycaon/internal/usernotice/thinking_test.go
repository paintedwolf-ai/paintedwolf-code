package usernotice

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestThinkingOverrideNoticePreservesIdentity(t *testing.T) {
	failure := &llm.ThinkingOverrideError{ProviderID: "provider", Model: "model", Reason: "effort is unavailable"}
	ctx := ContextFromPromptError(fmt.Errorf("dispatch: %w", failure))
	if ctx["provider_id"] != "provider" || ctx["model"] != "model" || ctx["reason"] != failure.Reason {
		t.Fatalf("missing override context: %+v", ctx)
	}
	if failure.NoticeCode() != wire.NoticeCodeThinkingOverrideUnavailable {
		t.Fatal("thinking override used a generic notice")
	}
}
