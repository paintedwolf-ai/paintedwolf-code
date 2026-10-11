package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"math"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/designkit"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const RenderViewToolName = "render_view"

type renderViewArgs struct {
	Markup     string          `json:"markup"`
	Mime       string          `json:"mime"`
	Caption    string          `json:"caption"`
	Dest       string          `json:"dest"`
	Handle     string          `json:"handle"`
	OldString  string          `json:"old_string"`
	NewString  string          `json:"new_string"`
	ReplaceAll bool            `json:"replace_all"`
	Theme      string          `json:"theme"`
	Fonts      []string        `json:"fonts"`
	Viewport   *renderViewport `json:"viewport"`
}

type renderViewport struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Preset string  `json:"preset"`
	Fit    string  `json:"fit"`
	Scale  float64 `json:"scale"`
}

type renderViewResult struct {
	Handle   string                `json:"handle,omitempty"`
	Revision int                   `json:"revision,omitempty"`
	Mime     string                `json:"mime"`
	Canvas   renderCanvas          `json:"canvas"`
	Caption  string                `json:"caption,omitempty"`
	Coverage *browser.MaskCoverage `json:"coverage,omitempty"`
	Theme    string                `json:"theme,omitempty"`
	Dest     string                `json:"dest,omitempty"`
	Kit      designkit.Catalog     `json:"kit"`
}

