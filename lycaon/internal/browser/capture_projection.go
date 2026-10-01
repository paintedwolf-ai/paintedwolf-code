package browser

import (
	"context"
	"fmt"

	"github.com/go-rod/rod"
	"github.com/lycaon/lycaon/internal/captureprojection"
)

const pageRegionCollectionAttempts = 3

const captureRegionsJS = `() => {
  const regions = [];
  let complete = true;
  let overflow = false;
  const limit = 4096;
  const mapRects = (rectList, ox, oy, sx, sy, clip) => {
    const rects = [];
    const clipRight = clip ? clip.x + clip.width : Infinity;
    const clipBottom = clip ? clip.y + clip.height : Infinity;
    for (const source of rectList) {
      let left = source.x;
      let top = source.y;
      let right = source.x + source.width;
      let bottom = source.y + source.height;
      if (clip) {
        left = Math.max(left, clip.x);
        top = Math.max(top, clip.y);
        right = Math.min(right, clipRight);
        bottom = Math.min(bottom, clipBottom);
      }
      if (right <= left || bottom <= top) continue;
      rects.push({x: ox + left * sx, y: oy + top * sy, width: (right - left) * sx, height: (bottom - top) * sy});
    }
    return rects;
  };
  const add = (text, sourceSpans, ox, oy, sx, sy, clip) => {
    if (!text || !text.trim()) return;
    if (regions.length >= limit) { complete = false; overflow = true; return; }
    const spans = [];
    for (const span of sourceSpans) {
      const rects = mapRects(span.rects || [], ox, oy, sx, sy, clip);
      if (rects.length) spans.push({start: span.start, end: span.end, rects});
    }
    if (spans.length) {
      const first = Math.min(...spans.map((span) => span.start));
      const last = Math.max(...spans.map((span) => span.end));
      const visibleText = Array.from(text).slice(first, last).join('');
      regions.push({
        text: visibleText,
        spans: spans.map((span) => ({...span, start: span.start - first, end: span.end - first})),
      });
    }
  };
  const textNodeSpans = (node) => {
    const text = node.nodeValue || '';
    const spans = [];
    let unitOffset = 0;
    let runeOffset = 0;
    for (const char of Array.from(text)) {
      const range = node.ownerDocument.createRange();
      range.setStart(node, unitOffset);
      range.setEnd(node, unitOffset + char.length);
      spans.push({start: runeOffset, end: runeOffset + 1, rects: Array.from(range.getClientRects())});
      unitOffset += char.length;
      runeOffset++;
    }
    return spans;
  };
  const controlSpans = (el, text) => {
    if (!text) return [];
    const bounds = el.getBoundingClientRect();
    const doc = el.ownerDocument;
    const mirror = doc.createElement('div');
    const style = doc.defaultView.getComputedStyle(el);
    for (const property of style) mirror.style.setProperty(property, style.getPropertyValue(property));
    mirror.style.setProperty('position', 'fixed');
    mirror.style.setProperty('left', bounds.x + 'px');
    mirror.style.setProperty('top', bounds.y + 'px');
    mirror.style.setProperty('width', bounds.width + 'px');
    mirror.style.setProperty('height', bounds.height + 'px');
    mirror.style.setProperty('margin', '0');
    mirror.style.setProperty('visibility', 'hidden');
    mirror.style.setProperty('pointer-events', 'none');
    mirror.style.setProperty('overflow', 'hidden');
    mirror.style.setProperty('z-index', '-2147483648');
    if (el.tagName === 'TEXTAREA') {
      mirror.style.setProperty('white-space', 'pre-wrap');
      mirror.style.setProperty('overflow-wrap', 'break-word');
    } else {
      mirror.style.setProperty('white-space', 'pre');
    }
    const node = doc.createTextNode(text);
    mirror.appendChild(node);
    doc.documentElement.appendChild(mirror);
    mirror.scrollLeft = el.scrollLeft || 0;
    mirror.scrollTop = el.scrollTop || 0;
    const spans = textNodeSpans(node);
    mirror.remove();
    return spans;
  };
  const visit = (root, ox, oy, sx, sy) => {
    const doc = root.ownerDocument || root;
    const view = doc.defaultView;
    const walker = doc.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
    let node = walker.currentNode;
    while (node) {
      if (node.nodeType === Node.TEXT_NODE) {
        const parent = node.parentElement;
        if (parent && !['SCRIPT','STYLE','NOSCRIPT'].includes(parent.tagName)) {
          add(node.nodeValue || '', textNodeSpans(node), ox, oy, sx, sy);
        }
      } else if (node.nodeType === Node.ELEMENT_NODE) {
        const el = node;
        const tag = el.tagName;
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') {
          const visibleValue = tag === 'SELECT'
            ? Array.from(el.selectedOptions || []).map((o) => o.textContent || o.value || '').join(' ')
            : (el.value || el.placeholder || '');
          if (tag === 'INPUT' && el.type === 'password' && el.value) {
            // Password text has no visible source range.
          } else {
            try {
              const bounds = el.getBoundingClientRect();
              add(visibleValue, controlSpans(el, visibleValue), ox, oy, sx, sy, bounds);
            } catch (_) { complete = false; }
          }
        }
        const before = view.getComputedStyle(el, '::before').content;
        const after = view.getComputedStyle(el, '::after').content;
        for (const generated of [before, after]) {
          if (generated && generated !== 'none' && generated !== 'normal') {
            // Generated text has no DOM range.
            complete = false;
          }
        }
        if (el.shadowRoot) visit(el.shadowRoot, ox, oy, sx, sy);
        if (tag === 'IFRAME') {
          const r = el.getBoundingClientRect();
          try {
            if (el.contentDocument) {
              const innerWidth = el.contentWindow?.innerWidth || el.clientWidth || r.width;
              const innerHeight = el.contentWindow?.innerHeight || el.clientHeight || r.height;
              if (!el.contentWindow || !el.contentWindow.__pwCaptureProbe ||
                  !el.contentWindow.__pwCaptureProbe.intact() ||
                  el.contentWindow.__pwCaptureProbe.closedShadowRoots() > 0) complete = false;
              visit(el.contentDocument, ox + r.x * sx, oy + r.y * sy,
                sx * r.width / innerWidth, sy * r.height / innerHeight);
            } else complete = false;
          } catch (_) { complete = false; }
        }
        if (tag === 'CANVAS') {
          const r = el.getBoundingClientRect();
          const state = view.__pwCaptureProbe ? view.__pwCaptureProbe.canvasText(el) : null;
          const runs = state && Array.isArray(state.runs) ? state.runs : [];
          if (!state || state.failed) complete = false;
          if (state && state.overflow) { complete = false; overflow = true; }
          const canvasWidth = el.width || r.width || 1;
          const canvasHeight = el.height || r.height || 1;
          for (const run of runs) {
            add(run.text || '', run.spans || [], ox + r.x * sx, oy + r.y * sy,
              sx * r.width / canvasWidth, sy * r.height / canvasHeight);
          }
        }
      }
      node = walker.nextNode();
    }
  };
  visit(document, 0, 0, 1, 1);
  // Closed roots have no readable text geometry.
  if (window.__pwCaptureProbe &&
      (!window.__pwCaptureProbe.intact() || window.__pwCaptureProbe.closedShadowRoots() > 0)) complete = false;
  return {regions, complete, overflow, width: window.innerWidth, height: window.innerHeight};
}`

