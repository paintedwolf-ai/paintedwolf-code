package browser

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// hideScrollbarsCSS removes scrollbar chrome from screenshots without disabling scroll
// (inner overflow regions stay scrollable for drive actions).
const hideScrollbarsCSS = `*::-webkit-scrollbar{display:none!important;width:0!important;height:0!important}
*{scrollbar-width:none!important;-ms-overflow-style:none!important}`

// hideScrollbars removes scrollbar gutters from PNGs.
func hideScrollbars(page *rod.Page) {
	if page == nil {
		return
	}
	_, _ = page.Eval(`(css) => {
  if (document.getElementById('lycaon-hide-scrollbars')) return true;
  const style = document.createElement('style');
  style.id = 'lycaon-hide-scrollbars';
  style.textContent = css;
  (document.head || document.documentElement).appendChild(style);
  return true;
}`, hideScrollbarsCSS)
}

// screenshotPNG captures the viewport after hiding scrollbar chrome.
func screenshotPNG(page *rod.Page) ([]byte, error) {
	if page == nil {
		return nil, fmt.Errorf("nil page")
	}
	hideScrollbars(page)
	raw, err := page.Screenshot(false, &proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// canvasPNG captures a resolved render canvas. Capture runs beyond the emulated
// viewport rather than resizing it, so the document is taken whole while vh
// units and viewport media queries stay bound to canvas.Frame.
func canvasPNG(page *rod.Page, canvas RenderCanvas) ([]byte, error) {
	if page == nil {
		return nil, fmt.Errorf("nil page")
	}
	hideScrollbars(page)
	raw, err := page.Screenshot(false, &proto.PageCaptureScreenshot{
		Format:                proto.PageCaptureScreenshotFormatPng,
		CaptureBeyondViewport: canvas.Height > canvas.Frame,
		Clip: &proto.PageViewport{
			X: 0, Y: 0,
			Width:  float64(canvas.Width),
			Height: float64(canvas.Height),
			Scale:  canvas.Scale,
		},
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}
