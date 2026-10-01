package browser

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/go-rod/rod"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/visual"
)

type annotateOverlay struct {
	Mime    string
	Bytes   []byte
	regions PageRegions
}

// annotateMeasureOverlay draws CSS-space boxes and a ruler.
func annotateMeasureOverlay(page *rod.Page, elements []MeasuredElement, viewportWidth int) (annotateOverlay, error) {
	regions := availablePageRegions(page)
	raw, err := screenshotPNG(page)
	if err != nil {
		return annotateOverlay{}, fmt.Errorf("annotate screenshot: %w", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return annotateOverlay{}, fmt.Errorf("decode screenshot: %w", err)
	}
	bounds := img.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, img, bounds.Min, draw.Src)

	scale := overlayScale(bounds.Dx(), viewportWidth)
	boxColor := color.RGBA{R: 255, G: 64, B: 64, A: 255}
	rulerColor := color.RGBA{R: 32, G: 128, B: 255, A: 255}
	for _, el := range elements {
		rect := el.Rect.scaled(scale)
		drawRectOutline(dst, rect, boxColor)
		// Corner ticks avoid rasterized labels.
		drawCornerTicks(dst, rect, boxColor)
	}
	drawPixelRuler(dst, bounds.Dx(), bounds.Dy(), scale, rulerColor)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return annotateOverlay{}, fmt.Errorf("encode overlay: %w", err)
	}
	norm, err := providerwire.NormalizeImageBytes(buf.Bytes(), "image/png", visual.MaxRasterBytes())
	if err != nil {
		if int64(len(buf.Bytes())) > visual.MaxRasterBytes().Int64() {
			return annotateOverlay{}, browserengine.Reject("CAPTURE_OUTPUT_OVERSIZED", map[string]any{
				"bytes": len(buf.Bytes()), "max_bytes": visual.MaxRasterBytes().Int64(), "capture_output_kind": "annotated screenshot",
			})
		}
		return annotateOverlay{}, fmt.Errorf("normalize overlay: %w", err)
	}
	return annotateOverlay{Mime: norm.Mime, Bytes: norm.Bytes, regions: regions}, nil
}

// overlayScale maps CSS coordinates to raster pixels.
func overlayScale(rasterWidth, cssWidth int) float64 {
	if rasterWidth <= 0 || cssWidth <= 0 {
		return 1
	}
	return float64(rasterWidth) / float64(cssWidth)
}

func drawRectOutline(img *image.RGBA, r ElementRect, c color.Color) {
	x0 := int(r.Left)
	y0 := int(r.Top)
	x1 := int(r.Right) - 1
	y1 := int(r.Bottom) - 1
	if x1 < x0 {
		x1 = x0
	}
	if y1 < y0 {
		y1 = y0
	}
	for x := x0; x <= x1; x++ {
		setOverlayPixel(img, x, y0, c)
		setOverlayPixel(img, x, y1, c)
	}
	for y := y0; y <= y1; y++ {
		setOverlayPixel(img, x0, y, c)
		setOverlayPixel(img, x1, y, c)
	}
}

func drawCornerTicks(img *image.RGBA, r ElementRect, c color.Color) {
	x0, y0 := int(r.Left), int(r.Top)
	for i := 0; i < 6; i++ {
		setOverlayPixel(img, x0+i, y0, c)
		setOverlayPixel(img, x0, y0+i, c)
	}
}

// drawPixelRuler marks 50 CSS-pixel intervals.
func drawPixelRuler(img *image.RGBA, rasterW, rasterH int, scale float64, c color.Color) {
	const step = 50
	for css := 0; ; css += step {
		x := int(float64(css) * scale)
		if x >= rasterW {
			break
		}
		h := 6
		if css%100 == 0 {
			h = 12
		}
		for y := 0; y < h && y < rasterH; y++ {
			setOverlayPixel(img, x, y, c)
		}
	}
	for css := 0; ; css += step {
		y := int(float64(css) * scale)
		if y >= rasterH {
			break
		}
		w := 6
		if css%100 == 0 {
			w = 12
		}
		for x := 0; x < w && x < rasterW; x++ {
			setOverlayPixel(img, x, y, c)
		}
	}
}

func setOverlayPixel(img *image.RGBA, x, y int, c color.Color) {
	if !image.Pt(x, y).In(img.Bounds()) {
		return
	}
	img.Set(x, y, c)
}