// CaptureCoverageProbeJS records canvas text geometry and closed roots.
const CaptureCoverageProbeJS = `(() => {
  if (window.__pwCaptureProbe) return;
  let closedShadowRoots = 0;
  const canvasText = new WeakMap();
  let hooksIntact = () => true;
  const stateFor = (canvas) => {
    let state = canvasText.get(canvas);
    if (!state || state.width !== canvas.width || state.height !== canvas.height) {
      state = {width: canvas.width, height: canvas.height, runs: [], failed: false, overflow: false};
      canvasText.set(canvas, state);
    }
    return state;
  };
  Object.defineProperty(window, '__pwCaptureProbe', {
    configurable: false,
    writable: false,
    value: Object.freeze({
      closedShadowRoots: () => closedShadowRoots,
      intact: () => hooksIntact(),
      canvasText: (canvas) => {
        const state = stateFor(canvas);
        return {
          failed: state.failed,
          overflow: state.overflow,
          runs: state.runs.map((run) => ({text: run.text, spans: run.spans.map((span) => ({
            start: span.start,
            end: span.end,
            rects: span.rects.map((rect) => ({...rect})),
          }))})),
        };
      },
    }),
  });
  const attach = Element.prototype.attachShadow;
  const attachProbe = function (init) {
    if (init && init.mode === 'closed') closedShadowRoots++;
    return attach.call(this, init);
  };
  Element.prototype.attachShadow = attachProbe;

  const proto = globalThis.CanvasRenderingContext2D && CanvasRenderingContext2D.prototype;
  const canvasProto = globalThis.HTMLCanvasElement && HTMLCanvasElement.prototype;
  if (!proto || !canvasProto) {
    hooksIntact = () => false;
    return;
  }
  const Point = DOMPoint;
  const measureText = proto.measureText;
  const getTransform = proto.getTransform;
  const contexts = new Set();
  const contextState = new WeakMap();
  const stateForContext = (context) => {
    let state = contextState.get(context);
    if (!state) {
      state = {clipped: false, stack: []};
      contextState.set(context, state);
    }
    return state;
  };
  const resetCanvas = (canvas) => {
    canvasText.delete(canvas);
    for (const context of contexts) {
      if (context.canvas === canvas) contextState.delete(context);
    }
  };
  const invalidateCanvas = (canvas) => {
    if (!canvas) return;
    const state = stateFor(canvas);
    if (state.runs.length === 0) return;
    state.runs = [];
    state.overflow = false;
    state.failed = true;
  };
  const transformedRect = (matrix, x, y, width, height) => {
    const corners = [
      new Point(x, y).matrixTransform(matrix),
      new Point(x + width, y).matrixTransform(matrix),
      new Point(x, y + height).matrixTransform(matrix),
      new Point(x + width, y + height).matrixTransform(matrix),
    ];
    const xs = corners.map((point) => point.x);
    const ys = corners.map((point) => point.y);
    return {x: Math.min(...xs), y: Math.min(...ys), width: Math.max(...xs) - Math.min(...xs), height: Math.max(...ys) - Math.min(...ys)};
  };
  const intersects = (a, b) => a.x < b.x + b.width && b.x < a.x + a.width &&
    a.y < b.y + b.height && b.y < a.y + a.height;
  const remember = function (text, x, y, maxWidth, stroked) {
    const canvas = this.canvas;
    if (!canvas) return;
    const state = stateFor(canvas);
    const value = String(text ?? '');
    if (!value.trim()) return;
    const matrix = getTransform.call(this);
    const shadowed = this.shadowColor !== 'rgba(0, 0, 0, 0)' && this.shadowColor !== 'transparent';
    if (stateForContext(this).clipped || matrix.b !== 0 || matrix.c !== 0 ||
        (this.filter || 'none') !== 'none' || this.globalCompositeOperation !== 'source-over' || shadowed) {
      invalidateCanvas(canvas);
      state.failed = true;
      return;
    }
    const metrics = measureText.call(this, value);
    const ascent = Number.isFinite(metrics.actualBoundingBoxAscent) ? metrics.actualBoundingBoxAscent : 0;
    const descent = Number.isFinite(metrics.actualBoundingBoxDescent) ? metrics.actualBoundingBoxDescent : 0;
    const width = metrics.width || 0;
    const scale = Number.isFinite(maxWidth) && maxWidth > 0 && width > maxWidth ? maxWidth / width : 1;
    const resolvedDirection = this.direction === 'inherit'
      ? canvas.ownerDocument.defaultView.getComputedStyle(canvas).direction
      : this.direction;
    const direction = resolvedDirection === 'rtl' ? 'rtl' : 'ltr';
    let left = x;
    if (this.textAlign === 'center') left -= width * scale / 2;
    else if (this.textAlign === 'right' || (this.textAlign === 'end' && direction === 'ltr') || (this.textAlign === 'start' && direction === 'rtl')) left -= width * scale;
    const strokePad = stroked ? Math.max(Number(this.lineWidth) || 0, 0) / 2 : 0;
    const chars = Array.from(value);
    const spans = [];
    let prefix = '';
    let prefixWidth = 0;
    for (let index = 0; index < chars.length; index++) {
      prefix += chars[index];
      const nextWidth = measureText.call(this, prefix).width;
      const charLeft = left + prefixWidth * scale;
      const charMetrics = measureText.call(this, chars[index]);
      const glyphLeft = (Number.isFinite(charMetrics.actualBoundingBoxLeft) ? charMetrics.actualBoundingBoxLeft * scale : 0) + strokePad;
      const glyphRight = (Number.isFinite(charMetrics.actualBoundingBoxRight) ? charMetrics.actualBoundingBoxRight * scale : (nextWidth - prefixWidth) * scale) + strokePad;
      const glyphAscent = (Number.isFinite(charMetrics.actualBoundingBoxAscent) ? charMetrics.actualBoundingBoxAscent : ascent) + strokePad;
      const glyphDescent = (Number.isFinite(charMetrics.actualBoundingBoxDescent) ? charMetrics.actualBoundingBoxDescent : descent) + strokePad;
      spans.push({start: index, end: index + 1, rects: [transformedRect(matrix,
        charLeft - glyphLeft, y - glyphAscent, Math.max(glyphLeft + glyphRight, 0), glyphAscent + glyphDescent)]});
      prefixWidth = nextWidth;
    }
    const rect = transformedRect(matrix, left - strokePad, y - ascent - strokePad,
      width * scale + strokePad * 2, ascent + descent + strokePad * 2);
    const key = value + '\u0000' + [rect.x, rect.y, rect.width, rect.height].map((n) => Math.round(n * 100) / 100).join(',');
    const prior = state.runs.findIndex((run) => run.key === key);
    const run = {key, text: value, spans, rect};
    if (prior >= 0) {
      state.runs[prior] = run;
      return;
    }
    const retained = state.runs.filter((existing) => !intersects(existing.rect, rect));
    if (retained.length !== state.runs.length) state.failed = true;
    state.runs = retained;
    if (state.runs.length < 4096) state.runs.push(run);
    else state.overflow = true;
  };
  const textProbes = new Map();
  for (const name of ['fillText', 'strokeText']) {
    const drawText = proto[name];
    const textProbe = function (text, x, y, maxWidth) {
      const result = maxWidth === undefined ? drawText.call(this, text, x, y) : drawText.call(this, text, x, y, maxWidth);
      try { remember.call(this, text, x, y, maxWidth, name === 'strokeText'); }
      catch (_) {
        if (this.canvas) {
          const state = stateFor(this.canvas);
          state.runs = [];
          state.failed = true;
        }
      }
      return result;
    };
    textProbes.set(name, textProbe);
    proto[name] = textProbe;
  }
  const methodProbes = new Map();
  for (const name of ['fillRect', 'strokeRect', 'drawImage', 'putImageData', 'fill', 'stroke', 'drawFocusIfNeeded']) {
    const draw = proto[name];
    if (typeof draw !== 'function') continue;
    const probe = function (...args) {
      const result = draw.apply(this, args);
      invalidateCanvas(this.canvas);
      return result;
    };
    methodProbes.set(name, probe);
    proto[name] = probe;
  }
  const clearRect = proto.clearRect;
  const clearProbe = function (x, y, width, height) {
    const result = clearRect.call(this, x, y, width, height);
    const canvas = this.canvas;
    const context = stateForContext(this);
    const matrix = getTransform.call(this);
    const rect = transformedRect(matrix, x, y, width, height);
    if (canvas && !context.clipped && matrix.b === 0 && matrix.c === 0 &&
        rect.x <= 0 && rect.y <= 0 && rect.x + rect.width >= canvas.width &&
        rect.y + rect.height >= canvas.height) resetCanvas(canvas);
    else invalidateCanvas(canvas);
    return result;
  };
  proto.clearRect = clearProbe;
  const stateProbes = new Map();
  const save = proto.save;
  const saveProbe = function () {
    const state = stateForContext(this);
    state.stack.push(state.clipped);
    return save.call(this);
  };
  stateProbes.set('save', saveProbe);
  proto.save = saveProbe;
  const restore = proto.restore;
  const restoreProbe = function () {
    const result = restore.call(this);
    const state = stateForContext(this);
    if (state.stack.length > 0) state.clipped = state.stack.pop();
    return result;
  };
  stateProbes.set('restore', restoreProbe);
  proto.restore = restoreProbe;
  const clip = proto.clip;
  const clipProbe = function (...args) {
    const result = clip.apply(this, args);
    stateForContext(this).clipped = true;
    return result;
  };
  stateProbes.set('clip', clipProbe);
  proto.clip = clipProbe;
  if (typeof proto.reset === 'function') {
    const reset = proto.reset;
    const resetProbe = function () {
      const result = reset.call(this);
      resetCanvas(this.canvas);
      return result;
    };
    stateProbes.set('reset', resetProbe);
    proto.reset = resetProbe;
  }
  const getContext = canvasProto.getContext;
  const getContextProbe = function (...args) {
    const context = getContext.apply(this, args);
    if (args[0] === '2d' && context) contexts.add(context);
    return context;
  };
  canvasProto.getContext = getContextProbe;
  const dimensionProbes = new Map();
  for (const name of ['width', 'height']) {
    const descriptor = Object.getOwnPropertyDescriptor(canvasProto, name);
    if (!descriptor || typeof descriptor.set !== 'function') continue;
    const setDimension = function (value) {
      descriptor.set.call(this, value);
      resetCanvas(this);
    };
    dimensionProbes.set(name, setDimension);
    Object.defineProperty(canvasProto, name, {...descriptor, set: setDimension});
  }
  hooksIntact = () => Element.prototype.attachShadow === attachProbe &&
    canvasProto.getContext === getContextProbe &&
    proto.fillText === textProbes.get('fillText') && proto.strokeText === textProbes.get('strokeText') &&
    proto.clearRect === clearProbe &&
    [...methodProbes].every(([name, probe]) => proto[name] === probe) &&
    [...stateProbes].every(([name, probe]) => proto[name] === probe) &&
    [...dimensionProbes].every(([name, probe]) => Object.getOwnPropertyDescriptor(canvasProto, name)?.set === probe) &&
    [...contexts].every((context) => context.fillText === textProbes.get('fillText') &&
      context.strokeText === textProbes.get('strokeText') && context.clearRect === clearProbe &&
      [...methodProbes].every(([name, probe]) => context[name] === probe) &&
      [...stateProbes].every(([name, probe]) => context[name] === probe));
})();`

