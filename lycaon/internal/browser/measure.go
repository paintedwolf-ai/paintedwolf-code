package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/go-rod/rod"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// MaxMeasureSelectors bounds how many CSS selectors one measure_page call may request.
const MaxMeasureSelectors = 16

// defaultMeasureStyleProps are computed style keys returned when metrics is empty.
var defaultMeasureStyleProps = []string{
	"padding-top", "padding-right", "padding-bottom", "padding-left",
	"margin-top", "margin-right", "margin-bottom", "margin-left",
	"font-size", "color", "background-color", "z-index",
}

// MeasureRequest probes layout geometry for selectors on a capture target.
type MeasureRequest struct {
	URL          string
	ProjectDir   string
	Path         string
	Selectors    []string
	Metrics      []string // computed style property names; empty → defaultMeasureStyleProps
	Width        int
	Height       int
	Wait         string
	Annotate     bool
	Caption      string
	CaptureScope captureprojection.Scope
}

// ElementRect is getBoundingClientRect in CSS pixels.
type ElementRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
	Left   float64 `json:"left"`
}

// scaled converts a CSS-pixel rect to device pixels for overlay drawing. Only
// the annotation moves; the reported rect stays in CSS pixels.
func (r ElementRect) scaled(scale float64) ElementRect {
	return ElementRect{
		X: r.X * scale, Y: r.Y * scale,
		Width: r.Width * scale, Height: r.Height * scale,
		Top: r.Top * scale, Right: r.Right * scale,
		Bottom: r.Bottom * scale, Left: r.Left * scale,
	}
}

// MeasuredElement is one selector probe result.
type MeasuredElement struct {
	Selector string            `json:"selector"`
	Count    int               `json:"count"`
	Rendered bool              `json:"rendered"`
	Rect     ElementRect       `json:"rect"`
	Styles   map[string]string `json:"styles"`
	Reach    PointerReach      `json:"reach"`
}

// Pointer reach states, from the element's own hit test.
const (
	ReachReceives        = "receives"
	ReachCovered         = "covered"
	ReachClipped         = "clipped"
	ReachOutsideViewport = "outside_viewport"
	ReachNotRendered     = "not_rendered"
)

// ElementBrief identifies an element in a pointer report.
type ElementBrief struct {
	Tag       string `json:"tag"`
	ID        string `json:"id,omitempty"`
	Testid    string `json:"testid,omitempty"`
	Role      string `json:"role,omitempty"`
	Name      string `json:"name,omitempty"`
	ClassName string `json:"className,omitempty"`
}

// PointerReach is where a pointer aimed at an element lands: the element receives it, or
// another element covers it, an overflow ancestor clips it, or it is out of view.
type PointerReach struct {
	State           string        `json:"state"`
	Point           *ActionOffset `json:"point,omitempty"`
	PointsReceiving int           `json:"points_receiving,omitempty"`
	PointsSampled   int           `json:"points_sampled,omitempty"`
	CoveredBy       *ElementBrief `json:"covered_by,omitempty"`
	ClippedBy       *ElementBrief `json:"clipped_by,omitempty"`
}

// MeasureRelation is a host-derived geometric assertion over probed elements.
type MeasureRelation struct {
	Kind    string         `json:"kind"`
	Between []string       `json:"between,omitempty"`
	Of      string         `json:"of,omitempty"`
	Value   float64        `json:"value,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// MeasureResult is the deterministic text geometry report (+ optional overlay bytes).
type MeasureResult struct {
	Units     string            `json:"units"`
	Viewport  map[string]int    `json:"viewport"`
	Elements  []MeasuredElement `json:"elements"`
	Relations []MeasureRelation `json:"relations"`
	// PageID identifies the live page when measurement reused a page_* session.
	PageID    string `json:"page_id,omitempty"`
	FinalURL  string `json:"final_url,omitempty"`
	Caption   string `json:"caption,omitempty"`
	Mime      string `json:"mime,omitempty"`
	Bytes     []byte `json:"-"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Annotated bool   `json:"annotated,omitempty"`
	// Coverage says whether every pixel's text reached the screening pass.
	Coverage *MaskCoverage `json:"coverage,omitempty"`
	regions  PageRegions
}

// MeasurePool runs measure_page against the shared capture browser pool.
type MeasurePool struct {
	Pool *Pool
}

// Measure opens the target, probes rects, styles, and pointer reach, derives relations, and closes it.
func (m *MeasurePool) Measure(ctx context.Context, req MeasureRequest) (MeasureResult, error) {
	if m == nil || m.Pool == nil {
		return MeasureResult{}, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_pool"})
	}
	if _, _, err := normalizeMeasureRequest(req); err != nil {
		return MeasureResult{}, err
	}
	held, err := OpenHeld(ctx, m.Pool, OpenOpts{
		URL: req.URL, ProjectDir: req.ProjectDir, Path: req.Path,
		Width: req.Width, Height: req.Height, Wait: req.Wait, CaptureScope: req.CaptureScope,
	})
	if err != nil {
		return MeasureResult{}, err
	}
	defer func() { _ = held.Close(ctx) }()
	return MeasureHeld(ctx, held, "", req)
}

