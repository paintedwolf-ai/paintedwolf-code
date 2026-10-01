package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildCompletionMessagesProjectsHostRedactionAsSystemAuthority(t *testing.T) {
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{})
	history := []api.Message{{
		ID: "tool-1", Role: api.MessageRoleTool, Content: "token=[REDACTED]",
		HostSecretRedaction: api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}}),
	}}
	out, err := engine.BuildCompletionMessages(context.Background(), &api.Session{ID: "s1"}, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}
	if len(out) != 2 || out[1].Role != api.MessageRoleSystem ||
		!strings.Contains(out[1].Content, transcript.ContentAuthorityNotice()) ||
		!strings.Contains(out[1].Content, llm.HostSecretRedactionNotice(true, false)) {
		t.Fatalf("messages = %+v", out)
	}
	// Host notices stay separate from projected tool data.
	if strings.Contains(out[0].Content, llm.HostSecretRedactionNotice(true, false)) ||
		strings.Contains(out[0].Content, transcript.ContentAuthorityNotice()) {
		t.Fatalf("host notice was written into tool/file content: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, history[0].Content) {
		t.Fatalf("tool content lost its original text: %q", out[0].Content)
	}
}
