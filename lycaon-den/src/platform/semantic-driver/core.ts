/** DOM drive primitives for capture_page and window.__harness. */

export type Json = Record<string, unknown>;

export const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export function isVisible(el: Element | null): el is Element {
  if (!el || !(el instanceof Element)) return false;
  if (el instanceof HTMLElement) {
    if (el.offsetParent === null && el.getClientRects().length === 0) return false;
    return true;
  }
  if (el instanceof SVGElement) {
    if (el.getClientRects().length === 0) return false;
    if (typeof window !== "undefined" && typeof window.getComputedStyle === "function") {
      const style = window.getComputedStyle(el);
      if (style.display === "none" || style.visibility === "hidden") return false;
    }
    return true;
  }
  return el.getClientRects().length > 0;
}

/** Set a field value and notify listeners. */
export function setNativeValue(el: HTMLInputElement | HTMLTextAreaElement, value: string): void {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  setter ? setter.call(el, value) : (el.value = value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
}

/** A form control disabled directly or by its fieldset, or a control marked aria-disabled. */
export function isDisabled(el: Element | null): boolean {
  if (!el) return false;
  return el.matches(":disabled") || el.getAttribute("aria-disabled") === "true";
}

function cssEscape(value: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") {
    return CSS.escape(value);
  }
  return value.replace(/["\\]/g, "\\$&");
}

export function byTestid(testid: string): HTMLElement | null {
  const el = document.querySelector<HTMLElement>(`[data-testid="${cssEscape(testid)}"]`);
  return isVisible(el) ? el : null;
}

export function resolve(testidOrSelector: string): HTMLElement | null {
  const direct = byTestid(testidOrSelector);
  if (direct) return direct;
  let el: HTMLElement | null = null;
  try {
    el = document.querySelector<HTMLElement>(testidOrSelector);
  } catch {
    el = null;
  }
  return isVisible(el) ? el : null;
}

const INTERACTIVE = "button, [role=button], a, [role=link], input, textarea, select, [data-testid]";

/** Resolve an accessible name. */
export function accessibleName(el: Element): string {
  const labelledBy = el.getAttribute("aria-labelledby");
  if (labelledBy) {
    const joined = labelledBy
      .split(/\s+/)
      .map((id) => (document.getElementById(id)?.textContent ?? "").trim())
      .filter(Boolean)
      .join(" ");
    if (joined) return joined;
  }
  const aria = (el.getAttribute("aria-label") ?? "").trim();
  if (aria) return aria;
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    if (el.id) {
      const lab = document.querySelector<HTMLLabelElement>(`label[for="${cssEscape(el.id)}"]`);
      const fromFor = (lab?.textContent ?? "").trim();
      if (fromFor) return fromFor;
    }
    const wrap = el.closest("label");
    const fromWrap = wrap ? (wrap.textContent ?? "").trim() : "";
    if (fromWrap) return fromWrap;
    const placeholder = "placeholder" in el ? String(el.placeholder ?? "").trim() : "";
    if (placeholder) return placeholder;
  }
  if (el instanceof SVGElement) {
    const titleEl = el.querySelector("title");
    if (titleEl && titleEl.textContent?.trim()) {
      return titleEl.textContent.trim();
    }
  }
  return (el.textContent ?? "").trim();
}

/** Find a control by label text. */
export function resolveByLabel(label: string): HTMLElement | null {
  const want = label.trim().toLowerCase();
  for (const el of document.querySelectorAll("label")) {
    if (!isVisible(el)) continue;
    if ((el.textContent ?? "").trim().toLowerCase().includes(want)) {
      const forId = el.getAttribute("for");
      if (forId) {
        const ctrl = document.getElementById(forId);
        if (ctrl && isVisible(ctrl)) return ctrl;
      }
      const nested = el.querySelector<HTMLElement>("input, textarea, button");
      if (nested && isVisible(nested)) return nested;
    }
  }
  return null;
}

export function resolveByAccessibleName(name: string): HTMLElement | null {
  const want = name.trim().toLowerCase();
  if (!want) return null;
  const candidates = [...document.querySelectorAll<HTMLElement>(INTERACTIVE)].filter(
    (el) => isVisible(el) && accessibleName(el).toLowerCase().includes(want),
  );
  candidates.sort((a, b) => accessibleName(a).length - accessibleName(b).length);
  return candidates[0] ?? null;
}

export function resolveByRole(role: string, name?: string): HTMLElement | null {
  const extras = role === "button" ? ", button" : role === "link" ? ", a" : "";
  const candidates = [...document.querySelectorAll<HTMLElement>(`[role="${cssEscape(role)}"]${extras}`)].filter(isVisible);
  if (!name) return candidates[0] ?? null;
  const want = name.trim().toLowerCase();
  return candidates.find((el) => accessibleName(el).toLowerCase().includes(want)) ?? null;
}

export type TargetOpts = {
  text?: string;
  selector?: string;
  role?: string;
  label?: string;
  testid?: string;
};

/** testid, then selector, then role+name, then label, then text. */
export function findTarget(opts: TargetOpts): HTMLElement | null {
  if (opts.testid) return byTestid(opts.testid);
  if (opts.selector) return resolve(opts.selector);
  const name = opts.label || opts.text;
  if (opts.role) return resolveByRole(opts.role, name);
  if (opts.label) return resolveByAccessibleName(opts.label) ?? resolveByLabel(opts.label);
  if (opts.text) return resolveByAccessibleName(opts.text);
  return null;
}

export async function waitUntil(
  predicate: () => boolean,
  {
    timeoutMs = 30_000,
    settleMs = 0,
    label = "condition",
  }: { timeoutMs?: number; settleMs?: number; label?: string } = {},
): Promise<Json> {
  const start = Date.now();
  let settledSince: number | null = null;
  while (Date.now() - start < timeoutMs) {
    if (predicate()) {
      if (settleMs === 0) return { ok: true, waitedFor: label };
      settledSince ??= Date.now();
      if (Date.now() - settledSince >= settleMs) return { ok: true, waitedFor: label };
    } else {
      settledSince = null;
    }
    await sleep(100);
  }
  return { ok: false, error: `timed out after ${timeoutMs}ms waiting for ${label}` };
}

function passesPointerThrough(el: Element): boolean {
  if (typeof window === "undefined" || typeof window.getComputedStyle !== "function") return false;
  return window.getComputedStyle(el).pointerEvents === "none";
}

/**
 * The element a pointer aimed at `el` actually reaches: a `pointer-events: none` target,
 * such as an icon inside a button, hands the hit to its nearest ancestor that takes it.
 */
function pointerHitTarget(el: Element): Element {
  let node: Element | null = el;
  while (node && passesPointerThrough(node)) node = node.parentElement;
  return node ?? el;
}

/** A compact identity for an element in a pointer report. */
export type ElementBrief = {
  tag: string;
  id?: string;
  testid?: string;
  role?: string;
  name?: string;
  className?: string;
  /** Interaction state the element declares: ARIA states and native form state. */
  state?: Record<string, string>;
};

const MAX_CLASS_REPORT = 160;

/** ARIA attributes that record interaction state rather than identity. */
const STATE_ATTRIBUTES = ["aria-selected", "aria-checked", "aria-pressed", "aria-expanded", "aria-current", "aria-disabled", "aria-invalid"];

/** The element's class list, whole classes only, within the report bound. */
function classReport(el: Element): string {
  const raw = el.getAttribute("class");
  if (!raw) return "";
  let out = "";
  for (const name of raw.trim().split(/\s+/)) {
    const next = out ? `${out} ${name}` : name;
    if (next.length > MAX_CLASS_REPORT) break;
    out = next;
  }
  return out;
}

function stateReport(el: Element): Record<string, string> | undefined {
  const state: Record<string, string> = {};
  for (const attr of STATE_ATTRIBUTES) {
    const value = el.getAttribute(attr);
    if (value !== null) state[attr] = value;
  }
  if (el instanceof HTMLInputElement && (el.type === "checkbox" || el.type === "radio")) {
    state.checked = String(el.checked);
  }
  if (el.matches(":disabled")) state.disabled = "true";
  return Object.keys(state).length ? state : undefined;
}

export function describeElement(el: Element): ElementBrief {
  const name = accessibleName(el).replace(/\s+/g, " ").trim().slice(0, 60);
  const className = classReport(el);
  return {
    tag: el.tagName.toLowerCase(),
    id: el.id || undefined,
    testid: el.getAttribute("data-testid") || undefined,
    role: el.getAttribute("role") || undefined,
    name: name || undefined,
    className: className || undefined,
    state: stateReport(el),
  };
}

type Box = { left: number; top: number; right: number; bottom: number };

function intersect(a: Box, b: Box): Box | null {
  const box = {
    left: Math.max(a.left, b.left),
    top: Math.max(a.top, b.top),
    right: Math.min(a.right, b.right),
    bottom: Math.min(a.bottom, b.bottom),
  };
  return box.right - box.left >= 1 && box.bottom - box.top >= 1 ? box : null;
}

function clipsOverflow(el: Element): boolean {
  if (typeof window === "undefined" || typeof window.getComputedStyle !== "function") return false;
  const style = window.getComputedStyle(el);
  return [style.overflow, style.overflowX, style.overflowY].some((value) => value !== "" && value !== "visible");
}

/** Where a pointer lands when aimed at an element. */
export type PointerReach =
  | { state: "receives"; point: { x: number; y: number }; points_receiving: number; points_sampled: number }
  | { state: "covered"; point: { x: number; y: number }; covered_by: ElementBrief; points_sampled: number }
  | { state: "clipped"; clipped_by: ElementBrief }
  | { state: "outside_viewport" }
  | { state: "not_rendered" };

/** Samples visible clipped element bounds to find receiving hit points. */
export function pointerReach(el: Element): PointerReach {
  const first = el.getClientRects()[0];
  if (!first || first.width <= 0 || first.height <= 0) return { state: "not_rendered" };
  const viewport: Box = {
    left: 0,
    top: 0,
    right: window.innerWidth || document.documentElement.clientWidth,
    bottom: window.innerHeight || document.documentElement.clientHeight,
  };
  let visible = intersect({ left: first.left, top: first.top, right: first.right, bottom: first.bottom }, viewport);
  if (!visible) return { state: "outside_viewport" };
  for (let ancestor = el.parentElement; ancestor && ancestor !== document.documentElement; ancestor = ancestor.parentElement) {
    if (!clipsOverflow(ancestor)) continue;
    const clip = ancestor.getBoundingClientRect();
    visible = intersect(visible, { left: clip.left, top: clip.top, right: clip.right, bottom: clip.bottom });
    if (!visible) return { state: "clipped", clipped_by: describeElement(ancestor) };
  }
  const width = visible.right - visible.left;
  const height = visible.bottom - visible.top;
  const points = [[0.5, 0.5], [0.25, 0.25], [0.75, 0.25], [0.25, 0.75], [0.75, 0.75]].map(([fx, fy]) => ({
    x: Math.floor(visible.left + width * fx!),
    y: Math.floor(visible.top + height * fy!),
  }));
  const receiver = pointerHitTarget(el);
  let firstReceiving: { x: number; y: number } | undefined;
  let receiving = 0;
  let cover: Element | undefined;
  for (const point of points) {
    const top = typeof document.elementFromPoint === "function" ? document.elementFromPoint(point.x, point.y) : null;
    if (top && (top === el || el.contains(top) || top === receiver)) {
      receiving++;
      firstReceiving ??= point;
    } else if (top) {
      cover ??= top;
    }
  }
  if (firstReceiving) {
    return { state: "receives", point: firstReceiving, points_receiving: receiving, points_sampled: points.length };
  }
  return { state: "covered", point: points[0]!, covered_by: describeElement(cover ?? document.body), points_sampled: points.length };
}

/** Brings the element into view without animation when the pointer cannot reach it. */
export function revealForPointer(el: Element): PointerReach {
  const reach = pointerReach(el);
  if (reach.state !== "outside_viewport" && reach.state !== "clipped") return reach;
  el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
  return pointerReach(el);
}
