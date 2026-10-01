package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/timelinearchive"
)

// HeldPage is a live CDP page kept open across tool calls. It has its own browser
// context, so cookies and storage persist for this page and are shared with no other.
type HeldPage struct {
	Page         *rod.Page
	Mount        *CaptureFetch
	Evidence     *pageEvidence
	drive        *pageDrive
	Origin       string
	TargetURL    string
	RootDir      string
	Width        int
	Height       int
	WaitIdle     bool
	Projector    *captureprojection.Projector
	CaptureScope captureprojection.Scope
}

// OpenOpts opens a held page (navigate + driver inject + initial idle).
type OpenOpts struct {
	URL          string
	ProjectDir   string
	Path         string
	Width        int
	Height       int
	Wait         string
	Routes       []RouteRule
	CaptureScope captureprojection.Scope
}

// SnapshotOpts configures one settled screenshot of a held page.
type SnapshotOpts struct {
	Selector string
	Caption  string
}

// NormalizeViewport enforces device-frame bounds.
func NormalizeViewport(width, height int) (int, int, error) {
	if width <= 0 {
		width = DefaultViewportWidth
	}
	if height <= 0 {
		height = DefaultViewportHeight
	}
	if width < MinViewportDim || height < MinViewportDim ||
		width > MaxViewportDim || height > MaxViewportDim {
		return 0, 0, browserengine.Reject("CAPTURE_VIEWPORT_BOUNDS", map[string]any{
			"width":          width,
			"height":         height,
			"min_edge":       MinViewportDim,
			"max_edge":       MaxViewportDim,
			"viewport_width": width, "viewport_height": height,
			"viewport_min_edge": MinViewportDim, "viewport_max_edge": MaxViewportDim,
		})
	}
	return width, height, nil
}

// OpenHeld resolves the target, creates a page, mounts fetch, navigates, and waits idle.
func OpenHeld(ctx context.Context, pool *Pool, opts OpenOpts) (*HeldPage, error) {
	if pool == nil {
		return nil, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_pool"})
	}
	width, height, err := NormalizeViewport(opts.Width, opts.Height)
	if err != nil {
		return nil, err
	}
	target, err := resolveCaptureTarget(CaptureRequest{
		URL: opts.URL, ProjectDir: opts.ProjectDir, Path: opts.Path,
	})
	if err != nil {
		return nil, err
	}
	if err := ValidateRoutes(opts.Routes); err != nil {
		return nil, err
	}
	page, err := pool.NewPage(ctx, width, height)
	if err != nil {
		return nil, err
	}
	projector := pool.captureProjector()
	evidence := attachPageEvidence(ctx, page, projector, opts.CaptureScope)
	drive := newPageDrive(newRouteTable(opts.Routes))
	mount, merr := AttachCaptureFetch(ctx, page, target.RootDir, drive.routes, evidence)
	if merr != nil {
		_ = closePage(ctx, page)
		return nil, merr
	}
	ctx, cancel := pageOperationContext(ctx, page, RasterizeTimeout)
	defer cancel()
	op := page.Context(ctx)
	if err := navigateInject(ctx, op, target.URL, target.Origin); err != nil {
		_ = mount.Close(ctx)
		_ = closePage(ctx, page)
		return nil, err
	}
	waitMode := strings.TrimSpace(opts.Wait)
	if waitMode == "" {
		waitMode = DefaultWaitIdle
	}
	idle := waitMode == "idle"
	waitIdleQuiet(op, evidence, idle)
	if err := op.GetContext().Err(); err != nil {
		_ = mount.Close(ctx)
		_ = closePage(ctx, page)
		return nil, err
	}
	return &HeldPage{
		Page: page, Mount: mount, Evidence: evidence, drive: drive,
		Origin: target.Origin, TargetURL: target.URL, RootDir: target.RootDir,
		Width: width, Height: height, WaitIdle: idle,
		Projector: projector, CaptureScope: opts.CaptureScope,
	}, nil
}

