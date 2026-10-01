package browser

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveRenderCanvasDefaultsToContentFit(t *testing.T) {
	c, err := ResolveRenderCanvas(0, 0, "", "", 0)
	testutil.FailErr(t, "resolve default render canvas", err)
	if c.Fit != FitContent {
		t.Fatalf("fit: got %q want %q", c.Fit, FitContent)
	}
	if c.Width != DefaultViewportWidth || c.Height != DefaultViewportHeight {
		t.Fatalf("defaults: got %dx%d want %dx%d",
			c.Width, c.Height, DefaultViewportWidth, DefaultViewportHeight)
	}
}

func TestResolveRenderCanvasPreset(t *testing.T) {
	c, err := ResolveRenderCanvas(0, 0, "phone", "", 0)
	testutil.FailErr(t, "ResolveRenderCanvas failed", err)
	if c.Width != 390 || c.Height != 844 {
		t.Fatalf("phone: got %dx%d want 390x844", c.Width, c.Height)
	}
}

func TestResolveRenderCanvasExplicitEdgesOverridePreset(t *testing.T) {
	c, err := ResolveRenderCanvas(1024, 0, "phone", "", 0)
	testutil.FailErr(t, "ResolveRenderCanvas failed", err)
	if c.Width != 1024 || c.Height != 844 {
		t.Fatalf("got %dx%d want 1024x844 (explicit width, preset height)", c.Width, c.Height)
	}
}

func TestResolveRenderCanvasExplicitScale(t *testing.T) {
	c, err := ResolveRenderCanvas(100, 100, "", "", 1.0)
	testutil.FailErr(t, "ResolveRenderCanvas failed", err)
	if c.Scale != 1.0 || !c.ExplicitScale {
		t.Fatalf("scale: got %v (explicit=%v) want 1.0 (explicit=true)", c.Scale, c.ExplicitScale)
	}
}

func TestResolveRenderCanvasUnknownPreset(t *testing.T) {
	_, err := ResolveRenderCanvas(0, 0, "watch", "", 0)
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "RENDER_KIT_UNKNOWN" {
		t.Fatalf("got %#v want RENDER_KIT_UNKNOWN", err)
	}
	if rej.Data["render_kit_kind"] != "viewport_preset" {
		t.Fatalf("kind: got %v want viewport_preset", rej.Data["render_kit_kind"])
	}
}

func TestResolveRenderCanvasUnknownFit(t *testing.T) {
	_, err := ResolveRenderCanvas(0, 0, "", "fullpage", 0)
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "RENDER_KIT_UNKNOWN" {
		t.Fatalf("got %#v want RENDER_KIT_UNKNOWN", err)
	}
	if rej.Data["render_kit_kind"] != "viewport_fit" {
		t.Fatalf("kind: got %v want viewport_fit", rej.Data["render_kit_kind"])
	}
}

// A canvas may be taller than a device frame, because a content fit grows into
// it. Width stays bounded by the device-frame edge.
func TestResolveRenderCanvasBounds(t *testing.T) {
	if _, err := ResolveRenderCanvas(1280, MaxRenderCanvasHeight, "", "", 0); err != nil {
		t.Fatalf("height at the canvas cap should resolve: %v", err)
	}
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"width over the frame edge", MaxViewportDim + 1, 720},
		{"height over the canvas cap", 1280, MaxRenderCanvasHeight + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveRenderCanvas(tc.w, tc.h, "", "", 0)
			rej := &browserengine.RejectError{}
			if !errors.As(err, &rej) || rej.Code != "RENDER_MARKUP_OVERSIZED" {
				t.Fatalf("got %#v want RENDER_MARKUP_OVERSIZED", err)
			}
		})
	}
}

func TestRasterScaleLandsOnPerceiveCeiling(t *testing.T) {
	for _, tc := range []struct {
		name      string
		w, h      int
		wantScale float64
	}{
		{"phone stays at the scale cap", 390, 844, MaxDeviceScaleFactor},
		{"desktop fills the ceiling", 1280, 720, 1.6},
		{"tall one-pager renders 1:1", 1280, 2048, 1},
		{"tallest canvas holds half scale", 1280, MaxRenderCanvasHeight, 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RasterScale(tc.w, tc.h)
			if got != tc.wantScale {
				t.Fatalf("RasterScale(%d,%d) = %v want %v", tc.w, tc.h, got, tc.wantScale)
			}
			longest := tc.w
			if tc.h > longest {
				longest = tc.h
			}
			if raster := float64(longest) * got; raster > float64(providerwire.MaxImageDimension) {
				t.Fatalf("raster edge %v exceeds perceive ceiling %d — image would be resampled",
					raster, providerwire.MaxImageDimension)
			}
		})
	}
}

func TestRenderCanvasCompleteness(t *testing.T) {
	full := RenderCanvas{Height: 900, ContentHeight: 900}
	if !full.Complete() || full.BelowFold() != 0 {
		t.Fatalf("exact fit: complete=%v belowFold=%d", full.Complete(), full.BelowFold())
	}
	cropped := RenderCanvas{Fit: FitViewport, Height: 720, ContentHeight: 3200}
	if cropped.Complete() {
		t.Fatal("a frame shorter than its document is not complete")
	}
	if got := cropped.BelowFold(); got != 2480 {
		t.Fatalf("belowFold: got %d want 2480", got)
	}
}