// PageRegions is maskable text geometry and its CSS extent.
type PageRegions struct {
	Regions  []captureprojection.Region `json:"regions"`
	Complete bool                       `json:"complete"`
	Overflow bool                       `json:"overflow"`
	Width    float64                    `json:"width"`
	Height   float64                    `json:"height"`
}

// MaskCoverage reports whether readable text had exact geometry.
type MaskCoverage struct {
	Structured bool `json:"structured"`
}

func coverageOf(meta captureprojection.Metadata) *MaskCoverage {
	return &MaskCoverage{Structured: meta.StructuredCoverage}
}

func leastComplete(a, b *MaskCoverage) *MaskCoverage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &MaskCoverage{
		Structured: a.Structured && b.Structured,
	}
}

// CaptureRasterGeometry is the CSS extent encoded by a raster.
type CaptureRasterGeometry struct {
	Width, Height float64
}

// CollectPageRegions reads maskable geometry for the current page state.
func CollectPageRegions(page *rod.Page) (PageRegions, error) {
	if page == nil {
		return PageRegions{}, fmt.Errorf("capture page is unavailable")
	}
	raw, err := page.Eval(captureRegionsJS)
	if err != nil {
		return PageRegions{}, err
	}
	var out PageRegions
	if raw == nil {
		return PageRegions{}, fmt.Errorf("decode capture regions")
	}
	if err := raw.Value.Unmarshal(&out); err != nil {
		return PageRegions{}, fmt.Errorf("decode capture regions: %w", err)
	}
	return out, nil
}

