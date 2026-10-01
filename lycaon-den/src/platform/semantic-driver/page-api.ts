/** CDP-injected page driver (`window.__lycaonDriver`). */

import {
  type Json,
  type PointerReach,
  type TargetOpts,
  describeElement,
  findTarget,
  isDisabled,
  isVisible,
  pointerReach,
  resolve,
  revealForPointer,
  sleep,
  waitUntil,
} from "./core.ts";
import { beginEffect, endEffect, logRequest, noteAimedElement } from "./effect.ts";
import { type RecordOptions, startRecording, stopRecording } from "./recorder.ts";

declare global {
  interface Window {
    __lycaonDriver?: LycaonDriverApi;
    __lycaonPending?: number;
    __lycaonMutations?: number;
  }
}

/** What an aim needs from its target before the host sends input to the point. */
export type AimRequirement = "receive" | "visible";

export interface LycaonDriverApi {
  state(): Json;
  snapshot(): Json;
  /** Finds, reveals, and hit-tests a target; the host dispatches trusted input at `point`. */
  aim(opts: TargetOpts & { require?: AimRequirement; timeout_ms?: number }): Promise<Json>;
  /** Aims at a fillable field: an input, textarea, or editable region, or one nested in the target. */
  aimField(opts: TargetOpts & { timeout_ms?: number }): Promise<Json>;
  /** Selects the focused field's contents so typed input replaces them. */
  selectFocusedContents(): Json;
  /** The focused field and its current value. */
  focusedField(): Json;
  select(opts: TargetOpts & { value: string; timeout_ms?: number }): Promise<Json>;
  /** Pointer reach for each selector's single match, for geometry reports. */
  reach(opts: { selectors: string[] }): Json;
  /** Starts recording what the next drive step changes on the page. */
  beginEffect(): Json;
  /** Reports the recording `id` started: attribute changes, nodes, requests, navigation, focus. */
  endEffect(opts: { id?: number }): Promise<Json>;
  /** Streams timeline telemetry through a host binding until stopped or the document unloads. */
  startRecording(opts: RecordOptions): Json;
  stopRecording(): Json;
  waitFor(opts: { selector?: string; text?: string; timeout_ms?: number }): Promise<Json>;
  waitIdle(opts?: { timeout_ms?: number; settle_ms?: number }): Promise<Json>;
}

const DEFAULT_AIM_TIMEOUT_MS = 10_000;
const MAX_FIELD_VALUE_REPORT = 512;

export const MAX_SNAPSHOT_NODES = 1_200;
const MAX_SNAPSHOT_DEPTH = 8;
const MAX_SNAPSHOT_CHILDREN = 30;
const MAX_SNAPSHOT_NAME = 120;
const MAX_TEXT_NODES_INSPECTED = 256;
const MAX_INTERACTIVE_NODES_INSPECTED = 5_000;
const MAX_URL_LENGTH = 2_048;
const MAX_TITLE_LENGTH = 256;

type SnapshotBudget = {
  remaining: number;
  nodeCount: number;
  truncated: boolean;
};

/** Elements whose text never renders: code, styles, and inert templates. */
const NON_RENDERING = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE"]);

function boundedText(root: Element, maxLength: number): string {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (node) =>
      node.parentElement && NON_RENDERING.has(node.parentElement.tagName) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
  });
  let out = "";
  let inspected = 0;
  while (out.length < maxLength && inspected < MAX_TEXT_NODES_INSPECTED) {
    const node = walker.nextNode();
    if (!node) break;
    inspected++;
    const text = (node.nodeValue ?? "").trim();
    if (!text) continue;
    if (out) out += " ";
    out += text.slice(0, maxLength - out.length);
  }
  return out.slice(0, maxLength);
}

function boundedAccessibleName(el: Element, maxLength: number): string {
  const labelledBy = el.getAttribute("aria-labelledby")?.slice(0, 1_024);
  if (labelledBy) {
    let joined = "";
    for (const id of labelledBy.split(/\s+/)) {
      const label = document.getElementById(id);
      if (!label) continue;
      const text = boundedText(label, maxLength - joined.length);
      if (text) joined += `${joined ? " " : ""}${text}`;
      if (joined.length >= maxLength) break;
    }
    if (joined) return joined.slice(0, maxLength);
  }
  const aria = (el.getAttribute("aria-label") ?? "").trim();
  if (aria) return aria.slice(0, maxLength);
  if (el instanceof SVGElement) {
    const titleEl = el.querySelector("title");
    if (titleEl && titleEl.textContent?.trim()) {
      return titleEl.textContent.trim().slice(0, maxLength);
    }
  }
  return boundedText(el, maxLength);
}

