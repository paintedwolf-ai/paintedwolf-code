package browser

import (
	"bytes"
	"context"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
	"image/color"
	"image/png"
	"testing"
)

func TestOverlayScaleReadsTheRasterRatio(t *testing.T) {
	for _, tc := range []struct {
		name        string
		raster, css int
		want        float64
	}{
		{"two device pixels per css pixel", 800, 400, 2},
		{"fractional ratio", 2048, 1280, 1.6},
		{"one to one", 400, 400, 1},
		{"unknown width falls back to 1:1", 0, 400, 1},
		{"unknown css width falls back to 1:1", 800, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := overlayScale(tc.raster, tc.css); got != tc.want {
				t.Fatalf("overlayScale(%d,%d) = %v want %v", tc.raster, tc.css, got, tc.want)
			}
		})
	}
}

// Annotation geometry scales from CSS pixels to device pixels.
func TestAnnotateMeasureOverlayLandsOnTheMeasuredBox(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)

	// A lone box at a known CSS rect, on white, with nothing else red.
	// Colors are %23-escaped: a bare # in a data: URL starts the fragment and
	// truncates the document.
	const cssW, cssH = 400, 300
	const boxLeft, boxTop, boxW, boxH = 100, 60, 160, 120
	markup := `data:text/html,<!doctype html><html><body style="margin:0;background:%23fff">` +
		`<div id="t" style="position:absolute;left:100px;top:60px;width:160px;height:120px;background:%23ddd"></div>` +
		`</body></html>`

	pool := NewPool("")
	defer pool.Close()
	page, err := pool.NewPage(context.Background(), cssW, cssH)
	testutil.FailErr(t, "pool.NewPage failed", err)
	defer func() { _ = page.Close() }()
	testutil.FailErr(t, "page.Navigate failed", page.Navigate(markup))
	_ = page.WaitLoad()

	elements, err := probeElements(page, []string{"#t"}, defaultMeasureStyleProps)
	testutil.FailErr(t, "probeElements failed", err)
	if len(elements) != 1 {
		t.Fatalf("probed %d elements, want 1", len(elements))
	}
	if got := elements[0].Rect; int(got.Left) != boxLeft || int(got.Top) != boxTop {
		t.Fatalf("measured rect %+v is not the CSS rect the test authored", got)
	}

	overlay, err := annotateMeasureOverlay(page, elements, cssW)
	testutil.FailErr(t, "annotateMeasureOverlay failed", err)
	img, err := png.Decode(bytes.NewReader(overlay.Bytes))
	testutil.FailErr(t, "png.Decode failed", err)

	scale := overlayScale(img.Bounds().Dx(), cssW)
	if scale <= 0 {
		t.Fatalf("overlay raster %v has no usable scale", img.Bounds())
	}
	// Sample each edge midpoint in device pixels.
	midX := int(float64(boxLeft+boxW/2) * scale)
	midY := int(float64(boxTop+boxH/2) * scale)
	for _, probe := range []struct {
		edge string
		x, y int
	}{
		{"top", midX, int(float64(boxTop) * scale)},
		{"bottom", midX, int(float64(boxTop+boxH)*scale) - 1},
		{"left", int(float64(boxLeft) * scale), midY},
		{"right", int(float64(boxLeft+boxW)*scale) - 1, midY},
	} {
		t.Run(probe.edge, func(t *testing.T) {
			if !isAnnotationRed(img.At(probe.x, probe.y)) {
				t.Fatalf("no annotation at %s edge (%d,%d) — overlay is off the measured box; scale %v, raster %v",
					probe.edge, probe.x, probe.y, scale, img.Bounds())
			}
		})
	}

	// The box interior stays unpainted: the outline is an outline.
	if isAnnotationRed(img.At(midX, midY)) {
		t.Fatalf("annotation filled the box interior at (%d,%d)", midX, midY)
	}
}

// isAnnotationRed identifies the overlay color.
func isAnnotationRed(c color.Color) bool {
	px := color.RGBAModel.Convert(c).(color.RGBA)
	return px.R > 200 && px.G < 120 && px.B < 120
}