// MeasureHeld probes the exact authenticated, interaction-driven state retained by page_open.
func MeasureHeld(ctx context.Context, held *HeldPage, pageID string, req MeasureRequest) (MeasureResult, error) {
	if held == nil || held.Page == nil {
		return MeasureResult{}, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_page"})
	}
	ctx, cancel := pageOperationContext(ctx, held.Page, RasterizeTimeout)
	defer cancel()
	page := held.Page.Context(ctx)
	sels, styleKeys, err := normalizeMeasureRequest(req)
	if err != nil {
		return MeasureResult{}, err
	}
	// Idle-mode pages settle before each measurement.
	waitIdleQuiet(page, held.Evidence, held.WaitIdle)
	finalURL := held.TargetURL
	if info, ierr := PageInfo(page); ierr == nil && info != nil {
		finalURL = info.URL
		if !SameOrigin(held.Origin, finalURL) && !strings.HasPrefix(finalURL, "about:") && !strings.HasPrefix(finalURL, "data:") {
			return MeasureResult{}, browserengine.Reject("CAPTURE_NAVIGATION_DENIED", map[string]any{"url": finalURL, "origin": held.Origin, "navigation_url": finalURL, "navigation_origin": held.Origin})
		}
	}
	out, err := measureOpenPage(page, sels, styleKeys, held.Width, held.Height, finalURL, req)
	if err != nil {
		return MeasureResult{}, err
	}
	out.PageID = strings.TrimSpace(pageID)
	return projectMeasureResult(ctx, held.Projector, held.CaptureScope, out)
}

func projectMeasureResult(
	ctx context.Context, projector *captureprojection.Projector, scope captureprojection.Scope,
	out MeasureResult,
) (MeasureResult, error) {
	if projector == nil {
		return MeasureResult{}, captureprojection.ErrUnavailable
	}
	caption, err := projector.Text(ctx, scope, "capture.browser.caption", out.Caption)
	if err != nil {
		return MeasureResult{}, err
	}
	out.Caption = caption.Value
	if len(out.Bytes) == 0 {
		return out, nil
	}
	safe, meta, err := ProjectRasterRegions(
		ctx, projector, scope, out.regions, out.Mime, out.Bytes,
		CaptureRasterGeometry{Width: float64(out.Width), Height: float64(out.Height)},
	)
	if err != nil {
		return MeasureResult{}, fmt.Errorf("screen measurement capture: %w", err)
	}
	out.Bytes = safe
	out.Coverage = coverageOf(meta)
	return out, nil
}

func normalizeMeasureRequest(req MeasureRequest) ([]string, []string, error) {
	sels := normalizeSelectors(req.Selectors)
	if len(sels) == 0 {
		return nil, nil, browserengine.Reject("MEASURE_SELECTORS_REQUIRED", map[string]any{"reason": "empty_selectors"})
	}
	if len(sels) > MaxMeasureSelectors {
		return nil, nil, browserengine.Reject("MEASURE_SELECTORS_BOUNDS", map[string]any{
			"count": len(sels), "max": MaxMeasureSelectors,
		})
	}
	styleKeys := req.Metrics
	if len(styleKeys) == 0 {
		styleKeys = append([]string(nil), defaultMeasureStyleProps...)
	}
	return sels, styleKeys, nil
}

func measureOpenPage(page *rod.Page, selectors, styleKeys []string, width, height int, finalURL string, req MeasureRequest) (MeasureResult, error) {
	elements, err := probeElements(page, selectors, styleKeys)
	if err != nil {
		return MeasureResult{}, err
	}
	if err := probeReach(page, elements); err != nil {
		return MeasureResult{}, err
	}
	out := MeasureResult{
		Units:     "css_px",
		Viewport:  map[string]int{"width": width, "height": height},
		Elements:  elements,
		Relations: DeriveMeasureRelations(elements, width, height),
		FinalURL:  finalURL,
		Caption:   strings.TrimSpace(req.Caption),
	}
	if !req.Annotate {
		return out, nil
	}
	overlay, err := annotateMeasureOverlay(page, elements, width)
	if err != nil {
		return MeasureResult{}, err
	}
	out.Mime = overlay.Mime
	out.Bytes = overlay.Bytes
	out.regions = overlay.regions
	out.Width = width
	out.Height = height
	out.Annotated = true
	return out, nil
}