function interactiveMap(): { items: Json[]; truncated: boolean } {
  const out: Json[] = [];
  const selector = "button, a, input, textarea, select, [role=button], [role=link], [contenteditable=true], [data-testid]";
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT);
  let inspected = 0;
  while (inspected < MAX_INTERACTIVE_NODES_INSPECTED) {
    const node = walker.nextNode();
    if (!node) return { items: out, truncated: false };
    inspected++;
    if (!(node instanceof Element) || !node.matches(selector)) continue;
    const el = node;
    if (!isVisible(el)) continue;
    const name = boundedAccessibleName(el, 80);
    out.push({
      tag: el.tagName.toLowerCase(),
      role: el.getAttribute("role") ?? undefined,
      text: boundedText(el, 80) || undefined,
      name: name || undefined,
      testid: el.getAttribute("data-testid") || undefined,
      disabled: isDisabled(el),
    });
    if (out.length >= 40) return { items: out, truncated: true };
  }
  return { items: out, truncated: true };
}

function readCompactState(): Json {
  const pending = typeof window.__lycaonPending === "number" ? window.__lycaonPending : 0;
  const interactive = interactiveMap();
  return {
    url: location.href.slice(0, MAX_URL_LENGTH),
    title: document.title.slice(0, MAX_TITLE_LENGTH),
    busy: pending > 0,
    pending_requests: pending,
    interactive: interactive.items,
    interactive_truncated: interactive.truncated || undefined,
  };
}

function a11yNode(el: Element, depth: number, budget: SnapshotBudget): Json | null {
  if (depth > MAX_SNAPSHOT_DEPTH || budget.remaining <= 0) {
    budget.truncated = true;
    return null;
  }
  if (!(el instanceof Element)) return null;
  if (!isVisible(el) && el !== document.body) return null;
  budget.remaining--;
  budget.nodeCount++;
  const role = el.getAttribute("role") || el.tagName.toLowerCase();
  const name = boundedAccessibleName(el, MAX_SNAPSHOT_NAME);
  const kids: Json[] = [];
  let inspectedChildren = 0;
  for (const child of el.children) {
    if (inspectedChildren >= MAX_SNAPSHOT_CHILDREN || budget.remaining <= 0) {
      budget.truncated = true;
      break;
    }
    inspectedChildren++;
    const n = a11yNode(child, depth + 1, budget);
    if (n) kids.push(n);
  }
  // Children past the bounds are counted, so a partial list never reads as the whole.
  const omitted = el.children.length - inspectedChildren;
  return {
    role,
    name: name || undefined,
    testid: el.getAttribute("data-testid") || undefined,
    children: kids.length ? kids : undefined,
    children_omitted: omitted > 0 ? omitted : undefined,
  };
}

export function readSnapshot(): Json {
  const budget: SnapshotBudget = {
    remaining: MAX_SNAPSHOT_NODES,
    nodeCount: 0,
    truncated: false,
  };
  return {
    url: location.href.slice(0, MAX_URL_LENGTH),
    title: document.title.slice(0, MAX_TITLE_LENGTH),
    tree: a11yNode(document.body, 0, budget),
    node_count: budget.nodeCount,
    truncated: budget.truncated || undefined,
  };
}

function locatorView(opts: TargetOpts): Json {
  const out: Json = {};
  if (opts.testid) out.testid = opts.testid;
  if (opts.selector) out.selector = opts.selector;
  if (opts.role) out.role = opts.role;
  if (opts.label) out.label = opts.label;
  if (opts.text) out.text = opts.text;
  return out;
}