// availablePageRegions returns incomplete coverage when collection fails.
func availablePageRegions(page *rod.Page) PageRegions {
	return availablePageRegionsWith(page, CollectPageRegions)
}

func availablePageRegionsWith(
	page *rod.Page,
	collect func(*rod.Page) (PageRegions, error),
) PageRegions {
	// Renderer activity can interrupt evaluation briefly.
	for range pageRegionCollectionAttempts {
		regions, err := collect(page)
		if err == nil {
			return regions
		}
	}
	return PageRegions{Complete: false}
}

// ProjectRasterRegions masks a raster against geometry captured with it.
func ProjectRasterRegions(
	ctx context.Context, projector *captureprojection.Projector,
	scope captureprojection.Scope, regions PageRegions, mime string, raw []byte,
	geometry CaptureRasterGeometry,
) ([]byte, captureprojection.Metadata, error) {
	if projector == nil {
		return nil, captureprojection.Metadata{}, captureprojection.ErrUnavailable
	}
	if geometry.Width > 0 {
		regions.Width = geometry.Width
	}
	if geometry.Height > 0 {
		regions.Height = geometry.Height
	}
	return projector.Raster(
		ctx, scope, mime, raw, regions.Width, regions.Height, regions.Regions, regions.Complete,
	)
}
