package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
)

// CaptureRequest drives a page and returns assertable text + screenshot bytes.
type CaptureRequest struct {
	URL        string
	ProjectDir string
	// Path is a site-relative entry within ProjectDir. Empty opens /.
	Path     string
	Actions  []CaptureAction
	Selector string
	Width    int
	Height   int
	Wait     string
	Caption  string
	Routes   []RouteRule
	// Record configures a timeline capture.
	Record RecordOpts
	// Mode is screenshot, filmstrip, or timeline. A filmstrip without actions uses one frame.
	Mode string
	// Preview hooks a live read-only screencast for the duration of this drive.
	Preview      *CapturePreview
	CaptureScope captureprojection.Scope
}

// CapturePreview binds a one-shot capture drive to the live preview stream.
type CapturePreview struct {
	Attach  func(held *HeldPage)
	Action  func(act CaptureAction, result json.RawMessage)
	Driving func(driving bool)
	Detach  func()
}

// CaptureResult holds semantic evidence and its raster artifact.
type CaptureResult struct {
	State    json.RawMessage `json:"state"`
	Snapshot json.RawMessage `json:"snapshot"`
	PageEvidence
	RoutesActive int               `json:"routes_active,omitempty"`
	Mime         string            `json:"mime"`
	Bytes        []byte            `json:"-"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	Caption      string            `json:"caption,omitempty"`
	FinalURL     string            `json:"final_url,omitempty"`
	ActionLog    []json.RawMessage `json:"action_results,omitempty"`
	// Coverage reports exact text geometry coverage.
	Coverage *MaskCoverage `json:"coverage,omitempty"`
	// Frames holds filmstrip metadata; bytes remain in the archive.
	Frames []CaptureFrame `json:"frames,omitempty"`
	// Timeline holds a timeline capture's derived facts; frames remain in the archive.
	Timeline *timelinearchive.Report `json:"timeline,omitempty"`
}

// CapturePool runs capture_page against a pooled browser.
type CapturePool struct {
	Pool *Pool
}

func captureFilmstrip(
	ctx context.Context,
	page *rod.Page,
	req CaptureRequest,
	target captureTarget,
	width, height int,
	held *HeldPage,
	idle bool,
	projector *captureprojection.Projector,
) (CaptureResult, error) {
	frames := make([]CaptureFrame, 0, len(req.Actions)+1)
	collect := func(caption string) error {
		scrollSelector(page, req.Selector)
		fr, err := collectCaptureFrame(ctx, page, projector, req.CaptureScope, len(frames), caption)
		if err != nil {
			return err
		}
		frames = append(frames, fr)
		return nil
	}
	if err := collect("initial"); err != nil {
		return CaptureResult{}, err
	}
	actionResults, err := runCaptureActions(page, held.drive, req.Actions, idle, func(act CaptureAction, _ json.RawMessage) error {
		return collect(actionCaption(act))
	})
	if err != nil {
		return CaptureResult{}, err
	}
	zipBytes, err := PackFilmstripZip(frames)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("pack filmstrip: %w", err)
	}
	if len(zipBytes) > visual.MaxFrameArchiveBytes {
		return CaptureResult{}, browserengine.Reject("CAPTURE_OUTPUT_OVERSIZED", map[string]any{
			"bytes": len(zipBytes), "max_bytes": visual.MaxFrameArchiveBytes, "capture_output_kind": "filmstrip archive",
		})
	}
	last := frames[len(frames)-1]
	finalURL := target.URL
	if info, ierr := PageInfo(page); ierr == nil && info != nil {
		finalURL = info.URL
		if !SameOrigin(target.Origin, finalURL) && !strings.HasPrefix(finalURL, "about:") && !strings.HasPrefix(finalURL, "data:") {
			return CaptureResult{}, browserengine.Reject("CAPTURE_NAVIGATION_DENIED", map[string]any{"url": finalURL, "origin": target.Origin, "navigation_url": finalURL, "navigation_origin": target.Origin})
		}
	}
	textFrames := make([]CaptureFrame, len(frames))
	var coverage *MaskCoverage
	for i, fr := range frames {
		textFrames[i] = CaptureFrame{
			Index: fr.Index, Caption: fr.Caption,
			State: fr.State, Snapshot: fr.Snapshot, Mime: fr.Mime,
			Coverage: fr.Coverage,
		}
		coverage = leastComplete(coverage, fr.Coverage)
	}
	out := CaptureResult{
		State: last.State, Snapshot: last.Snapshot, PageEvidence: held.Evidence.since(evidenceMark{}),
		Mime: FilmstripMime, Bytes: zipBytes, Width: width, Height: height,
		Caption: strings.TrimSpace(req.Caption), FinalURL: finalURL,
		ActionLog: actionResults, Frames: textFrames, Coverage: coverage,
	}
	return projectCaptureResult(ctx, projector, req.CaptureScope, out)
}

func collectCaptureFrame(
	ctx context.Context,
	page *rod.Page, projector *captureprojection.Projector,
	scope captureprojection.Scope, index int, caption string,
) (CaptureFrame, error) {
	stateRaw, snapRaw, err := captureSemanticEvidence(page)
	if err != nil {
		return CaptureFrame{}, err
	}
	safeBytes, mime, meta, err := captureProjectedRaster(
		ctx, page, projector, scope, CaptureRasterGeometry{},
	)
	if err != nil {
		return CaptureFrame{}, fmt.Errorf("screen filmstrip frame: %w", err)
	}
	stateRaw, _, err = projector.JSON(ctx, scope, "capture.browser.state", stateRaw)
	if err != nil {
		return CaptureFrame{}, fmt.Errorf("screen filmstrip state: %w", err)
	}
	snapRaw, _, err = projector.JSON(ctx, scope, "capture.browser.snapshot", snapRaw)
	if err != nil {
		return CaptureFrame{}, fmt.Errorf("screen filmstrip snapshot: %w", err)
	}
	projectedCaption, err := projector.Text(ctx, scope, "capture.browser.caption", caption)
	if err != nil {
		return CaptureFrame{}, fmt.Errorf("screen filmstrip caption: %w", err)
	}
	return CaptureFrame{
		Index: index, Caption: projectedCaption.Value,
		State:    json.RawMessage(stateRaw),
		Snapshot: json.RawMessage(snapRaw),
		Mime:     mime, Bytes: safeBytes,
		Coverage: coverageOf(meta),
	}, nil
}

func captureProjectedRaster(
	ctx context.Context, page *rod.Page, projector *captureprojection.Projector,
	scope captureprojection.Scope, geometry CaptureRasterGeometry,
) ([]byte, string, captureprojection.Metadata, error) {
	regions := availablePageRegions(page)
	raw, err := screenshotPNG(page)
	if err != nil {
		return nil, "", captureprojection.Metadata{}, fmt.Errorf("screenshot: %w", err)
	}
	norm, err := providerwire.NormalizeImageBytes(raw, "image/png", visual.MaxRasterBytes())
	if err != nil {
		if int64(len(raw)) > visual.MaxRasterBytes().Int64() {
			return nil, "", captureprojection.Metadata{}, browserengine.Reject("CAPTURE_OUTPUT_OVERSIZED", map[string]any{
				"bytes": len(raw), "max_bytes": visual.MaxRasterBytes().Int64(), "capture_output_kind": "screenshot",
			})
		}
		return nil, "", captureprojection.Metadata{}, fmt.Errorf("normalize screenshot: %w", err)
	}
	safe, meta, err := ProjectRasterRegions(ctx, projector, scope, regions, norm.Mime, norm.Bytes, geometry)
	if err != nil {
		return nil, "", captureprojection.Metadata{}, err
	}
	return safe, norm.Mime, meta, nil
}

func navigateInject(ctx context.Context, page *rod.Page, targetURL, origin string) error {
	navCtx, cancel := context.WithTimeout(ctx, NavTimeout)
	defer cancel()
	page = page.Context(navCtx)
	if _, err := page.EvalOnNewDocument(CaptureCoverageProbeJS); err != nil {
		return fmt.Errorf("install coverage probe: %w", err)
	}
	if _, err := page.EvalOnNewDocument(`(() => {
  const install = () => { ` + SemanticDriverJS + ` };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", install, {once: true});
  } else install();
})()`); err != nil {
		return fmt.Errorf("install navigation driver: %w", err)
	}
	if err := page.Navigate(targetURL); err != nil {
		return browserengine.Reject("CAPTURE_URL_UNREACHABLE", map[string]any{"url": targetURL, "reason": err.Error(), "navigation_url": targetURL, "navigation_error": err.Error()})
	}
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("wait for page load: %w", err)
	}
	if err := injectDriver(page); err != nil {
		return fmt.Errorf("inject driver: %w", err)
	}
	info, _ := PageInfo(page)
	if info != nil && !SameOrigin(origin, info.URL) && !strings.HasPrefix(info.URL, "about:") {
		return browserengine.Reject("CAPTURE_NAVIGATION_DENIED", map[string]any{"url": info.URL, "origin": origin, "navigation_url": info.URL, "navigation_origin": origin})
	}
	return nil
}

func waitIdleQuiet(page *rod.Page, evidence *pageEvidence, yes bool) {
	if !yes {
		return
	}
	if _, err := driverCall(page, "waitIdle", map[string]any{}); err != nil {
		evidence.noteHostLine("idle_wait: " + err.Error())
	}
}

func scrollSelector(page *rod.Page, selector string) {
	if sel := strings.TrimSpace(selector); sel != "" {
		el, err := page.Element(sel)
		if err == nil && el != nil {
			_ = el.ScrollIntoView()
		}
	}
}

func finalizeCapture(
	ctx context.Context,
	page *rod.Page, req CaptureRequest, origin, targetURL string, width, height int,
	evidence PageEvidence, actionResults []json.RawMessage,
	projector *captureprojection.Projector, scope captureprojection.Scope,
) (CaptureResult, error) {
	stateRaw, snapRaw, err := captureSemanticEvidence(page)
	if err != nil {
		return CaptureResult{}, err
	}
	safeBytes, mime, meta, err := captureProjectedRaster(
		ctx, page, projector, scope,
		CaptureRasterGeometry{Width: float64(width), Height: float64(height)},
	)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("screen screenshot: %w", err)
	}
	finalURL := targetURL
	if info, err := PageInfo(page); err == nil && info != nil {
		finalURL = info.URL
		if !SameOrigin(origin, finalURL) && !strings.HasPrefix(finalURL, "about:") && !strings.HasPrefix(finalURL, "data:") {
			return CaptureResult{}, browserengine.Reject("CAPTURE_NAVIGATION_DENIED", map[string]any{"url": finalURL, "origin": origin, "navigation_url": finalURL, "navigation_origin": origin})
		}
	}
	out := CaptureResult{
		State: json.RawMessage(stateRaw), Snapshot: json.RawMessage(snapRaw),
		PageEvidence: evidence, Mime: mime, Bytes: safeBytes, Width: width, Height: height,
		Caption: strings.TrimSpace(req.Caption), FinalURL: finalURL, ActionLog: actionResults,
		Coverage: coverageOf(meta),
	}
	return projectCaptureResult(ctx, projector, scope, out)
}

func projectCaptureResult(
	ctx context.Context, projector *captureprojection.Projector,
	scope captureprojection.Scope, out CaptureResult,
) (CaptureResult, error) {
	if projector == nil {
		return CaptureResult{}, captureprojection.ErrUnavailable
	}
	var err error
	if out.PageEvidence, err = projectEvidence(ctx, projector, scope, out.PageEvidence); err != nil {
		return CaptureResult{}, err
	}
	if len(out.State) > 0 {
		out.State, _, err = projector.JSON(ctx, scope, "capture.browser.state", out.State)
		if err != nil {
			return CaptureResult{}, fmt.Errorf("screen capture state: %w", err)
		}
	}
	if len(out.Snapshot) > 0 {
		out.Snapshot, _, err = projector.JSON(ctx, scope, "capture.browser.snapshot", out.Snapshot)
		if err != nil {
			return CaptureResult{}, fmt.Errorf("screen capture snapshot: %w", err)
		}
	}
	for i := range out.ActionLog {
		if len(out.ActionLog[i]) == 0 {
			continue
		}
		out.ActionLog[i], _, err = projector.JSON(ctx, scope, "capture.browser.action", out.ActionLog[i])
		if err != nil {
			return CaptureResult{}, fmt.Errorf("screen capture action: %w", err)
		}
	}
	for field, target := range map[string]*string{
		"caption": &out.Caption,
		"url":     &out.FinalURL,
	} {
		projected, projectErr := projector.Text(ctx, scope, "capture.browser."+field, *target)
		if projectErr != nil {
			return CaptureResult{}, fmt.Errorf("screen capture %s: %w", field, projectErr)
		}
		*target = projected.Value
	}
	return out, nil
}

type captureTarget struct {
	URL     string
	Origin  string
	RootDir string
}

// PageTarget is what a page shows: its URL, and for a project_dir page the tree served
// under the shared synthetic origin, which the URL alone does not distinguish.
type PageTarget struct {
	URL     string
	RootDir string
}

// ResolveTarget resolves a request's target without opening the page.
func ResolveTarget(req CaptureRequest) (PageTarget, error) {
	target, err := resolveCaptureTarget(req)
	if err != nil {
		return PageTarget{}, err
	}
	return PageTarget{URL: target.URL, RootDir: target.RootDir}, nil
}

func resolveCaptureTarget(req CaptureRequest) (captureTarget, error) {
	urlStr := strings.TrimSpace(req.URL)
	dir := strings.TrimSpace(req.ProjectDir)
	entry := strings.TrimSpace(req.Path)
	if urlStr != "" && dir != "" {
		return captureTarget{}, browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "url_and_project_dir"})
	}
	if urlStr == "" && dir == "" {
		return captureTarget{}, browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "missing_target"})
	}
	if entry != "" && dir == "" {
		return captureTarget{}, browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "path_without_project_dir"})
	}
	if dir != "" {
		abs, err := PrepareStaticRoot(dir)
		if err != nil {
			return captureTarget{}, err
		}
		nav, err := JoinStaticEntry(entry)
		if err != nil {
			return captureTarget{}, err
		}
		return captureTarget{URL: nav, Origin: StaticOrigin, RootDir: abs}, nil
	}
	u, uerr := url.Parse(urlStr)
	if uerr != nil || u.Scheme == "" {
		return captureTarget{}, browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "bad_url", "url": urlStr})
	}
	if strings.EqualFold(u.Scheme, "file") {
		return captureTarget{}, browserengine.Reject("CAPTURE_NAVIGATION_DENIED", map[string]any{"reason": "file_scheme", "url": urlStr, "navigation_url": urlStr})
	}
	if u.Host == "" {
		return captureTarget{}, browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "bad_url", "url": urlStr})
	}
	if !IsLoopbackURL(urlStr) {
		return captureTarget{}, browserengine.Reject("CAPTURE_URL_NOT_LOOPBACK", map[string]any{"url": urlStr, "navigation_url": urlStr})
	}
	return captureTarget{
		URL:    urlStr,
		Origin: fmt.Sprintf("%s://%s", u.Scheme, u.Host),
	}, nil
}

func injectDriver(page *rod.Page) error {
	_, err := proto.RuntimeEvaluate{Expression: SemanticDriverJS, ReturnByValue: true}.Call(page)
	if err != nil {
		return err
	}
	res, err := page.Eval(`() => !!window.__lycaonDriver`)
	if err != nil {
		return err
	}
	if res == nil || !res.Value.Bool() {
		return fmt.Errorf("driver missing after inject")
	}
	return nil
}

func driverCall(page *rod.Page, method string, args map[string]any) ([]byte, error) {
	ctx, cancel := pageOperationContext(page.GetContext(), page, ActionTimeout)
	defer cancel()
	page = page.Context(ctx)
	js, evalArgs, err := driverCallScript(method, args)
	if err != nil {
		return nil, err
	}
	// Navigation creates a new document and driver, so each call waits for the
	// load boundary; an action is not replayed to repair context.
	if err := page.WaitLoad(); err != nil {
		return nil, err
	}
	res, err := page.Eval(js, evalArgs...)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return []byte("null"), nil
	}
	return marshalDriverResult(method, res.Value)
}

func marshalDriverResult(method string, value any) ([]byte, error) {
	body, err := surveyjson.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxSemanticOutputBytes {
		return nil, browserengine.Reject("CAPTURE_OUTPUT_OVERSIZED", map[string]any{
			"channel": "semantic", "method": method, "capture_output_kind": "semantic snapshot", "capture_semantic_output": true,
			"bytes": len(body), "max_bytes": MaxSemanticOutputBytes,
		})
	}
	return body, nil
}

func captureSemanticEvidence(page *rod.Page) ([]byte, []byte, error) {
	state, err := driverCall(page, "state", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("capture semantic state: %w", err)
	}
	snapshot, err := driverCall(page, "snapshot", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("capture semantic snapshot: %w", err)
	}
	return state, snapshot, nil
}

// driverMethods are the page driver calls the host makes; each takes one options object.
var driverMethods = map[string]bool{
	"state": true, "snapshot": true, "waitIdle": true, "waitFor": true,
	"aim": true, "aimField": true, "selectFocusedContents": true, "focusedField": true,
	"select": true, "reach": true, "startRecording": true, "stopRecording": true,
	"beginEffect": true, "endEffect": true,
}

func driverCallScript(method string, args map[string]any) (string, []any, error) {
	if !driverMethods[method] {
		return "", nil, fmt.Errorf("unknown driver method %q", method)
	}
	return `async (method, opts) => await window.__lycaonDriver[method](opts || {})`, []any{method, args}, nil
}