/** Install network-quiet + DOM-mutation counters used by waitIdle. */
export function installIdleHooks(): void {
  if (typeof window.__lycaonPending === "number") return;
  window.__lycaonPending = 0;
  window.__lycaonMutations = 0;
  const origFetch = window.fetch.bind(window);
  window.fetch = ((...args: Parameters<typeof fetch>) => {
    window.__lycaonPending = (window.__lycaonPending ?? 0) + 1;
    const [input, init] = args;
    const request = input instanceof Request ? input : null;
    const entry = logRequest(init?.method ?? request?.method ?? "GET", request ? request.url : String(input));
    return origFetch(...args)
      .then(
        (response) => {
          entry.status = response.status;
          return response;
        },
        (err: unknown) => {
          entry.failed = true;
          throw err;
        },
      )
      .finally(() => {
        window.__lycaonPending = Math.max(0, (window.__lycaonPending ?? 1) - 1);
      });
  }) as typeof fetch;
  const OrigOpen = window.XMLHttpRequest.prototype.open;
  window.XMLHttpRequest.prototype.open = function (this: XMLHttpRequest, method: string, url: string | URL, ...rest: unknown[]): void {
    const entry = logRequest(method, String(url));
    this.addEventListener("loadend", () => {
      window.__lycaonPending = Math.max(0, (window.__lycaonPending ?? 1) - 1);
      if (this.status) entry.status = this.status;
      else entry.failed = true;
    });
    window.__lycaonPending = (window.__lycaonPending ?? 0) + 1;
    return (OrigOpen as (this: XMLHttpRequest, method: string, url: string | URL, ...rest: unknown[]) => void).call(
      this,
      method,
      url,
      ...rest,
    );
  } as typeof XMLHttpRequest.prototype.open;
  const obs = new MutationObserver(() => {
    window.__lycaonMutations = (window.__lycaonMutations ?? 0) + 1;
  });
  obs.observe(document.documentElement, { childList: true, subtree: true, attributes: true, characterData: true });
}

const EDITABLE_SELECTOR = "input:not([type=hidden]), textarea, [contenteditable=''], [contenteditable=true]";

function isEditable(el: Element): boolean {
  return (
    el instanceof HTMLTextAreaElement ||
    (el instanceof HTMLInputElement && el.type !== "hidden") ||
    (el instanceof HTMLElement && el.isContentEditable)
  );
}

function reachFailure(reach: PointerReach): Json {
  switch (reach.state) {
    case "covered":
      return { error: "target obscured", obscured_by: reach.covered_by };
    case "clipped":
      return { error: "target clipped", clipped_by: reach.clipped_by };
    case "outside_viewport":
      return { error: "target outside viewport" };
    default:
      return { error: "target not rendered" };
  }
}

/** Waits for element to be visible/enabled, reveals it, and resolves target input point. */
async function aimAt(
  opts: TargetOpts & { timeout_ms?: number },
  require: AimRequirement,
  pick: (el: HTMLElement) => HTMLElement | null,
): Promise<Json> {
  const timeoutMs = opts.timeout_ms ?? DEFAULT_AIM_TIMEOUT_MS;
  const deadline = Date.now() + timeoutMs;
  let last: Json = { error: "target not found" };
  while (Date.now() < deadline) {
    const found = findTarget(opts);
    const el = found ? pick(found) : null;
    if (!found) {
      last = { error: "target not found" };
    } else if (!el) {
      last = { error: "not a fillable field" };
    } else if (!isVisible(el)) {
      last = { error: "target not visible" };
    } else if (isDisabled(el)) {
      last = { error: "control not enabled" };
    } else {
      noteAimedElement(el);
      const reach = revealForPointer(el);
      if (reach.state === "receives" || (require === "visible" && reach.state === "covered")) {
        const box = el.getBoundingClientRect();
        return {
          ok: true,
          point: reach.point,
          reach,
          target: describeElement(el),
          rect: { x: box.x, y: box.y, width: box.width, height: box.height },
        };
      }
      last = reachFailure(reach);
    }
    await sleep(100);
  }
  return { ok: false, ...last, locators: locatorView(opts), state: readCompactState() };
}

