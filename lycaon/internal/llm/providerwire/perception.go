package providerwire

import (
	"math"
	"sync/atomic"
)

// PerceptionWindow bounds how many tool-result images carry pixels in one
// request. Older images keep their records, which the model can reopen.
type PerceptionWindow struct {
	// MaxToolImages is the most images one request attaches.
	MaxToolImages int
	// DropBatch is how many of the oldest images leave together once the
	// window is full, so the attached prefix, and the provider's prompt
	// cache, changes once per batch.
	DropBatch int
}

// DefaultPerceptionWindow applies when no budget is configured.
var DefaultPerceptionWindow = PerceptionWindow{MaxToolImages: 8, DropBatch: 4}

// Normalized returns a window whose bounds are usable: at least one image,
// and a batch between one and the window size.
func (w PerceptionWindow) Normalized() PerceptionWindow {
	if w.MaxToolImages <= 0 {
		return DefaultPerceptionWindow
	}
	if w.DropBatch <= 0 || w.DropBatch > w.MaxToolImages {
		w.DropBatch = w.MaxToolImages
	}
	return w
}

// Dropped returns how many of total images, oldest first, fall outside the
// window. It depends only on total, so every request over the same
// transcript prefix detaches the same images.
func (w PerceptionWindow) Dropped(total int) int {
	w = w.Normalized()
	if total <= w.MaxToolImages {
		return 0
	}
	over := total - w.MaxToolImages
	return w.DropBatch * ((over + w.DropBatch - 1) / w.DropBatch)
}

var perceptionWindow atomic.Pointer[PerceptionWindow]

// SetPerceptionWindow installs the configured window for every transport and
// for context accounting.
func SetPerceptionWindow(w PerceptionWindow) {
	w = w.Normalized()
	perceptionWindow.Store(&w)
}

// CurrentPerceptionWindow returns the installed window or the default.
func CurrentPerceptionWindow() PerceptionWindow {
	if w := perceptionWindow.Load(); w != nil {
		return *w
	}
	return DefaultPerceptionWindow
}

// UnsizedImageTokenEstimate charges an image whose dimensions are unknown.
const UnsizedImageTokenEstimate = 1600

// ImageTokenEstimate approximates the prompt tokens one image costs after
// downscaling to MaxImageDimension, at about one token per 750 pixels.
func ImageTokenEstimate(width, height int) int {
	if width <= 0 || height <= 0 {
		return UnsizedImageTokenEstimate
	}
	w, h := float64(width), float64(height)
	if long := math.Max(w, h); long > MaxImageDimension {
		scale := MaxImageDimension / long
		w, h = w*scale, h*scale
	}
	return max(85, int(math.Ceil(w*h/750)))
}
