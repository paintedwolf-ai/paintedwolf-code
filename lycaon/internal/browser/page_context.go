package browser

import (
	"context"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const pageCloseTimeout = 2 * time.Second

type pageLifetimeKey struct{}

// pageContexts maps each page target to the browser context created for it alone.
var pageContexts sync.Map // proto.TargetTargetID -> proto.BrowserBrowserContextID

// createPage opens a target in a browser context of its own, so its cookies, storage, and
// cache are shared with no other page. Setup is bounded; the page lives until teardown.
func createPage(ctx context.Context, b *rod.Browser) (*rod.Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	lifetime = context.WithValue(lifetime, pageLifetimeKey{}, cancel)
	stopBrowser := context.AfterFunc(b.GetContext(), cancel) //nolint:contextcheck // Browser teardown also ends the retained page lifetime.
	stopRequest := context.AfterFunc(ctx, cancel)
	timer := time.AfterFunc(RasterizeTimeout, cancel)
	var target *proto.TargetCreateTargetResult
	browserContext, err := proto.TargetCreateBrowserContext{DisposeOnDetach: true}.Call(b.Context(lifetime))
	if err == nil {
		target, err = (proto.TargetCreateTarget{URL: "about:blank", BrowserContextID: browserContext.BrowserContextID}).Call(b.Context(lifetime))
	}
	var page *rod.Page
	if err == nil {
		pageContexts.Store(target.TargetID, browserContext.BrowserContextID)
		page, err = b.Context(lifetime).PageFromTarget(target.TargetID)
	}
	stopRequest()
	timer.Stop()
	if err == nil {
		err = lifetime.Err()
	}
	if err != nil {
		stopBrowser()
		cancel()
		if target != nil {
			_ = closeTarget(ctx, b, target.TargetID)
		} else if browserContext != nil {
			_ = disposeBrowserContext(ctx, b, browserContext.BrowserContextID)
		}
		return nil, err
	}
	context.AfterFunc(page.GetContext(), func() { //nolint:contextcheck // Target teardown releases the retained page lifetime.
		stopBrowser()
		cancel()
	})
	return page, nil
}

func pageOperationContext(ctx context.Context, page *rod.Page, budget time.Duration) (context.Context, context.CancelFunc) {
	opCtx, cancel := context.WithTimeout(ctx, budget)
	stop := context.AfterFunc(page.GetContext(), cancel)
	if page.GetContext().Err() != nil {
		cancel()
	}
	return opCtx, func() {
		stop()
		cancel()
	}
}

// Close the page target even after a caller times out or the page registers a
// beforeunload handler. Teardown keeps ctx's values but not its cancellation.
func closePage(ctx context.Context, page *rod.Page) error {
	if cancel, ok := page.Browser().GetContext().Value(pageLifetimeKey{}).(context.CancelFunc); ok {
		defer cancel()
	}
	return closeTarget(ctx, page.Browser(), page.TargetID)
}

func closeTarget(ctx context.Context, b *rod.Browser, target proto.TargetTargetID) error {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageCloseTimeout)
	defer cancel()
	defer b.RemoveState(target)
	_, err := proto.TargetCloseTarget{TargetID: target}.Call(b.Context(closeCtx))
	if id, ok := pageContexts.LoadAndDelete(target); ok {
		if disposeErr := disposeBrowserContext(ctx, b, id.(proto.BrowserBrowserContextID)); err == nil {
			err = disposeErr
		}
	}
	return err
}

func disposeBrowserContext(ctx context.Context, b *rod.Browser, id proto.BrowserBrowserContextID) error {
	disposeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageCloseTimeout)
	defer cancel()
	return proto.TargetDisposeBrowserContext{BrowserContextID: id}.Call(b.Context(disposeCtx))
}

// PageInfo honors the page's operation context; Rod's Page.Info uses its
// browser's lifetime context instead.
func PageInfo(page *rod.Page) (*proto.TargetTargetInfo, error) {
	ctx, cancel := pageOperationContext(page.GetContext(), page, RasterizeTimeout)
	defer cancel()
	page = page.Context(ctx)
	res, err := proto.TargetGetTargetInfo{TargetID: page.TargetID}.Call(page)
	if err != nil {
		return nil, err
	}
	return res.TargetInfo, nil
}
