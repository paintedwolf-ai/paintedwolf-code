package browser

import (
	"time"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

// MaxMarkupBytes is the largest authored markup accepted for rasterize.
const MaxMarkupBytes = 512 * 1024

// MaxSemanticOutputBytes caps one driver result.
const MaxSemanticOutputBytes = 1 * 1024 * 1024

// DefaultViewportWidth is the canvas width when none is supplied.
const DefaultViewportWidth = 1280

// DefaultViewportHeight is the default and minimum content-fit height.
const DefaultViewportHeight = 720

// MinViewportDim is the smallest allowed viewport edge.
const MinViewportDim = 1

// MaxViewportDim is the largest observed or rendered viewport edge.
const MaxViewportDim = providerwire.MaxImageDimension

// MaxDeviceScaleFactor caps the raster device pixel ratio.
const MaxDeviceScaleFactor = 2.0

// MinRenderScale is the minimum device pixels per CSS pixel.
const MinRenderScale = 0.5

// MaxRenderCanvasHeight is the tallest CSS canvas FitContent may grow to: the
// height at which RasterScale still reaches MinRenderScale.
const MaxRenderCanvasHeight = int(float64(providerwire.MaxImageDimension) / MinRenderScale)

// RasterScale preserves the sharpest raster within the model image bounds.
func RasterScale(width, height int) float64 {
	longest := width
	if height > longest {
		longest = height
	}
	if longest <= 0 {
		return MaxDeviceScaleFactor
	}
	scale := float64(providerwire.MaxImageDimension) / float64(longest)
	if scale > MaxDeviceScaleFactor {
		return MaxDeviceScaleFactor
	}
	return scale
}

// RasterizeTimeout bounds headless browser work per call.
const RasterizeTimeout = 30 * time.Second

// fontSettleDelay lets inlined fonts update layout before capture.
const fontSettleDelay = 400 * time.Millisecond
