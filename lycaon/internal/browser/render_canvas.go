package browser

import (
	"math"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browser/designkit"
	"github.com/lycaon/lycaon/internal/browserengine"
)

// Render fit modes select document or viewport height.
const (
	FitContent  = "content"
	FitViewport = "viewport"
)

// renderFits lists modes with the default first.
func renderFits() []string { return []string{FitContent, FitViewport} }

// RenderCanvas separates layout-frame and capture dimensions in CSS pixels.
type RenderCanvas struct {
	Fit           string
	Width         int
	Frame         int
	Height        int
	ContentHeight int
	Scale         float64
	ExplicitScale bool
}

// Complete reports whether the canvas shows the whole document.
func (c RenderCanvas) Complete() bool {
	return c.ContentHeight <= c.Height
}

// BelowFold is CSS pixels of document the canvas does not show.
func (c RenderCanvas) BelowFold() int {
	if c.Complete() {
		return 0
	}
	return c.ContentHeight - c.Height
}

// normalizeRenderFit resolves the requested fit, defaulting to FitContent.
func normalizeRenderFit(fit string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(fit)) {
	case "":
		return FitContent, nil
	case FitContent:
		return FitContent, nil
	case FitViewport:
		return FitViewport, nil
	default:
		return "", browserengine.Reject("RENDER_KIT_UNKNOWN", map[string]any{
			"render_kit_kind":      "viewport_fit",
			"name":                 fit,
			"render_kit_available": renderFits(),
		})
	}
}

// ResolveRenderCanvas applies the preset and bounds the layout frame.
func ResolveRenderCanvas(width, height int, preset, fit string, scale float64) (RenderCanvas, error) {
	mode, err := normalizeRenderFit(fit)
	if err != nil {
		return RenderCanvas{}, err
	}
	width, height, err = presetEdges(width, height, preset)
	if err != nil {
		return RenderCanvas{}, err
	}
	if width < MinViewportDim || height < MinViewportDim {
		return RenderCanvas{}, browserengine.Reject("RENDER_MARKUP_INVALID", map[string]any{"reason": "viewport_too_small"})
	}
	if width > MaxViewportDim || height > MaxRenderCanvasHeight {
		return RenderCanvas{}, browserengine.Reject("RENDER_MARKUP_OVERSIZED", map[string]any{
			"width":                    width,
			"height":                   height,
			"max_width":                MaxViewportDim,
			"max_height":               MaxRenderCanvasHeight,
			"reason":                   "viewport_exceeds_max_edge",
			"render_viewport_exceeded": true,
		})
	}
	scaleVal := RasterScale(width, height)
	explicit := false
	if scale > 0 {
		scaleVal = scale
		explicit = true
	}
	return RenderCanvas{
		Fit:           mode,
		Width:         width,
		Frame:         height,
		Height:        height,
		Scale:         scaleVal,
		ExplicitScale: explicit,
	}, nil
}

// presetEdges fills unset edges from a named preset, then from host defaults.
func presetEdges(width, height int, preset string) (int, int, error) {
	if preset = strings.TrimSpace(preset); preset != "" {
		p, ok := designkit.LookupViewportPreset(preset)
		if !ok {
			return 0, 0, browserengine.Reject("RENDER_KIT_UNKNOWN", map[string]any{
				"render_kit_kind":      "viewport_preset",
				"name":                 preset,
				"render_kit_available": designkit.ViewportPresetNames(),
			})
		}
		if width <= 0 {
			width = p.Width
		}
		if height <= 0 {
			height = p.Height
		}
	}
	if width <= 0 {
		width = DefaultViewportWidth
	}
	if height <= 0 {
		height = DefaultViewportHeight
	}
	return width, height, nil
}

// fitToContent measures the laid-out document and extends a FitContent capture
// to cover it. Only Height moves: growing Frame would restate every vh unit and
// grow the document again.
func fitToContent(page *rod.Page, canvas RenderCanvas) (RenderCanvas, error) {
	content, err := documentContentHeight(page)
	if err != nil {
		return RenderCanvas{}, err
	}
	canvas.ContentHeight = content
	if canvas.Fit != FitContent || content <= canvas.Height {
		return canvas, nil
	}
	if content > MaxRenderCanvasHeight {
		return RenderCanvas{}, browserengine.Reject("RENDER_CANVAS_OVERFLOW", map[string]any{
			"width":          canvas.Width,
			"content_height": content,
			"max_height":     MaxRenderCanvasHeight,
		})
	}
	canvas.Height = content
	if !canvas.ExplicitScale {
		canvas.Scale = RasterScale(canvas.Width, canvas.Height)
	}
	return canvas, nil
}

// documentContentHeight is the laid-out scrollable height in CSS pixels. It
// reads CDP layout metrics rather than page script because the raster sandbox
// runs with JavaScript disabled.
func documentContentHeight(page *rod.Page) (int, error) {
	metrics, err := proto.PageGetLayoutMetrics{}.Call(page)
	if err != nil {
		return 0, cdpUnavailable("layout_metrics_failed", err)
	}
	if metrics == nil || metrics.CSSContentSize == nil {
		return 0, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "layout_metrics_empty"})
	}
	height := int(math.Ceil(metrics.CSSContentSize.Height))
	if height < MinViewportDim {
		return MinViewportDim, nil
	}
	return height, nil
}
