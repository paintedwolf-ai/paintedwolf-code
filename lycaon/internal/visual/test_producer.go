package visual

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const testProducerToolName = "emit_visual_fixture"

// testPNG1x1 is a minimal PNG for fixture producers and tests.
var testPNG1x1 = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
	0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

// TestPNG1x1Bytes returns a copy of the fixture 1×1 PNG.
func TestPNG1x1Bytes() []byte {
	return append([]byte(nil), testPNG1x1...)
}

type emitVisualFixtureArgs struct {
	Caption  string `json:"caption"`
	Perceive bool   `json:"perceive"`
}

// emitVisualFixtureResult is stored before artifact projection.
type emitVisualFixtureResult struct {
	Mime    string `json:"mime"`
	Caption string `json:"caption,omitempty"`
}

// RegisterTestProducer registers the visual fixture tool.
func RegisterTestProducer(reg tools.ToolRegistry) error {
	if reg == nil {
		return fmt.Errorf("emit_visual_fixture deps incomplete")
	}
	handler := func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		var in emitVisualFixtureArgs
		raw, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("encode visual fixture args: %w", err)
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", fmt.Errorf("decode visual fixture args: %w", err)
		}
		if tctx.Effects.Out == nil {
			tctx.Effects.Out = &tools.ToolInvocationOut{}
		}
		tctx.Effects.Out.Visual = &tools.VisualCapture{
			Mime:      "image/png",
			Bytes:     TestPNG1x1Bytes(),
			Source:    api.VisualArtifactSourceRender,
			Caption:   in.Caption,
			Perceive:  in.Perceive,
			Projected: true,
		}
		out, err := json.Marshal(emitVisualFixtureResult{
			Mime:    "image/png",
			Caption: in.Caption,
		})
		if err != nil {
			return "", fmt.Errorf("encode visual fixture result: %w", err)
		}
		return string(out), nil
	}
	return reg.RegisterDefinition(tools.Definition{
		Meta: tools.ToolMeta{
			Name: testProducerToolName, Description: "Emit a visual fixture.",
			ArgsSchema: map[string]any{"type": "object"},
		},
		Contract: toolcontract.Contract{
			Owner: "visual_artifacts", Reversibility: toolcontract.ReversibilityReversible,
			Batch: toolcontract.BatchSerial, Order: toolcontract.TurnOrderNormal,
			Lifecycle: toolcontract.LifecycleDBTransaction,
		},
		Handler: handler,
	})
}
