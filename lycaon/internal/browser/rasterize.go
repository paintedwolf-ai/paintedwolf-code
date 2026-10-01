package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browser/designkit"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/visual"
)

// RasterizeRequest is offline markup rasterized to PNG bytes.
type RasterizeRequest struct {
	Markup         string
	Mime           string
	Width          int
	Height         int
	ViewportPreset string
	ViewportFit    string
	Scale          float64
	Theme          string
	Fonts          []string
	ProjectRoot    string
	CaptureScope   captureprojection.Scope
}

// RasterizeResult holds normalized PNG output, the canvas the document was
// actually laid out on, and the kit catalog for agents.
type RasterizeResult struct {
	Bytes   []byte
	Mime    string
	Canvas  RenderCanvas
	Catalog designkit.Catalog
	// Coverage reports exact text geometry coverage.
	Coverage *MaskCoverage
}

// Rasterizer headlessly rasterizes authored markup.
type Rasterizer struct {
	cacheDir  string
	budgets   RenderBudgets
	projector *captureprojection.Projector
}

// SetCaptureProjector installs the safe projection for rendered artifacts.
func (r *Rasterizer) SetCaptureProjector(projector *captureprojection.Projector) {
	if r != nil {
		r.projector = projector
	}
}

// ProjectCaption screens artifact metadata under the render's capture scope.
func (r *Rasterizer) ProjectCaption(ctx context.Context, scope captureprojection.Scope, caption string) (string, error) {
	if r == nil || r.projector == nil {
		return "", captureprojection.ErrUnavailable
	}
	projected, err := r.projector.Text(ctx, scope, "capture.render.caption", caption)
	return projected.Value, err
}

// NewRasterizer returns a rasterizer. cacheDir is the managed browser-cache
// root; budgets cap project asset bytes served into each render.
func NewRasterizer(cacheDir string, budgets RenderBudgets) *Rasterizer {
	return &Rasterizer{cacheDir: strings.TrimSpace(cacheDir), budgets: budgets}
}

