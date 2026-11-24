package browser

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
)

func TestRasterizeUnknownIconRejectsWithSerializableGuidance(t *testing.T) {
	r := NewRasterizer(t.TempDir(), RenderBudgets{})
	_, err := r.Rasterize(t.Context(), RasterizeRequest{
		Mime:   "svg",
		Markup: `<svg xmlns="http://www.w3.org/2000/svg"><use href="#kit-not-a-real-icon-zz"/></svg>`,
	})
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "RENDER_KIT_UNKNOWN" {
		t.Fatalf("got %#v want RENDER_KIT_UNKNOWN", err)
	}
	if naming, ok := rej.Data["naming"].(string); !ok || naming == "" {
		t.Fatalf("naming = %#v want the guide text", rej.Data["naming"])
	}
	unknown, ok := rej.Data["unknown"].([]string)
	if !ok || len(unknown) != 1 || unknown[0] != "not-a-real-icon-zz" {
		t.Fatalf("unknown = %#v", rej.Data["unknown"])
	}
	if _, err := json.Marshal(rej.Data); err != nil {
		t.Fatalf("rejection data must serialize for the tool result: %v", err)
	}
}
