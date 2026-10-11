package page

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/pkg/api"
)

// renderSVGView rasterizes the screened markup; redaction already rewrote it.
func renderSVGView(
	ctx context.Context,
	raster *browser.Rasterizer,
	tctx tools.ToolContext,
	svgBytes []byte,
	displayPath string,
	in viewImageArgs,
	view screened,
) (string, error) {
	if raster == nil {
		return "", errors.New("browser rasterizer unavailable for svg rendering")
	}
	scale := in.Scale
	if scale <= 0 {
		scale = 1.0
	}
	if scale < 0.1 || scale > 4.0 {
		return "", toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"reason": "invalid_scale",
			"scale":  scale,
			"min":    0.1,
			"max":    4.0,
		})
	}
	res := viewImageResult{
		Path:         displayPath,
		Format:       "svg",
		SizeBytes:    int64(len(svgBytes)),
		Transparent:  true,
		Text:         runeclamp.Clamp(view.text, 4000),
		Perception:   view.resultPerception(),
		ScreeningGap: view.gap,
	}
	if !view.perceive() {
		raw, _ := surveyjson.Marshal(res)
		return string(raw), nil
	}
	width, height := 0, 0
	if in.Viewport != nil {
		width = in.Viewport.Width
		height = in.Viewport.Height
	}
	out, err := raster.Rasterize(ctx, browser.RasterizeRequest{
		Markup:       string(view.bytes),
		Mime:         "svg",
		Width:        width,
		Height:       height,
		Scale:        scale,
		Theme:        "transparent",
		ProjectRoot:  tctx.ActiveRootPath(),
		CaptureScope: captureScope(tctx),
	})
	if err != nil {
		rej := &browserengine.RejectError{}
		if errors.As(err, &rej) {
			return "", &toolrejection.ToolReject{Code: rej.Code, Data: rej.Data}
		}
		return "", err
	}
	if tctx.Effects.Out == nil {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
	}
	caption, err := raster.ProjectCaption(ctx, captureScope(tctx), displayPath)
	if err != nil {
		caption = displayPath
	}
	tctx.Effects.Out.Visual = &tools.VisualCapture{
		Mime:      out.Mime,
		Bytes:     append([]byte(nil), out.Bytes...),
		Source:    api.VisualArtifactSourceRender,
		Caption:   caption,
		Perceive:  true,
		Projected: true,
	}
	res.Width, res.Height = out.Canvas.Width, out.Canvas.Height
	raw, _ := surveyjson.Marshal(res)
	return string(raw), nil
}

// renderRasterView reports the source dimensions and attaches the screened
// pixels normalized for the model.
func renderRasterView(tctx tools.ToolContext, rawBytes []byte, displayPath string, view screened) (string, error) {
	header, err := visualscreen.CheckRasterBounds(rawBytes)
	rejectData := map[string]any{"path": displayPath, "format": header.Format}
	if err != nil {
		return "", imageRejectFor(err, rejectData)
	}
	res := viewImageResult{
		Path:         displayPath,
		Format:       header.Format,
		Width:        header.Width,
		Height:       header.Height,
		SizeBytes:    int64(len(rawBytes)),
		Text:         runeclamp.Clamp(view.text, 4000),
		Perception:   view.resultPerception(),
		ScreeningGap: view.gap,
	}
	if view.perceive() {
		norm, err := providerwire.NormalizeImageBytes(view.bytes, view.mime, visual.MaxRasterBytes())
		if err != nil {
			return "", &toolrejection.ToolReject{Code: "IMAGE_CORRUPTED", Data: withImageFacts(rejectData, map[string]any{
				"rejection_reason": err.Error(),
			})}
		}
		if tctx.Effects.Out == nil {
			tctx.Effects.Out = &tools.ToolInvocationOut{}
		}
		tctx.Effects.Out.Visual = &tools.VisualCapture{
			Mime:      norm.Mime,
			Bytes:     norm.Bytes,
			Source:    api.VisualArtifactSourceWorkspace,
			Caption:   displayPath,
			Perceive:  true,
			Projected: true,
		}
	}
	raw, _ := surveyjson.Marshal(res)
	return string(raw), nil
}