// Rasterize validates markup, launches a sandboxed browser, and returns PNG bytes.
func (r *Rasterizer) Rasterize(ctx context.Context, req RasterizeRequest) (RasterizeResult, error) {
	if err := ValidateMarkup(req.Markup, req.Mime); err != nil {
		return RasterizeResult{}, err
	}
	if err := designkit.ValidateFonts(req.Fonts); err != nil {
		return RasterizeResult{}, browserengine.Reject("RENDER_KIT_UNKNOWN", map[string]any{
			"render_kit_kind":      "font",
			"detail":               err.Error(),
			"render_kit_available": designkit.FontFamilies(),
		})
	}
	if err := designkit.ValidateIconRefs(req.Markup); err != nil {
		ire := &designkit.IconRefError{}
		if errors.As(err, &ire) {
			return RasterizeResult{}, browserengine.Reject("RENDER_KIT_UNKNOWN", map[string]any{
				"render_kit_kind":        "icon",
				"unknown":                ire.Unknown,
				"render_kit_suggestions": ire.Suggestions,
				"naming":                 designkit.IconNamingGuide,
			})
		}
		return RasterizeResult{}, browserengine.Reject("RENDER_KIT_UNKNOWN", map[string]any{
			"render_kit_kind": "icon",
			"detail":          err.Error(),
		})
	}
	theme, err := designkit.NormalizeTheme(req.Theme)
	if err != nil {
		return RasterizeResult{}, browserengine.Reject("RENDER_KIT_UNKNOWN", map[string]any{
			"render_kit_kind":      "theme",
			"detail":               err.Error(),
			"render_kit_available": designkit.Themes(),
		})
	}
	canvas, err := ResolveRenderCanvas(req.Width, req.Height, req.ViewportPreset, req.ViewportFit, req.Scale)
	if err != nil {
		return RasterizeResult{}, err
	}

	brand, err := designkit.LoadBrandCSS(req.ProjectRoot)
	if err != nil {
		return RasterizeResult{}, browserengine.Reject("RENDER_MARKUP_INVALID", map[string]any{
			"reason": "brand_css",
			"detail": err.Error(),
		})
	}
	html, err := designkit.DocumentHTML(designkit.Options{
		Theme:    theme,
		BrandCSS: brand,
		BodyHTML: req.Markup,
	})
	if err != nil {
		return RasterizeResult{}, browserengine.Reject("RENDER_MARKUP_INVALID", map[string]any{
			"reason": "kit_document",
			"detail": err.Error(),
		})
	}

	b, cleanup, err := LaunchHeadless(ctx, LaunchOptions{
		CacheDir:  r.cacheDir,
		Roots:     nonEmptyRoots(req.ProjectRoot),
		DisableJS: true,
	})
	if err != nil {
		return RasterizeResult{}, err
	}
	defer cleanup()

	page, err := createPage(ctx, b)
	if err != nil {
		return RasterizeResult{}, cdpUnavailable("page_failed", err)
	}
	defer func() { _ = closePage(ctx, page) }()
	ctx, cancel := pageOperationContext(ctx, page, RasterizeTimeout)
	defer cancel()
	page = page.Context(ctx)

	assetMount, err := NewAssetMount(req.ProjectRoot, r.budgets)
	if err != nil {
		return RasterizeResult{}, err
	}
	mount, err := designkit.AttachMount(page, html, assetMount)
	if err != nil {
		return RasterizeResult{}, cdpUnavailable("designkit_mount", err)
	}
	defer func() { _ = mount.Close() }()

	if err := applyRenderCanvas(page, canvas); err != nil {
		return RasterizeResult{}, err
	}

	if err := page.Navigate(designkit.ViewURL()); err != nil {
		return RasterizeResult{}, cdpUnavailable("navigate_failed", err)
	}
	if err := page.WaitLoad(); err != nil {
		return RasterizeResult{}, cdpUnavailable("load_failed", err)
	}
	// Wait for inlined font faces to affect layout.
	_ = page.WaitStable(fontSettleDelay)

	// Reject referenced assets refused by the project fence.
	if rej := assetMount.FirstError(); rej != nil {
		return RasterizeResult{}, rej
	}

	// Extend content-fit captures without changing the layout frame.
	canvas, err = fitToContent(page, canvas)
	if err != nil {
		return RasterizeResult{}, err
	}

	regions := availablePageRegions(page)
	if theme == "transparent" {
		alpha := 0.0
		_ = (proto.EmulationSetDefaultBackgroundColorOverride{
			Color: &proto.DOMRGBA{R: 0, G: 0, B: 0, A: &alpha},
		}).Call(page)
	}
	raw, err := canvasPNG(page, canvas)
	if err != nil {
		return RasterizeResult{}, cdpUnavailable("screenshot_failed", err)
	}

	norm, err := providerwire.NormalizeImageBytes(raw, "image/png", visual.MaxRasterBytes())
	if err != nil {
		if int64(len(raw)) > visual.MaxRasterBytes().Int64() {
			return RasterizeResult{}, browserengine.Reject("RENDER_OUTPUT_OVERSIZED", map[string]any{
				"bytes":     len(raw),
				"max_bytes": visual.MaxRasterBytes().Int64(),
			})
		}
		return RasterizeResult{}, fmt.Errorf("normalize screenshot: %w", err)
	}
	safeBytes, meta, err := ProjectRasterRegions(
		ctx, r.projector, req.CaptureScope, regions, norm.Mime, norm.Bytes,
		CaptureRasterGeometry{Width: float64(canvas.Width), Height: float64(canvas.Height)},
	)
	if err != nil {
		return RasterizeResult{}, fmt.Errorf("screen rendered artifact: %w", err)
	}

	catalog := designkit.GetCatalog()
	catalog.Assets = assetMount.Summary()
	return RasterizeResult{
		Bytes:    safeBytes,
		Mime:     norm.Mime,
		Canvas:   canvas,
		Catalog:  catalog,
		Coverage: coverageOf(meta),
	}, nil
}

// applyRenderCanvas configures layout independently of the capture clip.
func applyRenderCanvas(page *rod.Page, canvas RenderCanvas) error {
	err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             canvas.Width,
		Height:            canvas.Frame,
		DeviceScaleFactor: 1,
		Mobile:            false,
	})
	if err != nil {
		return cdpUnavailable("viewport_failed", err)
	}
	return nil
}
