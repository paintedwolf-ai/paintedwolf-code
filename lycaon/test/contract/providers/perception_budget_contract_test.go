package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProviderBudgetsFollowPerceptionSelection(t *testing.T) {
	var encoded bytes.Buffer
	contractcheck.FailErr(t, "encode image", png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	prior := providerwire.CurrentPerceptionWindow()
	providerwire.SetPerceptionWindow(providerwire.DefaultPerceptionWindow)
	t.Cleanup(func() { providerwire.SetPerceptionWindow(prior) })
	for _, driver := range []string{"openai", "anthropic", "vertex", "bedrock"} {
		for _, scenario := range []struct {
			name     string
			vision   bool
			bytes    []byte
			maxInput int
		}{
			{"perception window", true, encoded.Bytes(), 11000},
			{"no vision", false, encoded.Bytes(), 1500},
			{"missing bytes", true, nil, 1500},
		} {
			t.Run(driver+"/"+scenario.name, func(t *testing.T) {
				req := modelcall.CompletionRequest{Model: "model", MaxTokens: 512}
				for i := range 9 {
					req.Messages = append(req.Messages, api.Message{Role: api.MessageRoleTool, Content: "Captured",
						ToolResult: &api.ToolResult{Tool: "capture_page", ToolCallID: fmt.Sprintf("call-%d", i),
							Visual: &api.VisualArtifact{ID: fmt.Sprintf("image-%d", i), Mime: "image/png", Bytes: scenario.bytes, Perceive: true}},
					})
				}
				before, err := json.Marshal(req.Messages)
				contractcheck.FailErr(t, "clone canonical messages", err)
				budget := perceptionBudgetProvider(driver, scenario.vision).RequestBudget(t.Context(), req)
				if budget.Input > scenario.maxInput || budget.Input+budget.Output+budget.Headroom > budget.Window {
					t.Fatalf("non-dispatched images consumed context: %+v", budget)
				}
				after, err := json.Marshal(req.Messages)
				contractcheck.FailErr(t, "encode canonical messages", err)
				if !bytes.Equal(after, before) {
					t.Fatal("budget planning changed canonical messages")
				}
			})
		}
	}
}

func TestProviderBudgetsResolveUserImages(t *testing.T) {
	var encoded bytes.Buffer
	contractcheck.FailErr(t, "encode image", png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	providerwire.SetVisualBytesResolver(func(session, id string) ([]byte, string, bool) {
		return encoded.Bytes(), "image/png", session == "session" && id == "available"
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })
	for _, driver := range []string{"openai", "anthropic", "vertex", "bedrock"} {
		for _, vision := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/vision=%v", driver, vision), func(t *testing.T) {
				p := perceptionBudgetProvider(driver, vision)
				req := modelcall.CompletionRequest{Model: "model", MaxTokens: 512, Debug: modelcall.RequestDebug{SessionID: "session"},
					Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Describe", ArtifactIDs: []string{"available", "missing"}}}}
				got := p.RequestBudget(t.Context(), req)
				req.Messages[0].ArtifactIDs = nil
				base := p.RequestBudget(t.Context(), req)
				want := 0
				if vision {
					want = providerwire.UnsizedImageTokenEstimate
				}
				if got.Input-base.Input != want {
					t.Fatalf("user image charge = %d, want %d", got.Input-base.Input, want)
				}
			})
		}
	}
}

func perceptionBudgetProvider(driver string, vision bool) modelcall.RequestBudgetReporter {
	state := modelinfo.CapabilityUnsupported
	if vision {
		state = modelinfo.CapabilitySupported
	}
	models := []modelinfo.Entry{{ID: "model", ContextLength: 14000,
		Capabilities: modelinfo.ModelCapabilities{Vision: modelinfo.CapabilityEvidence{State: state}}}}
	switch driver {
	case "anthropic":
		return anthropic.New(driver, "https://example.invalid", "", models)
	case "vertex":
		return vertexexpress.New(driver, "https://example.invalid", "", models)
	case "bedrock":
		return bedrock.New(driver, "us-east-1", "", models)
	default:
		return openaicompat.New(driver, "https://example.invalid", "", models)
	}
}
