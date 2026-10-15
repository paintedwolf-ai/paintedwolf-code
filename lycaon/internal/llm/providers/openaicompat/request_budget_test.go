package openaicompat

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRequestBudgetUsesDispatchedToolImages(t *testing.T) {
	p := New("vision", "https://example.invalid", "", []modelinfo.Entry{{
		ID: "model", ContextLength: 14000,
		Capabilities: modelinfo.ModelCapabilities{Vision: modelinfo.CapabilityEvidence{State: modelinfo.CapabilitySupported}},
	}})
	rows := []api.Message{{Role: api.MessageRoleUser, Content: "Compare the captured images"}}
	var calls []api.ToolCall
	for i := range 9 {
		calls = append(calls, api.ToolCall{ID: fmt.Sprintf("call-%d", i), Name: "capture_page"})
	}
	rows = append(rows, api.Message{Role: api.MessageRoleAssistant, ToolCalls: calls})
	for i := range 9 {
		rows = append(rows, api.Message{Role: api.MessageRoleTool, Content: "Captured",
			ToolResult: &api.ToolResult{Tool: "capture_page", ToolCallID: calls[i].ID, Content: "Captured",
				Visual: &api.VisualArtifact{ID: fmt.Sprintf("image-%d", i), Mime: "image/png", Bytes: toolImagePNG(t), Perceive: true}},
		})
	}
	req := modelcall.CompletionRequest{Model: "model", MaxTokens: 512, Messages: rows}
	prepared := req
	prepared.Messages = providerwire.PrepareMessagesForVision(rows, true, "")
	want := p.Catalog.RequestBudget(t.Context(), prepared)
	got := p.RequestBudget(t.Context(), req)
	if got.Input != want.Input {
		t.Fatalf("planned input = %d, dispatched estimate = %d", got.Input, want.Input)
	}
	if got.Input+got.Output+got.Headroom > got.Window {
		t.Fatalf("dispatched images should fit: %+v", got)
	}
}

func TestRequestBudgetUsesNativeReasoningReservation(t *testing.T) {
	p := New("fireworks", "https://example.invalid", "", []modelinfo.Entry{{
		ID: "accounts/fireworks/models/glm-5p3-flash", ContextLength: 32768,
	}}).WithProfile(providerprofile.Fireworks())
	req := modelcall.CompletionRequest{Model: "accounts/fireworks/models/glm-5p3-flash"}
	want := p.resolveRequestControls(req, req.Model, controlOpts{}).MaxTokens
	if want <= modelcall.OrchestrationMaxTokens {
		t.Fatalf("fixture did not reserve native reasoning: %d", want)
	}
	if got := p.RequestBudget(t.Context(), req).Output; got != want {
		t.Fatalf("planned output = %d, native output = %d", got, want)
	}
}
