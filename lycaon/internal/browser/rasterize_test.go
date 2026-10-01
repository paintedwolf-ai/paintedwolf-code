package browser

import (
	"bytes"
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"image"
	_ "image/png"
	"path/filepath"
	"strconv"
	"testing"
)

func newTestRasterizer(t *testing.T) *Rasterizer {
	t.Helper()
	r := NewRasterizer("", testRenderBudgets(t))
	r.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	return r
}

func TestRasterizeInlineSVG(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	r := newTestRasterizer(t)
	out, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup: `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="80"><rect width="120" height="80" fill="#336699"/></svg>`,
		Mime:   "svg",
		Width:  200,
		Height: 120,
	})
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	if len(out.Bytes) == 0 {
		t.Fatal("expected png bytes")
	}
	if out.Mime != "image/png" {
		t.Fatalf("mime: got %q", out.Mime)
	}
}

func TestRasterizeProjectAssets(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	root := t.TempDir()
	writeTestPNG(t, filepath.Join(root, "assets", "logo.png"))
	writeTestFile(t, filepath.Join(root, "styles", "app.css"), ".hero{color:#123abc;font-size:32px}")
	r := newTestRasterizer(t)
	out, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup: `<link rel="stylesheet" href="http://lycaon.asset/styles/app.css">` +
			`<div class="hero"><img src="http://lycaon.asset/assets/logo.png" width="64" height="64" alt="logo"> Hello</div>`,
		Mime:        "html",
		Width:       320,
		Height:      200,
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatalf("Rasterize with project assets: %v", err)
	}
	if len(out.Bytes) == 0 {
		t.Fatal("expected png bytes")
	}
	sum := out.Catalog.Assets
	if sum == nil || sum.Count != 2 {
		t.Fatalf("kit.assets summary: %+v", sum)
	}
}

func TestRasterizeMissingAssetFailsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	r := newTestRasterizer(t)
	_, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup:      `<img src="http://lycaon.asset/assets/missing.png">`,
		Mime:        "html",
		ProjectRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected fail-fast reject for missing asset")
	}
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "RENDER_ASSET_NOT_FOUND" {
		t.Fatalf("got %#v want RENDER_ASSET_NOT_FOUND", err)
	}
}

// tallMarkup is a document of known CSS height with no vertical margins to
// collapse, so the measured content height is exactly the sum of its blocks.
func tallMarkup(height int) string {
	return `<div style="margin:0;padding:0;height:` +
		strconv.Itoa(height) + `px;background:#eee">tall</div>`
}

func TestRasterizeGrowsCanvasToContent(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	r := newTestRasterizer(t)
	const contentHeight = 2400
	out, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup: tallMarkup(contentHeight),
		Mime:   "html",
		Width:  1280,
		Height: 720,
	})
	testutil.FailErr(t, "Rasterize failed", err)
	if out.Canvas.Height < contentHeight {
		t.Fatalf("canvas height %d did not grow to content %d", out.Canvas.Height, contentHeight)
	}
	if !out.Canvas.Complete() || out.Canvas.BelowFold() != 0 {
		t.Fatalf("content fit must be complete: %+v", out.Canvas)
	}
	// Content height can exceed the requested minimum.
	if out.Canvas.Width != 1280 {
		t.Fatalf("width: got %d want 1280", out.Canvas.Width)
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(out.Bytes))
	testutil.FailErr(t, "image.DecodeConfig failed", derr)
	wantH := int(float64(out.Canvas.Height) * out.Canvas.Scale)
	if cfg.Height != wantH {
		t.Fatalf("png height %d want %d (canvas %d × scale %v)",
			cfg.Height, wantH, out.Canvas.Height, out.Canvas.Scale)
	}
}

func TestRasterizeTreatsRequestedHeightAsFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	r := newTestRasterizer(t)
	out, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup:         `<div style="height:80px">short</div>`,
		Mime:           "html",
		ViewportPreset: "desktop",
	})
	testutil.FailErr(t, "Rasterize failed", err)
	if out.Canvas.Height != 720 || out.Canvas.Width != 1280 {
		t.Fatalf("canvas: got %dx%d want 1280x720", out.Canvas.Width, out.Canvas.Height)
	}
}

func TestRasterizeViewportFitCropsAndStatesIt(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	r := newTestRasterizer(t)
	const contentHeight = 2400
	out, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup:      tallMarkup(contentHeight),
		Mime:        "html",
		Width:       1280,
		Height:      720,
		ViewportFit: FitViewport,
	})
	testutil.FailErr(t, "Rasterize failed", err)
	if out.Canvas.Height != 720 {
		t.Fatalf("viewport fit must hold its frame: got height %d", out.Canvas.Height)
	}
	if out.Canvas.Complete() {
		t.Fatal("a cropped render must not report itself complete")
	}
	if out.Canvas.ContentHeight < contentHeight {
		t.Fatalf("content_height %d must state the whole document (>= %d)",
			out.Canvas.ContentHeight, contentHeight)
	}
	if out.Canvas.BelowFold() == 0 {
		t.Fatal("below_fold must state how much was cropped")
	}
}

// A full-height hero means one screen, not one document: vh units resolve
// against the frame, which holds while the capture extends past it.
func TestRasterizeHoldsViewportUnitsToTheFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	r := newTestRasterizer(t)
	out, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup: `<div style="margin:0;min-height:100vh;background:#222"></div>` +
			`<div style="margin:0;height:1500px;background:#eee">below</div>`,
		Mime:   "html",
		Width:  1280,
		Height: 720,
	})
	testutil.FailErr(t, "Rasterize failed", err)
	if out.Canvas.Frame != 720 {
		t.Fatalf("frame moved to %d — vh units would have fed back into layout", out.Canvas.Frame)
	}
	// Hero (one 720 frame) + 1500 below. A frame that grew with the capture
	// would compound instead.
	if out.Canvas.ContentHeight != 2220 {
		t.Fatalf("content height %d want 2220 (100vh hero of %d + 1500)",
			out.Canvas.ContentHeight, out.Canvas.Frame)
	}
	if !out.Canvas.Complete() {
		t.Fatalf("content fit must cover the document: %+v", out.Canvas)
	}
}

func TestRasterizeRejectsCanvasOverflow(t *testing.T) {
	if testing.Short() {
		t.Skip("browser rasterize")
	}
	browsertest.SkipIfNoBrowser(t)
	r := newTestRasterizer(t)
	_, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup: tallMarkup(MaxRenderCanvasHeight + 500),
		Mime:   "html",
		Width:  1280,
	})
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "RENDER_CANVAS_OVERFLOW" {
		t.Fatalf("got %#v want RENDER_CANVAS_OVERFLOW", err)
	}
	if rej.Data["content_height"] == nil || rej.Data["max_height"] == nil {
		t.Fatalf("reject data must state the measurement and the cap: %v", rej.Data)
	}
}

func TestRasterizeRejectsForbiddenMarkup(t *testing.T) {
	r := newTestRasterizer(t)
	_, err := r.Rasterize(context.Background(), RasterizeRequest{
		Markup: `<html><script src="https://evil.test/x.js"></script></html>`,
		Mime:   "html",
	})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "RENDER_MARKUP_FORBIDDEN" {
		t.Fatalf("got %#v want RENDER_MARKUP_FORBIDDEN", err)
	}
}
