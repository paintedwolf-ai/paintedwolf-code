package openaicompat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGeminiImportedAndSignedToolHistory(t *testing.T) {
	for _, signed := range []bool{false, true} {
		t.Run(map[bool]string{false: "imported", true: "signed parallel"}[signed], func(t *testing.T) {
			extra := map[string]any{"google": map[string]any{"other": "retain"}}
			if signed {
				extra["google"].(map[string]any)["thought_signature"] = "opaque-signature"
			}
			messages := []api.Message{
				{Role: api.MessageRoleUser, Content: "Inspect the files."},
				{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
					{ID: "first", Name: "read", Args: map[string]any{"path": "a"}, ExtraContent: extra},
					{ID: "second", Name: "read", Args: map[string]any{"path": "b"}},
				}},
				{Role: api.MessageRoleTool, Content: "a", ToolResult: &api.ToolResult{ToolCallID: "first"}},
				{Role: api.MessageRoleTool, Content: "b", ToolResult: &api.ToolResult{ToolCallID: "second"}},
			}
			before, err := json.Marshal(messages)
			testutil.FailErr(t, "snapshot input history", err)
			for _, profile := range []providerprofile.Profile{providerprofile.Gemini(), providerprofile.OpenAI()} {
				provider := New("test", "http://localhost", "unused", nil).WithProfile(profile)
				body, err := encodeChatCompletionRequest(modelcall.CompletionRequest{Model: "gemini-3.8-flash", Messages: messages}, provider, true, controlOpts{})
				testutil.FailErr(t, "encode replay", err)
				var request Request
				testutil.FailErr(t, "decode replay", json.Unmarshal(body, &request))
				calls := request.Messages[1].ToolCalls
				google := calls[0].ExtraContent["google"].(map[string]any)
				want := ""
				if signed {
					want = "opaque-signature"
				} else if profile.GoogleThoughtSignatures {
					want = googleImportedThoughtSignature
				}
				got, _ := google["thought_signature"].(string)
				if got != want || google["other"] != "retain" || len(calls[1].ExtraContent) != 0 {
					t.Fatalf("replay metadata = %+v, want first signature %q and unchanged parallel call", calls, want)
				}
				if request.Messages[2].ToolCallID != "first" || request.Messages[3].ToolCallID != "second" {
					t.Fatal("replay lost tool-result pairing")
				}
			}
			after, err := json.Marshal(messages)
			testutil.FailErr(t, "snapshot unchanged history", err)
			if string(before) != string(after) {
				t.Fatal("wire projection mutated durable history")
			}
		})
	}
}

func TestGeminiSystemEventsStayAfterAssistant(t *testing.T) {
	messages := []api.Message{
		{Role: api.MessageRoleSystem, Content: "Application guidance."},
		{Role: api.MessageRoleSystem, Content: "Tool guidance."},
		{Role: api.MessageRoleUser, Content: "Update the report."},
		{Role: api.MessageRoleAssistant, Content: "Report updated."},
		{Role: api.MessageRoleSystem, Content: "A verification result arrived."},
	}
	provider := New("google", "http://localhost", "unused", nil).WithProfile(providerprofile.Gemini())
	body, err := encodeChatCompletionRequest(modelcall.CompletionRequest{Model: "gemini-3.8-flash", Messages: messages}, provider, false, controlOpts{})
	testutil.FailErr(t, "encode host wake", err)
	var request Request
	testutil.FailErr(t, "decode host wake", json.Unmarshal(body, &request))
	var roles []string
	for i, message := range request.Messages {
		roles = append(roles, message.Role)
		expected := []string{"Application guidance.\n\nTool guidance.", "Update the report.", "Report updated.", "A verification result arrived."}
		if message.Content != expected[i] {
			t.Fatalf("message %d changed content", i)
		}
	}
	if !reflect.DeepEqual(roles, []string{"system", "user", "assistant", "user"}) {
		t.Fatalf("wire roles = %v", roles)
	}
}
