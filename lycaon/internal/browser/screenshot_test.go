package browser

import (
	"bytes"
	"context"
	"image"
	_ "image/png"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
)

// No canvas the host will accept may raster past the reject cap, at either
// bound: a device frame at the scale cap, or the tallest content canvas.
func TestRasterScale_fitsRejectCap(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"widest device frame", MaxViewportDim, MaxViewportDim},
		{"tallest content canvas", MaxViewportDim, MaxRenderCanvasHeight},
		{"smallest canvas", MinViewportDim, MinViewportDim},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scale := RasterScale(tc.w, tc.h)
			longest := tc.w
			if tc.h > longest {
				longest = tc.h
			}
			if edge := float64(longest) * scale; edge > float64(providerwire.RejectImageDimension) {
				t.Fatalf("CSS %dx%d × scale %v = %v exceeds RejectImageDimension %d",
					tc.w, tc.h, scale, edge, providerwire.RejectImageDimension)
			}
		})
	}
}

func TestScreenshotPNG_deviceScaleFactor(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := NewPool("")
	defer pool.Close()
	const cssW, cssH = 400, 300
	page, err := pool.NewPage(context.Background(), cssW, cssH)
	testutil.FailErr(t, "pool.NewPage failed", err)
	defer func() { _ = page.Close() }()
	if err := page.Navigate(`data:text/html,<!doctype html><html><body style="margin:0;background:#fff"></body></html>`); err != nil {
		testutil.FailErr(t, "page.Navigate failed", err)
	}
	_ = page.WaitLoad()
	raw, err := screenshotPNG(page)
	testutil.FailErr(t, "screenshotPNG failed", err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	testutil.FailErr(t, "image.DecodeConfig failed", err)
	scale := RasterScale(cssW, cssH)
	wantW := int(float64(cssW) * scale)
	wantH := int(float64(cssH) * scale)
	if cfg.Width != wantW || cfg.Height != wantH {
		t.Fatalf("png %dx%d want %dx%d (CSS %dx%d × scale %v)",
			cfg.Width, cfg.Height, wantW, wantH, cssW, cssH, scale)
	}
}

func TestScreenshotPNG_hidesScrollbars(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := NewPool("")
	defer pool.Close()
	page, err := pool.NewPage(context.Background(), 400, 300)
	testutil.FailErr(t, "pool.NewPage failed", err)
	defer func() { _ = page.Close() }()

	// Tall content forces a classic vertical scrollbar without the hide CSS.
	if err := page.Navigate(`data:text/html,<!doctype html><html><body style="margin:0;height:2000px;background:#fff"><div style="height:2000px">tall</div></body></html>`); err != nil {
		testutil.FailErr(t, "page.Navigate failed", err)
	}
	_ = page.WaitLoad()

	raw, err := screenshotPNG(page)
	testutil.FailErr(t, "screenshotPNG failed", err)
	if len(raw) < 100 {
		t.Fatalf("expected png bytes, got %d", len(raw))
	}
	res, err := page.Eval(`() => !!document.getElementById('lycaon-hide-scrollbars')`)
	testutil.FailErr(t, "page.Eval failed", err)
	if res == nil || !res.Value.Bool() {
		t.Fatal("expected hide-scrollbars style tag")
	}
	css, err := page.Eval(`() => document.getElementById('lycaon-hide-scrollbars').textContent`)
	testutil.FailErr(t, "page.Eval failed", err)
	if css == nil || !strings.Contains(css.Value.Str(), "::-webkit-scrollbar") {
		t.Fatalf("unexpected css: %#v", css)
	}
}