export function installLycaonDriver(): LycaonDriverApi {
  installIdleHooks();
  if (window.__lycaonDriver) return window.__lycaonDriver;

  const api: LycaonDriverApi = {
    state: () => readCompactState(),
    snapshot: () => readSnapshot(),

    aim: (opts) => aimAt(opts, opts.require ?? "receive", (el) => el),

    aimField: (opts) =>
      aimAt(opts, "receive", (el) => (isEditable(el) ? el : el.querySelector<HTMLElement>(EDITABLE_SELECTOR) ?? null)),

    selectFocusedContents() {
      const el = document.activeElement;
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
        el.select();
        return { ok: true, empty: el.value === "" };
      }
      if (el instanceof HTMLElement && el.isContentEditable) {
        const range = document.createRange();
        range.selectNodeContents(el);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
        return { ok: true, empty: (el.textContent ?? "") === "" };
      }
      return { ok: false, error: "no focused field" };
    },

    focusedField() {
      const el = document.activeElement;
      if (!(el instanceof HTMLElement) || !isEditable(el)) return { editable: false };
      const value = el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement ? el.value : el.textContent ?? "";
      return {
        editable: true,
        field: describeElement(el),
        value: value.slice(0, MAX_FIELD_VALUE_REPORT),
        value_truncated: value.length > MAX_FIELD_VALUE_REPORT || undefined,
      };
    },

    async select(opts) {
      const found = await waitUntil(() => !!findTarget(opts), {
        timeoutMs: opts.timeout_ms ?? DEFAULT_AIM_TIMEOUT_MS,
        label: "select target",
      });
      const target = findTarget(opts);
      if (!found.ok || !target) {
        return { ok: false, error: "select target not found", locators: locatorView(opts), state: readCompactState() };
      }
      const el = target instanceof HTMLSelectElement ? target : target.querySelector("select");
      if (!(el instanceof HTMLSelectElement)) {
        return { ok: false, error: "not a select control", locators: locatorView(opts), state: readCompactState() };
      }
      if (isDisabled(el)) {
        return { ok: false, error: "control not enabled", locators: locatorView(opts), state: readCompactState() };
      }
      noteAimedElement(el);
      const want = opts.value.trim();
      const option =
        [...el.options].find((o) => o.value === opts.value) ??
        [...el.options].find((o) => o.label.trim().toLowerCase() === want.toLowerCase());
      if (!option) {
        return {
          ok: false,
          error: "no matching option",
          options: [...el.options].slice(0, 20).map((o) => ({ value: o.value, label: o.label })),
          locators: locatorView(opts),
          state: readCompactState(),
        };
      }
      el.focus();
      el.value = option.value;
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
      return { ok: true, selected: { value: option.value, label: option.label }, state: readCompactState() };
    },

    beginEffect: () => beginEffect(),
    endEffect: (opts) => endEffect(opts),

    startRecording: (opts) => startRecording(opts),
    stopRecording: () => stopRecording(),

    reach(opts) {
      return {
        elements: opts.selectors.map((selector) => {
          let el: Element | null = null;
          try {
            el = document.querySelector(selector);
          } catch {
            el = null;
          }
          return { selector, reach: el ? pointerReach(el) : { state: "not_rendered" } };
        }),
      };
    },

    async waitFor(opts) {
      const timeoutMs = opts.timeout_ms ?? 30_000;
      if (opts.selector) {
        return waitUntil(() => !!resolve(opts.selector!), { timeoutMs, label: `selector ${opts.selector}` });
      }
      if (opts.text) {
        return waitUntil(() => (document.body.innerText ?? "").includes(opts.text!), {
          timeoutMs,
          label: `text "${opts.text}"`,
        });
      }
      return { ok: false, error: "wait_for requires selector or text" };
    },

    async waitIdle(opts = {}) {
      const timeoutMs = opts.timeout_ms ?? 30_000;
      const settleMs = opts.settle_ms ?? 400;
      let lastMut = window.__lycaonMutations ?? 0;
      return waitUntil(
        () => {
          const pending = window.__lycaonPending ?? 0;
          if (pending > 0) return false;
          const mut = window.__lycaonMutations ?? 0;
          if (mut !== lastMut) {
            lastMut = mut;
            return false;
          }
          return true;
        },
        { timeoutMs, settleMs, label: "idle" },
      ).then((r) => ({ ...r, state: readCompactState() }));
    },
  };

  window.__lycaonDriver = api;
  return api;
}
