package llm_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func mustTinyPNG(t *testing.T) []byte {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "base64.StdEncoding.DecodeString failed", err)
	return png
}

func TestEncodeChatCompletionRequest_imageURLPart(t *testing.T) {
	provider := openaicompat.New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-4o", Capabilities: modelinfo.ModelCapabilities{
			Vision: modelinfo.CapabilityEvidence{State: modelinfo.CapabilitySupported},
		}},
	})
	png := mustTinyPNG(t)
	req := modelcall.CompletionRequest{
		Model: "gpt-4o",
		Messages: []api.Message{
			{Role: api.MessageRoleUser, Content: "check ui"},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call_1", Name: "capture_page", Args: map[string]any{}}}},
			{
				Role:    api.MessageRoleTool,
				Content: "console: clean",
				ToolResult: &api.ToolResult{
					ToolCallID: "call_1",
					Content:    "console: clean",
					Visual: &api.VisualArtifact{
						ID:       "art_1",
						Mime:     "image/png",
						Bytes:    png,
						Source:   api.VisualArtifactSourceCapture,
						Perceive: true,
					},
				},
			},
		},
	}
	body, err := provider.Prepare(req, false)
	testutil.FailErr(t, "encode request", err)
	if !strings.Contains(string(body), `"image_url"`) {
		t.Fatalf("expected image_url in wire body: %s", body)
	}
	if !strings.Contains(string(body), "data:image/png;base64,") {
		t.Fatalf("expected data url in wire body: %s", body)
	}
}

func TestPrepareMessagesForVision_nonVisionDropsImage(t *testing.T) {
	msgs := []api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Content: "snapshot",
			Visual: &api.VisualArtifact{
				Mime: "image/png", Bytes: []byte{1, 2, 3}, Perceive: true,
			},
		},
	}}
	out := providerwire.PrepareMessagesForVision(msgs, false, "sess-1")
	if out[0].ToolResult.Visual.Bytes != nil {
		t.Fatal("expected image bytes dropped on non-vision model")
	}
	if !strings.Contains(out[0].Content, `"reason":"model_lacks_vision"`) {
		t.Fatalf("content must state why the image was not attached: %q", out[0].Content)
	}
}

func TestMockProvider_imageEchoFixture(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{VisionModels: []string{"vision-fixture"}})
	raw := mustTinyPNG(t)
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Content: "ok",
			Visual:  &api.VisualArtifact{Mime: "image/png", Bytes: raw, Perceive: true},
		}},
	}
	completion, err := mock.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "vision-fixture",
		Messages: msgs,
	})
	testutil.FailErr(t, "mock complete", err)
	want := "image-echo:" + providerwire.PerceiveImageFixtureDigest(raw)
	if completion.Content != want {
		t.Fatalf("content = %q, want %q", completion.Content, want)
	}
}

func TestMockProvider_nonVisionStructuralOnly(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{})
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Content: "structural snapshot",
			Visual:  &api.VisualArtifact{Mime: "image/png", Bytes: mustTinyPNG(t), Perceive: true},
		}},
	}
	completion, err := mock.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "text-fixture",
		Messages: msgs,
	})
	testutil.FailErr(t, "mock complete", err)
	if strings.HasPrefix(completion.Content, "image-echo:") {
		t.Fatalf("non-vision model should not echo image: %q", completion.Content)
	}
}

func TestEstimateMessagesTokensWithVision_imageAccounting(t *testing.T) {
	msgs := compaction.ContextMessagesFromAPI([]api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{Visual: &api.VisualArtifact{
			ID: "art-1", Mime: "image/png", StoreRef: true, Perceive: true, Width: 2048, Height: 1152,
		}},
	}})
	want := providerwire.ImageTokenEstimate(2048, 1152)
	if got := compaction.EstimateMessagesTokensWithVision(msgs, true); got != want {
		t.Fatalf("tokens = %d, want %d for a stored capture", got, want)
	}
	if got := compaction.EstimateMessagesTokensWithVision(msgs, false); got != 0 {
		t.Fatalf("non-attach tokens = %d, want 0", got)
	}
}

func TestEstimateMessagesTokensWithVision_chargesOnlyTheWindow(t *testing.T) {
	window := providerwire.CurrentPerceptionWindow()
	total := window.MaxToolImages + 1
	msgs := make([]compaction.ContextMessage, total)
	for i := range msgs {
		msgs[i] = compaction.ContextMessage{ToolImageTokens: 100}
	}
	want := (total - window.Dropped(total)) * 100
	if got := compaction.EstimateMessagesTokensWithVision(msgs, true); got != want {
		t.Fatalf("tokens = %d, want %d: images outside the window cost nothing", got, want)
	}
}

func TestRecordingClientCapturesPerceiveMessages(t *testing.T) {
	stub := &stubVisionLLM{}
	rec := llm.NewRecordingClient(stub)
	provider := openaicompat.New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-4o", Capabilities: modelinfo.ModelCapabilities{
			Vision: modelinfo.CapabilityEvidence{State: modelinfo.CapabilitySupported},
		}},
	})
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "hi"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "capture_page"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			ToolCallID: "c1",
			Content:    "log",
			Visual:     &api.VisualArtifact{Mime: "image/png", Bytes: mustTinyPNG(t), Perceive: true},
		}},
	}
	_, err := rec.Complete(context.Background(), modelcall.CompletionRequest{Model: "gpt-4o", Messages: msgs})
	testutil.FailErr(t, "complete", err)
	if !stub.called {
		t.Fatal("expected stub client invoked")
	}
	body, err := provider.Prepare(rec.LastRequest(), false)
	testutil.FailErr(t, "encode recorded request", err)
	if !strings.Contains(string(body), `"image_url"`) {
		t.Fatalf("recording missed image wire: %s", body)
	}
}

type stubVisionLLM struct {
	called bool
}

func (s *stubVisionLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	s.called = true
	return &modelcall.Completion{Content: "ok"}, nil
}

func (s *stubVisionLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c, err := s.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: c.Content, Done: true}
	close(ch)
	return ch, nil
}