// probeReach hit-tests each element with the same sampling pointer actions use.
func probeReach(page *rod.Page, elements []MeasuredElement) error {
	selectors := make([]string, len(elements))
	for i, el := range elements {
		selectors[i] = el.Selector
	}
	raw, err := driverCall(page, "reach", map[string]any{"selectors": selectors})
	if err != nil {
		return fmt.Errorf("probe pointer reach: %w", err)
	}
	var out struct {
		Elements []struct {
			Selector string       `json:"selector"`
			Reach    PointerReach `json:"reach"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("decode pointer reach: %w", err)
	}
	for i := range elements {
		if i < len(out.Elements) && out.Elements[i].Selector == elements[i].Selector {
			elements[i].Reach = out.Elements[i].Reach
		}
	}
	return nil
}

func normalizeSelectors(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

type probeNode struct {
	Count    int               `json:"count"`
	Rendered bool              `json:"rendered"`
	Rect     ElementRect       `json:"rect"`
	Styles   map[string]string `json:"styles"`
	Error    string            `json:"error,omitempty"`
}

func probeElements(page *rod.Page, selectors, styleKeys []string) ([]MeasuredElement, error) {
	raw, err := page.Eval(`(sels, props) => {
      const out = [];
      for (const sel of sels) {
        let nodes;
        try { nodes = document.querySelectorAll(sel); }
        catch (e) { out.push({ count: 0, rect: {}, styles: {}, error: String(e && e.message || e) }); continue; }
        const count = nodes.length;
        if (count !== 1) {
          out.push({ count, rect: {}, styles: {} });
          continue;
        }
        const el = nodes[0];
        const r = el.getBoundingClientRect();
        const cs = getComputedStyle(el);
		const rendered = r.width > 0 && r.height > 0 && cs.display !== "none" && cs.visibility !== "hidden" && cs.visibility !== "collapse" && Number.parseFloat(cs.opacity || "1") > 0;
        const styles = {};
        for (const p of props) {
          styles[p] = cs.getPropertyValue(p) || cs[p] || "";
        }
        out.push({
          count,
		  rendered,
          rect: {
            x: r.x, y: r.y, width: r.width, height: r.height,
            top: r.top, right: r.right, bottom: r.bottom, left: r.left,
          },
          styles,
        });
      }
      return out;
    }`, selectors, styleKeys)
	if err != nil {
		return nil, fmt.Errorf("probe elements: %w", err)
	}
	var nodes []probeNode
	if err := raw.Value.Unmarshal(&nodes); err != nil {
		return nil, fmt.Errorf("decode probe: %w", err)
	}
	return measuredProbeNodes(selectors, nodes)
}

func measuredProbeNodes(selectors []string, nodes []probeNode) ([]MeasuredElement, error) {
	if len(nodes) != len(selectors) {
		return nil, fmt.Errorf("probe length mismatch: got %d want %d", len(nodes), len(selectors))
	}
	out := make([]MeasuredElement, 0, len(selectors))
	for i, sel := range selectors {
		n := nodes[i]
		if n.Error != "" {
			return nil, browserengine.Reject("MEASURE_SELECTOR_INVALID", map[string]any{
				"selector": sel, "measure_selector": sel, "reason": n.Error,
			})
		}
		if n.Count == 0 {
			return nil, browserengine.Reject("MEASURE_SELECTOR_EMPTY", map[string]any{
				"selector": sel, "measure_selector": sel, "count": 0,
			})
		}
		if n.Count != 1 {
			return nil, browserengine.Reject("MEASURE_SELECTOR_AMBIGUOUS", map[string]any{
				"selector": sel, "measure_selector": sel, "count": n.Count,
			})
		}
		out = append(out, MeasuredElement{
			Selector: sel,
			Count:    n.Count,
			Rendered: n.Rendered,
			Rect:     roundRect(n.Rect),
			Styles:   n.Styles,
		})
	}
	return out, nil
}

func roundRect(r ElementRect) ElementRect {
	return ElementRect{
		X: roundPx(r.X), Y: roundPx(r.Y),
		Width: roundPx(r.Width), Height: roundPx(r.Height),
		Top: roundPx(r.Top), Right: roundPx(r.Right),
		Bottom: roundPx(r.Bottom), Left: roundPx(r.Left),
	}
}

func roundPx(v float64) float64 {
	return math.Round(v*100) / 100
}

// MarshalMeasureReport encodes the geometry report with stable key ordering.
func MarshalMeasureReport(r MeasureResult) ([]byte, error) {
	type wire struct {
		Units     string            `json:"units"`
		Viewport  map[string]int    `json:"viewport"`
		Elements  []MeasuredElement `json:"elements"`
		Relations []MeasureRelation `json:"relations"`
		PageID    string            `json:"page_id,omitempty"`
		FinalURL  string            `json:"final_url,omitempty"`
		Caption   string            `json:"caption,omitempty"`
		Annotated bool              `json:"annotated,omitempty"`
	}
	return surveyjson.MarshalIndent(wire{
		Units: r.Units, Viewport: r.Viewport, Elements: r.Elements,
		Relations: r.Relations, PageID: r.PageID, FinalURL: r.FinalURL, Caption: r.Caption, Annotated: r.Annotated,
	}, "", "  ")
}