// Reload re-navigates the held page with a new route table, re-injecting its driver.
func (h *HeldPage) Reload(ctx context.Context, width, height int, wait string, routes []RouteRule) error {
	if h == nil || h.Page == nil {
		return browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_page"})
	}
	if err := ValidateRoutes(routes); err != nil {
		return err
	}
	h.drive.routes.replace(routes)
	ctx, cancel := pageOperationContext(ctx, h.Page, RasterizeTimeout)
	defer cancel()
	page := h.Page.Context(ctx)
	if width > 0 && height > 0 {
		w, hgt, err := NormalizeViewport(width, height)
		if err != nil {
			return err
		}
		h.Width, h.Height = w, hgt
		_ = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width: w, Height: hgt, DeviceScaleFactor: RasterScale(w, hgt), Mobile: false,
		})
	}
	if err := navigateInject(ctx, page, h.TargetURL, h.Origin); err != nil {
		return err
	}
	waitMode := strings.TrimSpace(wait)
	if waitMode == "" {
		waitMode = DefaultWaitIdle
	}
	waitIdleQuiet(page, h.Evidence, waitMode == "idle")
	return page.GetContext().Err()
}

// ActReport is what a drive did: each action's result and the page evidence recorded meanwhile.
type ActReport struct {
	Results      []json.RawMessage
	Evidence     PageEvidence
	RoutesActive int
}

// Act runs a bounded action script on the held page, settling after each step.
func (h *HeldPage) Act(ctx context.Context, actions []CaptureAction, onAction func(CaptureAction, json.RawMessage)) (ActReport, error) {
	if h == nil || h.Page == nil {
		return ActReport{}, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_page"})
	}
	if err := validateActions(actions); err != nil {
		return ActReport{}, err
	}
	ctx, cancel := pageOperationContext(ctx, h.Page, RasterizeTimeout)
	defer cancel()
	page := h.Page.Context(ctx)
	mark := h.Evidence.mark()
	results, err := runCaptureActions(page, h.drive, actions, true, func(act CaptureAction, result json.RawMessage) error {
		if onAction != nil {
			onAction(act, result)
		}
		return nil
	})
	if err != nil {
		return ActReport{}, err
	}
	evidence, err := projectEvidence(ctx, h.Projector, h.CaptureScope, h.Evidence.since(mark))
	if err != nil {
		return ActReport{}, err
	}
	return ActReport{Results: results, Evidence: evidence, RoutesActive: h.drive.routes.len()}, nil
}

// Snapshot returns the universal {state, snapshot, log, artifact} without closing the page.
func (h *HeldPage) Snapshot(ctx context.Context, opts SnapshotOpts) (CaptureResult, error) {
	if h == nil || h.Page == nil {
		return CaptureResult{}, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_page"})
	}
	ctx, cancel := pageOperationContext(ctx, h.Page, RasterizeTimeout)
	defer cancel()
	page := h.Page.Context(ctx)
	scrollSelector(page, opts.Selector)
	caption, err := h.projectCaption(ctx, opts.Caption)
	if err != nil {
		return CaptureResult{}, err
	}
	out, err := finalizeCapture(
		ctx,
		page,
		CaptureRequest{Caption: caption, Selector: opts.Selector},
		h.Origin, h.TargetURL, h.Width, h.Height,
		h.Evidence.since(evidenceMark{}), nil, h.Projector, h.CaptureScope,
	)
	out.RoutesActive = h.drive.routes.len()
	return out, err
}

// Filmstrip runs actions while collecting per-settle frames (keeps page open).
func (h *HeldPage) Filmstrip(ctx context.Context, actions []CaptureAction, selector, caption string) (CaptureResult, error) {
	if h == nil || h.Page == nil {
		return CaptureResult{}, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_page"})
	}
	ctx, cancel := pageOperationContext(ctx, h.Page, RasterizeTimeout)
	defer cancel()
	page := h.Page.Context(ctx)
	caption, err := h.projectCaption(ctx, caption)
	if err != nil {
		return CaptureResult{}, err
	}
	req := CaptureRequest{
		Actions: actions, Selector: selector, Caption: caption,
		Mode: CaptureModeFilmstrip, Width: h.Width, Height: h.Height,
		CaptureScope: h.CaptureScope,
	}
	target := captureTarget{URL: h.TargetURL, Origin: h.Origin, RootDir: h.RootDir}
	out, err := captureFilmstrip(ctx, page, req, target, h.Width, h.Height, h, h.WaitIdle, h.Projector)
	out.RoutesActive = h.drive.routes.len()
	return out, err
}

func (h *HeldPage) projectCaption(ctx context.Context, caption string) (string, error) {
	if h == nil || h.Projector == nil {
		return strings.TrimSpace(caption), nil
	}
	projected, err := h.Projector.Text(
		ctx, h.CaptureScope, "capture.browser.caption", strings.TrimSpace(caption),
	)
	return projected.Value, err
}

