package llm

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Each transport must preserve the same partition even when history starts
// with a system-shaped observation rather than a user row.
func TestProviderStandingPrefixExcludesChangingHostContext(t *testing.T) {
	for _, beforeUser := range []bool{false, true} {
		messages := []api.Message{
			{Role: api.MessageRoleSystem, Content: "standing guidance", PromptCacheBreakpoint: api.PromptCacheTierStanding},
			{Role: api.MessageRoleUser, Content: "user request", PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleSystem, Content: "volatile host state"},
		}
		if beforeUser {
			messages = append(messages[:1], append([]api.Message{{Role: api.MessageRoleSystem, Content: "source brief"}}, messages[1:]...)...)
		}
		assertPartition := func(t *testing.T, system, conversation any) {
			t.Helper()
			sys, err := json.Marshal(system)
			testutil.FailErr(t, "encode standing projection", err)
			body, err := json.Marshal(conversation)
			testutil.FailErr(t, "encode conversation projection", err)
			if !bytes.Contains(sys, []byte("standing guidance")) || bytes.Contains(sys, []byte("volatile host state")) || bytes.Contains(sys, []byte("source brief")) {
				t.Fatalf("volatile context entered standing prefix: %s", sys)
			}
			userAt := bytes.Index(body, []byte("user request"))
			if !bytes.Contains(body, []byte("user request")) || bytes.Index(body, []byte("volatile host state")) <= userAt {
				t.Fatalf("tail lost conversation order: %s", body)
			}
			if beforeUser && (!bytes.Contains(body, []byte("source brief")) || bytes.Index(body, []byte("source brief")) >= userAt) {
				t.Fatalf("source brief lost conversation order: %s", body)
			}
		}
		t.Run("anthropic", func(t *testing.T) {
			system, conversation := anthropicprovider.ProjectMessages(messages, nil, false, "", "", "")
			assertPartition(t, system, conversation)
		})
		t.Run("bedrock", func(t *testing.T) {
			system, conversation := bedrock.ProjectMessages(messages, nil, false, "")
			assertPartition(t, system, conversation)
		})
		t.Run("vertex express", func(t *testing.T) {
			system, conversation := vertexexpress.ProjectMessages(messages, false, "")
			assertPartition(t, system, conversation)
		})
		t.Run("ollama", func(t *testing.T) {
			projected := ollamaprovider.ProjectMessages(messages, false, "")
			assertPartition(t, projected[0], projected[1:])
		})
		for name, profile := range map[string]providerprofile.Profile{
			"gemini": providerprofile.Gemini(), "vertex": providerprofile.Vertex(), "together": providerprofile.Together(),
			"lmstudio": providerprofile.Lmstudio(), "omlx": providerprofile.Omlx(), "litellm": providerprofile.Litellm(),
		} {
			t.Run(name, func(t *testing.T) {
				p := openaicompat.New(name, "http://localhost", "unused", nil).WithProfile(profile)
				body, err := p.Prepare(modelcall.CompletionRequest{Model: "fixture", Messages: messages}, false)
				testutil.FailErr(t, "prepare provider request", err)
				var wire openaicompat.Request
				testutil.FailErr(t, "decode provider request", json.Unmarshal(body, &wire))
				assertPartition(t, wire.Messages[0], wire.Messages[1:])
			})
		}
	}
}

func TestTemplateProvidersUseOneSystemSlot(t *testing.T) {
	messages := []api.Message{
		{Role: api.MessageRoleSystem, Content: "First."},
		{Role: api.MessageRoleSystem, Content: "Second."},
		{Role: api.MessageRoleUser, Content: "Continue."},
		{Role: api.MessageRoleAssistant, Content: "Working."},
		{Role: api.MessageRoleSystem, Content: "Worker returned."},
	}
	for name, profile := range map[string]providerprofile.Profile{
		"together": providerprofile.Together(), "gemini": providerprofile.Gemini(), "vertex": providerprofile.Vertex(),
		"lmstudio": providerprofile.Lmstudio(), "omlx": providerprofile.Omlx(), "litellm": providerprofile.Litellm(),
	} {
		t.Run(name, func(t *testing.T) {
			provider := openaicompat.New(name, "http://localhost", "unused", nil).WithProfile(profile)
			body, err := provider.Prepare(modelcall.CompletionRequest{Model: "fixture", Messages: messages}, false)
			testutil.FailErr(t, "encode template conversation", err)
			var wire openaicompat.Request
			testutil.FailErr(t, "decode template conversation", json.Unmarshal(body, &wire))
			if len(wire.Messages) != 4 || wire.Messages[0].Content != "First.\n\nSecond." || wire.Messages[3].Role != "user" || wire.Messages[3].Content != "Worker returned." {
				t.Fatalf("template conversation = %s", body)
			}
		})
	}
	ollama := ollamaprovider.ProjectMessages(messages, false, "")
	if len(ollama) != 4 || ollama[0].Role != "system" || ollama[0].Content != "First.\n\nSecond." || ollama[3].Role != "user" {
		t.Fatalf("native template conversation = %+v", ollama)
	}
}

func TestWorkflowWakeStartsConversationWithoutUserHistory(t *testing.T) {
	for _, kind := range []api.MessageKind{api.MessageKindHostLoopWake, api.MessageKindHostKick} {
		t.Run(string(kind), func(t *testing.T) {
			messages := transcript.Project([]api.Message{
				{Role: api.MessageRoleSystem, Content: "Application guidance."},
				{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: kind, Content: "Run the active phase."},
				{Role: api.MessageRoleSystem, Content: "Current workflow state."},
			})
			original, err := json.Marshal(messages)
			testutil.FailErr(t, "snapshot workflow request", err)
			projected := providerwire.SystemPreamble(messages)
			if len(projected) != 3 || projected[0].Role != api.MessageRoleSystem || projected[0].Content != messages[0].Content ||
				projected[1].Role != api.MessageRoleUser || projected[1].Content != messages[1].Content || projected[2].Role != api.MessageRoleUser {
				t.Fatalf("workflow request roles: %+v", projected)
			}
			provider := openaicompat.New("google", "http://localhost", "unused", nil).WithProfile(providerprofile.Gemini())
			body, err := provider.Prepare(modelcall.CompletionRequest{Model: "fixture", Messages: messages}, false)
			testutil.FailErr(t, "encode workflow wake", err)
			var wire openaicompat.Request
			testutil.FailErr(t, "decode workflow wake", json.Unmarshal(body, &wire))
			if len(wire.Messages) != 3 || wire.Messages[0].Role != "system" || wire.Messages[1].Role != "user" || wire.Messages[1].Content != messages[1].Content {
				t.Fatalf("workflow wire = %s", body)
			}
			system, conversation := anthropicprovider.ProjectMessages(messages, nil, false, "", "", "")
			if len(system) != 1 || system[0].Text != messages[0].Content || len(conversation) != 1 || conversation[0].Role != "user" || len(conversation[0].Content) != 2 {
				t.Fatalf("workflow request = %+v / %+v", system, conversation)
			}
			after, err := json.Marshal(messages)
			testutil.FailErr(t, "snapshot projected request", err)
			if string(original) != string(after) {
				t.Fatal("wire projection changed the host transcript")
			}
		})
	}
}
