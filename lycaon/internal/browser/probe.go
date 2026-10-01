package browser

import (
	"context"

	"github.com/lycaon/lycaon/internal/browserengine"
)

// ProbeUsability exercises the launch, target creation, and screenshot path used by
// visual tools without navigating away from the browser's offline about:blank page.
func ProbeUsability(ctx context.Context, opts LaunchOptions) error {
	b, cleanup, err := LaunchHeadless(ctx, opts)
	if err != nil {
		return err
	}
	defer cleanup()

	page, err := createPage(ctx, b)
	if err != nil {
		return cdpUnavailable("probe_page_failed", err)
	}
	defer func() { _ = closePage(ctx, page) }()
	ctx, cancel := pageOperationContext(ctx, page, RasterizeTimeout)
	defer cancel()
	page = page.Context(ctx)

	if err := page.WaitLoad(); err != nil {
		return cdpUnavailable("probe_page_failed", err)
	}
	raw, err := page.Screenshot(false, nil)
	if err != nil {
		return cdpUnavailable("probe_screenshot_failed", err)
	}
	if len(raw) == 0 {
		return browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "probe_empty_screenshot"})
	}
	return nil
}