// renderCanvas states what the render holds. Result keys are emitted in
// alphabetical order, so one key sorting ahead of the bulky "kit" catalog keeps
// these in a compacted result.
type renderCanvas struct {
	Fit    string `json:"fit"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// FrameHeight is the emulated viewport vh units resolved against; it holds
	// at the requested height even when the capture runs past it.
	FrameHeight   int     `json:"frame_height"`
	ContentHeight int     `json:"content_height"`
	Complete      bool    `json:"complete"`
	BelowFold     int     `json:"below_fold,omitempty"`
	Scale         float64 `json:"scale"`
}

// DestWriter writes raster output to the project workspace.
type DestWriter func(ctx context.Context, tctx tools.ToolContext, relPath string, data []byte) error

type effectiveRenderInput struct {
	caption string
	markup  string
	mime    string
	theme   string
	fonts   []string
	width   int
	height  int
	preset  string
	fit     string
	scale   float64
	// base is the stored revision this render derives from; zero replaces.
	base int
}

func resolveEffectiveRender(in renderViewArgs, sessionID string, handleStore renderhandle.Store) (effectiveRenderInput, error) {
	handleID := in.Handle
	eff := effectiveRenderInput{
		caption: in.Caption,
		markup:  in.Markup,
		mime:    in.Mime,
		theme:   in.Theme,
		fonts:   in.Fonts,
	}
	if in.Viewport != nil {
		eff.width = in.Viewport.Width
		eff.height = in.Viewport.Height
		eff.preset = in.Viewport.Preset
		eff.fit = in.Viewport.Fit
		eff.scale = in.Viewport.Scale
	}

	if handleID != "" {
		if handleStore == nil {
			return eff, errors.New("render handle store unavailable")
		}
		if in.OldString != "" {
			patched, err := handleStore.Patch(sessionID, handleID, in.OldString, in.NewString, in.ReplaceAll)
			if err != nil {
				if errors.Is(err, renderhandle.ErrHandleNotFound) {
					return eff, &toolrejection.ToolReject{
						Code: "RENDER_HANDLE_NOT_FOUND",
						Data: map[string]any{"handle": handleID},
					}
				}
				if errors.Is(err, renderhandle.ErrPatchNotFound) {
					return eff, &toolrejection.ToolReject{
						Code: "RENDER_PATCH_NOT_FOUND",
						Data: map[string]any{"handle": handleID},
					}
				}
				if errors.Is(err, renderhandle.ErrPatchAmbiguous) {
					existing, _ := handleStore.Get(sessionID, handleID)
					matchCount := 0
					if existing != nil {
						matchCount = strings.Count(existing.Markup, in.OldString)
					}
					return eff, &toolrejection.ToolReject{
						Code: "RENDER_PATCH_AMBIGUOUS",
						Data: map[string]any{
							"handle":      handleID,
							"match_count": fmt.Sprintf("%d", matchCount),
						},
					}
				}
				if errors.Is(err, renderhandle.ErrPatchEmptyTarget) {
					return eff, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
						"reason": "empty_old_string",
					})
				}
				return eff, err
			}
			eff.markup = patched.Markup
			eff.mime = patched.Mime
			if eff.theme == "" {
				eff.theme = patched.Theme
			}
			if in.Viewport == nil && (patched.Viewport.Width > 0 || patched.Viewport.Height > 0 || patched.Viewport.Preset != "") {
				eff.width = patched.Viewport.Width
				eff.height = patched.Viewport.Height
				eff.preset = patched.Viewport.Preset
				eff.fit = patched.Viewport.Fit
				eff.scale = patched.Viewport.Scale
			}
			if len(eff.fonts) == 0 {
				eff.fonts = patched.Fonts
			}
			eff.base = patched.Revision
			if eff.caption == "" {
				eff.caption = patched.Caption
			}
		} else if eff.markup != "" {
			if eff.mime == "" {
				return eff, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "missing_mime"})
			}
		} else {
			existing, ok := handleStore.Get(sessionID, handleID)
			if !ok {
				return eff, &toolrejection.ToolReject{
					Code: "RENDER_HANDLE_NOT_FOUND",
					Data: map[string]any{"handle": handleID},
				}
			}
			eff.markup = existing.Markup
			eff.mime = existing.Mime
			if eff.theme == "" {
				eff.theme = existing.Theme
			}
			if in.Viewport == nil && (existing.Viewport.Width > 0 || existing.Viewport.Height > 0 || existing.Viewport.Preset != "") {
				eff.width = existing.Viewport.Width
				eff.height = existing.Viewport.Height
				eff.preset = existing.Viewport.Preset
				eff.fit = existing.Viewport.Fit
				eff.scale = existing.Viewport.Scale
			}
			if len(eff.fonts) == 0 {
				eff.fonts = existing.Fonts
			}
			eff.base = existing.Revision
			if eff.caption == "" {
				eff.caption = existing.Caption
			}
		}
	} else {
		if eff.markup == "" {
			return eff, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "missing_markup"})
		}
		if eff.mime == "" {
			return eff, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "missing_mime"})
		}
	}

	if eff.scale <= 0 && in.Dest != "" {
		eff.scale = 1.0
	}
	if eff.scale > 0 && (eff.scale < 0.1 || eff.scale > 4.0) {
		return eff, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"reason": "invalid_scale",
			"scale":  eff.scale,
			"min":    0.1,
			"max":    4.0,
		})
	}

	return eff, nil
}

// RenderViewHandler builds the render_view handler for authored mockup rasterization.
func RenderViewHandler(raster *browser.Rasterizer, destWriter DestWriter, handleStore renderhandle.Store) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseRenderViewArgs(args)
		if err != nil {
			return "", err
		}

		eff, err := resolveEffectiveRender(in, tctx.Identity.SessionID, handleStore)
		if err != nil {
			return "", err
		}

		out, err := raster.Rasterize(ctx, browser.RasterizeRequest{
			Markup:         eff.markup,
			Mime:           eff.mime,
			Width:          eff.width,
			Height:         eff.height,
			ViewportPreset: eff.preset,
			ViewportFit:    eff.fit,
			Scale:          eff.scale,
			Theme:          eff.theme,
			Fonts:          eff.fonts,
			ProjectRoot:    tctx.ActiveRootPath(),
			CaptureScope:   captureScope(tctx),
		})
		if err != nil {
			rej := &browserengine.RejectError{}
			if errors.As(err, &rej) {
				return "", &toolrejection.ToolReject{Code: rej.Code, Data: rej.Data}
			}
			return "", err
		}
		revision := 0
		if in.Handle != "" && handleStore != nil {
			stored, err := handleStore.Put(tctx.Identity.SessionID, &renderhandle.RenderHandle{
				ID:        in.Handle,
				SessionID: tctx.Identity.SessionID,
				Markup:    eff.markup,
				Mime:      eff.mime,
				Theme:     eff.theme,
				Viewport: renderhandle.ViewportConfig{
					Preset: eff.preset,
					Width:  eff.width,
					Height: eff.height,
					Fit:    eff.fit,
					Scale:  eff.scale,
				},
				Fonts:   eff.fonts,
				Caption: eff.caption,
				Bytes:   out.Bytes,
				Canvas:  out.Canvas,
			}, eff.base)
			if err != nil {
				if errors.Is(err, renderhandle.ErrHandleConflict) {
					return "", &toolrejection.ToolReject{Code: "RENDER_HANDLE_CONFLICT", Data: map[string]any{"handle": in.Handle}}
				}
				return "", err
			}
			revision = stored.Revision
		}
		if in.Dest != "" {
			if destWriter == nil {
				return "", errors.New("destination write unavailable")
			}
			if err := destWriter(ctx, tctx, in.Dest, out.Bytes); err != nil {
				return "", err
			}
		}

		if tctx.Effects.Out == nil {
			tctx.Effects.Out = &tools.ToolInvocationOut{}
		}
		caption, err := raster.ProjectCaption(ctx, captureScope(tctx), strings.TrimSpace(in.Caption))
		if err != nil {
			return "", err
		}
		tctx.Effects.Out.Visual = &tools.VisualCapture{
			Mime:      out.Mime,
			Bytes:     append([]byte(nil), out.Bytes...),
			Source:    api.VisualArtifactSourceRender,
			Caption:   caption,
			Perceive:  true,
			Projected: true,
		}
		theme, _ := designkit.NormalizeTheme(eff.theme)
		payload, _ := surveyjson.Marshal(renderViewResult{
			Handle:   in.Handle,
			Revision: revision,
			Mime:     out.Mime,
			Canvas:   canvasResult(out.Canvas),
			Caption:  caption,
			Coverage: out.Coverage,
			Theme:    theme,
			Dest:     in.Dest,
			Kit:      out.Catalog,
		})
		return string(payload), nil
	}
}

// canvasResult states the laid-out canvas, rounding scale to two places.
func canvasResult(c browser.RenderCanvas) renderCanvas {
	return renderCanvas{
		Fit:           c.Fit,
		Width:         c.Width,
		Height:        c.Height,
		FrameHeight:   c.Frame,
		ContentHeight: c.ContentHeight,
		Complete:      c.Complete(),
		BelowFold:     c.BelowFold(),
		Scale:         math.Round(c.Scale*100) / 100,
	}
}

func parseRenderViewArgs(args map[string]any) (renderViewArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return renderViewArgs{}, err
	}
	var in renderViewArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return renderViewArgs{}, err
	}
	in.Markup = strings.TrimSpace(in.Markup)
	in.Mime = strings.ToLower(strings.TrimSpace(in.Mime))
	in.Caption = strings.TrimSpace(in.Caption)
	in.Dest = strings.TrimSpace(in.Dest)
	in.Handle = strings.TrimSpace(in.Handle)
	if in.Dest != "" {
		ext := strings.ToLower(filepath.Ext(in.Dest))
		if ext != ".png" {
			return renderViewArgs{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
				"reason":   "invalid_dest_extension",
				"expected": ".png",
				"got":      ext,
			})
		}
	}
	in.Theme = strings.TrimSpace(in.Theme)
	if in.Viewport != nil {
		in.Viewport.Preset = strings.TrimSpace(in.Viewport.Preset)
		in.Viewport.Fit = strings.TrimSpace(in.Viewport.Fit)
	}
	if in.Handle == "" && in.Markup == "" {
		return renderViewArgs{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "missing_markup_or_handle"})
	}
	return in, nil
}