// Close tears down the CDP page and fetch mount; teardown outlives ctx's cancellation.
func (h *HeldPage) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	// Report teardown failures from both resources.
	var mountErr, pageErr error
	if h.Mount != nil {
		mountErr = h.Mount.Close(ctx)
		h.Mount = nil
	}
	if h.Page != nil {
		pageErr = closePage(ctx, h.Page)
		h.Page = nil
	}
	return errors.Join(mountErr, pageErr)
}

// Capture opens, drives, snapshots, and closes one page.
func (c *CapturePool) Capture(ctx context.Context, req CaptureRequest) (CaptureResult, error) {
	if c == nil || c.Pool == nil {
		return CaptureResult{}, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_pool"})
	}
	mode, err := NormalizeCaptureMode(req.Mode)
	if err != nil {
		return CaptureResult{}, err
	}
	req.Mode = mode
	if err := validateActions(req.Actions); err != nil {
		return CaptureResult{}, err
	}
	held, err := OpenHeld(ctx, c.Pool, OpenOpts{
		URL: req.URL, ProjectDir: req.ProjectDir, Path: req.Path,
		Width: req.Width, Height: req.Height, Wait: req.Wait,
		Routes: req.Routes, CaptureScope: req.CaptureScope,
	})
	if err != nil {
		return CaptureResult{}, err
	}
	defer func() { _ = held.Close(ctx) }()
	ctx, cancel := context.WithTimeout(ctx, RasterizeTimeout)
	defer cancel()
	if req.Preview != nil && req.Preview.Attach != nil {
		req.Preview.Attach(held)
	}
	if req.Preview != nil && req.Preview.Detach != nil {
		defer req.Preview.Detach()
	}

	if mode == CaptureModeFilmstrip && len(req.Actions) > 0 {
		return held.Filmstrip(ctx, req.Actions, req.Selector, req.Caption)
	}
	if mode == CaptureModeTimeline {
		return held.timelineCapture(ctx, req)
	}
	var onAction func(CaptureAction, json.RawMessage)
	if req.Preview != nil && req.Preview.Action != nil && len(req.Actions) > 0 {
		if req.Preview.Driving != nil {
			req.Preview.Driving(true)
			defer req.Preview.Driving(false)
		}
		onAction = req.Preview.Action
	}
	report, err := held.Act(ctx, req.Actions, onAction)
	if err != nil {
		return CaptureResult{}, err
	}
	out, err := held.Snapshot(ctx, SnapshotOpts{Selector: req.Selector, Caption: req.Caption})
	if err != nil {
		return CaptureResult{}, err
	}
	out.ActionLog = report.Results
	return out, nil
}

// timelineCapture records the drive, then reads the settled page's state for the result.
// A failed step returns the recording beside its rejection, as Record does.
func (h *HeldPage) timelineCapture(ctx context.Context, req CaptureRequest) (CaptureResult, error) {
	var onAction func(CaptureAction, json.RawMessage)
	if req.Preview != nil && req.Preview.Action != nil {
		if req.Preview.Driving != nil {
			req.Preview.Driving(true)
			defer req.Preview.Driving(false)
		}
		onAction = req.Preview.Action
	}
	rec, err := h.Record(ctx, req.Actions, req.Record, onAction)
	if err != nil {
		if !rec.Recorded() {
			return CaptureResult{}, err
		}
		return CaptureResult{
			PageEvidence: rec.Evidence, RoutesActive: rec.RoutesActive,
			Mime: timelinearchive.Mime, Bytes: rec.Archive, Width: h.Width, Height: h.Height,
			ActionLog: rec.Results, Coverage: rec.Coverage, Timeline: rec.Report(),
		}, err
	}
	ctx, cancel := pageOperationContext(ctx, h.Page, RasterizeTimeout)
	defer cancel()
	page := h.Page.Context(ctx)
	state, snapshot, err := captureSemanticEvidence(page)
	if err != nil {
		return CaptureResult{}, err
	}
	finalURL := h.TargetURL
	if info, ierr := PageInfo(page); ierr == nil && info != nil {
		finalURL = info.URL
	}
	caption, err := h.projectCaption(ctx, req.Caption)
	if err != nil {
		return CaptureResult{}, err
	}
	out := CaptureResult{
		State: state, Snapshot: snapshot, PageEvidence: rec.Evidence, RoutesActive: rec.RoutesActive,
		Mime: timelinearchive.Mime, Bytes: rec.Archive, Width: h.Width, Height: h.Height,
		Caption: caption, FinalURL: finalURL, ActionLog: rec.Results, Coverage: rec.Coverage,
		Timeline: rec.Report(),
	}
	return projectCaptureResult(ctx, h.Projector, h.CaptureScope, out)
}
