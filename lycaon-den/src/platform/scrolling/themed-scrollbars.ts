import { measureSync } from "../../chat/stream/den-main-thread-perf.ts";
import { FrameMeasurements } from "../../layout/frame-measurements.ts";
import { observeSharedContentBox, type SharedContentBox } from "../../layout/shared-resize-observer.ts";
import {
  OverlayScrollbars,
  type PartialOptions,
  type ScrollbarElements,
} from "overlayscrollbars";
import { documentStyleNonce } from "../csp-nonce.ts";
import { preservingSelection } from "../interaction/selection-lease.ts";
import { bindOverlayScrollbarAutoHide } from "./overlay-scrollbar-autohide.ts";
import { bindOverlayScrollbarInput, type ScrollbarAxisModel, type ScrollbarInput } from "./overlay-scrollbar-input.ts";
import { ScrollbarUpdates } from "./scrollbar-updates.ts";
import {
  DEN_SCROLLPORT_INPUT_EVENT,
  bindScrollportMotion,
  bindScrollportNativeInput,
  scrollportMotionForHost,
  unbindScrollportMotion,
} from "./scrollport-motion.ts";

/**
 * Every themed scroll surface is a frame around the element that scrolls:
 *
 *   .den-scrollport[data-den-scrollport=<axis>]   frame: layout box, carries the scrollbar chrome
 *     > .den-scrollport__viewport                 the scroll container
 *       > .den-scrollport__content                the scroll extent
 *
 * The frame carries the scrollbar chrome outside the viewport so the thumb
 * stays stable during off-thread scrolling.
 */
export const DEN_SCROLLPORT_CLASS = "den-scrollport";
export const DEN_SCROLLPORT_VIEWPORT_CLASS = "den-scrollport__viewport";
export const DEN_SCROLLPORT_CONTENT_CLASS = "den-scrollport__content";
/** Marks a frame and names the axes its viewport scrolls. */
export const DEN_SCROLLPORT_AXIS_ATTR = "data-den-scrollport";
/** Frames repeated through long documents attach only once they approach view. */
export const DEN_SCROLLPORT_DEFER_ATTR = "data-den-scrollport-defer";

export type ScrollportAxis = "x" | "y" | "both";

const SCROLLPORT_FRAME_SELECTOR = `[${DEN_SCROLLPORT_AXIS_ATTR}]`;
const EDITOR_SCROLL_BOUNDARY_SELECTOR = ".cm-editor";
const ATTACHMENTS_PER_FRAME = 4;
const ATTACHMENT_FRAME_BUDGET_MS = 4;

export const DEN_SCROLL_DIRECTION_ATTR = "data-overlayscrollbars-direction";

const EXITING_STAGE_CLASS = "den-exit-fade";
const INACTIVE_SCROLLBAR_SELECTOR = `.${EXITING_STAGE_CLASS}, [data-resident="idle"], [data-resident-portal="idle"]`;

let scrollbarNonceApplied = false;

function ensureScrollbarNonce(): void {
  if (scrollbarNonceApplied) return;
  scrollbarNonceApplied = true;
  const nonce = documentStyleNonce();
  if (nonce) OverlayScrollbars.nonce(nonce);
}

function overflowFor(axis: ScrollportAxis): PartialOptions["overflow"] {
  return {
    x: axis === "y" ? "hidden" : "scroll",
    y: axis === "x" ? "hidden" : "scroll",
  };
}

function scrollbarOptionsFor(axis: ScrollportAxis): PartialOptions {
  return {
    overflow: overflowFor(axis),
    update: {
      // Frames publish their own geometry, so content churn never reaches the library.
      ignoreMutation: () => true,
      debounce: { mutation: [120, 250], resize: 0, event: [33, 99], env: [222, 666] },
      flowDirectionStyles: () => ({}),
    },
    scrollbars: {
      theme: "os-theme-den",
      visibility: "auto",
      // `bindOverlayScrollbarAutoHide` drives reveal and fade.
      autoHide: "never",
      // `bindOverlayScrollbarInput` handles thumb drags and track clicks.
      dragScroll: false,
      clickScroll: false,
    },
  };
}

type ScrollbarInstance = ReturnType<typeof OverlayScrollbars>;

const activeHosts = new Set<HTMLElement>();
/** Hosts a component attached itself; they survive temporary detachment. */
const retainedHosts = new WeakSet<HTMLElement>();
const pendingHosts = new WeakSet<HTMLElement>();
const geometrySignatures = new WeakMap<HTMLElement, string>();
const verticalModels = new WeakMap<HTMLElement, ScrollbarAxisModel>();
const chromePlacements = new FrameMeasurements(
  (host: HTMLElement) => host.isConnected && activeHosts.has(host) && !isMeasurementHeld(host)
    ? readThemedScrollbarGeometry(host) : undefined,
  (host, geometry) => { if (geometry) placeScrollbarChrome(host, geometry); },
);
const updates = new ScrollbarUpdates(
  (host) => activeHosts.has(host) && host.isConnected &&
    !isHostQuiet(host) && !directInputHosts.has(host),
  (host) => measureSync("scrollbar.update", () => {
    const instance = instanceFor(host);
    if (sleepingHosts.delete(host)) instance?.sleep(false);
    else instance?.update();
  }),
);
const heldAttachments = new Set<HTMLElement>();
let enqueueAttachment: ((host: HTMLElement) => void) | undefined;
const hostCleanups = new WeakMap<HTMLElement, () => void>();
const inputBindings = new WeakMap<HTMLElement, ScrollbarInput>();
const quietDepthByHost = new Map<HTMLElement, number>();
const directInputHosts = new Set<HTMLElement>();
const directInputTimers = new Map<HTMLElement, ReturnType<typeof setTimeout>>();
const sleepingHosts = new Set<HTMLElement>();
const DIRECT_INPUT_RECHECK_MS = 200;

function instanceFor(host: HTMLElement): ScrollbarInstance | undefined {
  return OverlayScrollbars(host) ?? undefined;
}

function markStaticDirection(host: HTMLElement): void {
  if (!host.hasAttribute(DEN_SCROLL_DIRECTION_ATTR)) {
    host.setAttribute(DEN_SCROLL_DIRECTION_ATTR, "ltr");
  }
}

function untrackHost(host: HTMLElement): void {
  chromePlacements.cancel(host);
  hostCleanups.get(host)?.();
  hostCleanups.delete(host);
  inputBindings.delete(host);
  unbindScrollportMotion(host);
  activeHosts.delete(host);
  updates.forget(host);
  heldAttachments.delete(host);
  if (deferredHosts.delete(host)) visibilityObserver?.unobserve(host);
  visibleDeferredHosts.delete(host);
  for (const hold of quietHolds) hold.hosts.delete(host);
  quietDepthByHost.delete(host);
  sleepingHosts.delete(host);
  directInputHosts.delete(host);
  const inputTimer = directInputTimers.get(host);
  if (inputTimer !== undefined) clearTimeout(inputTimer);
  directInputTimers.delete(host);
  geometrySignatures.delete(host);
  verticalModels.delete(host);
}

type ScrollMeasureQuietHold = {
  readonly scope: Element;
  readonly hosts: Set<HTMLElement>;
};

const quietHolds = new Set<ScrollMeasureQuietHold>();

/** Covers hosts that contain the scope or sit inside it. */
function holdCovers(
  hold: ScrollMeasureQuietHold,
  host: HTMLElement,
): boolean {
  const scope = hold.scope;
  return (
    host === scope ||
    host.contains(scope) ||
    scope.contains(host)
  );
}

function setHostSleeping(host: HTMLElement, sleeping: boolean): void {
  const depth = quietDepthByHost.get(host) ?? 0;
  const next = sleeping ? depth + 1 : depth - 1;
  if (next <= 0) quietDepthByHost.delete(host);
  else quietDepthByHost.set(host, next);
  applyHostSleep(host);
}

function applyHostSleep(host: HTMLElement): void {
  const sleeping = isMeasurementHeld(host);
  if (sleeping && !sleepingHosts.has(host)) {
    sleepingHosts.add(host);
    instanceFor(host)?.sleep(true);
  } else if (!sleeping && sleepingHosts.has(host)) {
    if (directInputHosts.has(host)) {
      sleepingHosts.delete(host);
      instanceFor(host)?.sleep(false);
    } else {
      // Deferred waking shares layout with the next scheduled update.
      updates.request(host);
    }
  }
  updates.resume(host);
}

function noteDirectInput(host: HTMLElement): void {
  if (!directInputHosts.has(host)) {
    directInputHosts.add(host);
    applyHostSleep(host);
  }
  // One recheck timer per host re-arms while the scrollport still reports input.
  if (directInputTimers.has(host)) return;
  directInputTimers.set(
    host,
    setTimeout(() => {
      directInputTimers.delete(host);
      if (scrollportMotionForHost(host)?.isDirectInputActive()) {
        noteDirectInput(host);
        return;
      }
      directInputHosts.delete(host);
      applyHostSleep(host);
    }, DIRECT_INPUT_RECHECK_MS),
  );
}

function connectedHosts(): HTMLElement[] {
  const hosts: HTMLElement[] = [];
  for (const host of activeHosts) {
    // Retained hosts may remain detached.
    if (!instanceFor(host)) {
      activeHosts.delete(host);
      continue;
    }
    if (host.isConnected) hosts.push(host);
  }
  return hosts;
}

export function beginScrollMeasureQuiet(scope: Element): () => void {
  const hold: ScrollMeasureQuietHold = { scope, hosts: new Set() };
  quietHolds.add(hold);
  for (const host of connectedHosts()) sleepUnderHold(hold, host);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    quietHolds.delete(hold);
    for (const host of hold.hosts) setHostSleeping(host, false);
    hold.hosts.clear();
    flushHeldAttachments();
  };
}

/** Attaches deferred frames after their last hold ends. */
function flushHeldAttachments(): void {
  if (heldAttachments.size === 0) return;
  for (const frame of [...heldAttachments]) {
    if (!frame.isConnected) {
      heldAttachments.delete(frame);
      continue;
    }
    if (attachHeldFor(frame) || frame.closest(INACTIVE_SCROLLBAR_SELECTOR)) continue;
    heldAttachments.delete(frame);
    if (enqueueAttachment) enqueueAttachment(frame);
    else attachScrollportFrame(frame);
  }
}

function sleepUnderHold(
  hold: ScrollMeasureQuietHold,
  host: HTMLElement,
): void {
  if (hold.hosts.has(host) || !holdCovers(hold, host)) return;
  hold.hosts.add(host);
  setHostSleeping(host, true);
}

function applyActiveQuietTo(host: HTMLElement): void {
  for (const hold of quietHolds) sleepUnderHold(hold, host);
}

function isHostQuiet(host: HTMLElement): boolean {
  return (quietDepthByHost.get(host) ?? 0) > 0 || !!host.closest(INACTIVE_SCROLLBAR_SELECTOR);
}

function isMeasurementHeld(host: HTMLElement): boolean {
  return !!host.closest(INACTIVE_SCROLLBAR_SELECTOR) || (isHostQuiet(host) && !directInputHosts.has(host));
}

export function isScrollMeasureQuietForTests(): boolean {
  return quietHolds.size > 0;
}

export function resetScrollMeasureQuietForTests(): void {
  chromePlacements.clear();
  for (const host of quietDepthByHost.keys()) instanceFor(host)?.sleep(false);
  quietDepthByHost.clear();
  sleepingHosts.clear();
  directInputHosts.clear();
  for (const timer of directInputTimers.values()) clearTimeout(timer);
  directInputTimers.clear();
  quietHolds.clear();
  updates.clear();
  heldAttachments.clear();
  for (const host of deferredHosts) visibilityObserver?.unobserve(host);
  deferredHosts.clear();
}

/** Runs chrome placement in the host's own read and write phases. */
type ScrollbarChromeScheduler = (
  read: () => ScrollbarGeometry | undefined,
  write: (geometry: ScrollbarGeometry | undefined) => void,
) => void;

export type ThemedViewportScrollbarOptions = {
  /** Axes the viewport scrolls; both when omitted. */
  axis?: ScrollportAxis;
  /** A logical range the host publishes instead of native viewport geometry. */
  vertical?: ScrollbarAxisModel;
  /** Hosts with their own measure pass place scroll-driven chrome inside it. */
  schedule?: ScrollbarChromeScheduler;
  /** Content whose border box spans the scroll extent; its resizes publish geometry. */
  extent?: HTMLElement;
};

/**
 * Publishes geometry when the viewport or its extent changes size along a scrolled axis.
 * Sizes across an unscrolled axis never move the range, so a width drag leaves a vertical
 * scrollport's library state alone.
 */
function observeExtent(
  host: HTMLElement,
  viewport: HTMLElement,
  extent: HTMLElement,
  axis: ScrollportAxis,
): () => void {
  const sizes = new Map<Element, string>();
  const publish = (element: Element) => (box: SharedContentBox) => {
    const width = Math.round(box.borderWidth ?? box.width);
    const height = Math.round(box.borderHeight ?? box.height);
    sizes.set(element, axis === "y" ? `${height}` : axis === "x" ? `${width}` : `${width}x${height}`);
    updateThemedViewportScrollbar(host, `${sizes.get(viewport) ?? ""}:${sizes.get(extent) ?? ""}`);
  };
  const stopViewport = observeSharedContentBox(viewport, publish(viewport));
  const stopExtent = observeSharedContentBox(extent, publish(extent));
  return () => {
    stopViewport();
    stopExtent();
  };
}

/** Binds scrollbar chrome in `host` to the `viewport` it frames. */
function bindScrollbar(
  host: HTMLElement,
  viewport: HTMLElement,
  options: ThemedViewportScrollbarOptions,
): ScrollbarInstance {
  const { vertical, schedule } = options;
  ensureScrollbarNonce();
  markStaticDirection(host);
  activeHosts.add(host);
  // Preserving selection prevents focus restoration from clearing the active selection.
  const instance = measureSync("scrollbar.attach", () => preservingSelection(() =>
    OverlayScrollbars(
      { target: host, elements: { viewport } },
      scrollbarOptionsFor(options.axis ?? "both"),
    )));
  const elements = instance.elements();
  if (vertical) {
    verticalModels.set(host, vertical);
    elements.scrollbarVertical.scrollbar.classList.add("den-scrollbar-logical");
  }
  const scrollViewport = elements.scrollOffsetElement ?? viewport;
  const motion = bindScrollportMotion(
    host,
    scrollViewport,
    options.extent ?? elements.content ?? scrollViewport,
  );
  const motionElements = { ...elements, host, scrollOffsetElement: scrollViewport };
  const input = bindOverlayScrollbarInput(motionElements, motion, vertical);
  inputBindings.set(host, input);
  const stopAutoHide = bindOverlayScrollbarAutoHide(motionElements, motion);
  const stopNativeInput = bindScrollportNativeInput(motion);
  // A scroll that moved nothing skips the host's measure pass; attach cannot read the offset.
  let placedOffset: number | undefined;
  const placeChrome = schedule
    ? () => {
      const offset = motion.offsetY();
      const horizontal = instanceFor(host)?.state().hasOverflow?.x === true;
      if (!horizontal && offset === placedOffset) return;
      placedOffset = offset;
      schedule(
        () => chromePlacements.measure(host),
        // Re-applying thumb input here would re-enter the host's scroll pipeline.
        (geometry) => { if (geometry) placeScrollbarChrome(host, geometry, { reapplyInput: false }); },
      );
    }
    : () => chromePlacements.request(host);
  const requestPlaceChrome = () => {
    if (schedule) placeChrome();
    else chromePlacements.request(host);
  };
  const stopExtent = motion.subscribeExtent(requestPlaceChrome);
  const stopCommits = vertical ? motion.subscribeCommits(requestPlaceChrome) : undefined;
  const stopContentExtent = options.extent
    ? observeExtent(host, viewport, options.extent, options.axis ?? "both")
    : undefined;
  const onDirectInput = () => noteDirectInput(host);
  host.addEventListener(DEN_SCROLLPORT_INPUT_EVENT, onDirectInput);
  scrollViewport.addEventListener("scroll", placeChrome, { passive: true });
  hostCleanups.set(host, () => {
    input.dispose();
    stopAutoHide();
    stopNativeInput();
    stopExtent();
    stopCommits?.();
    stopContentExtent?.();
    host.removeEventListener(DEN_SCROLLPORT_INPUT_EVENT, onDirectInput);
    scrollViewport.removeEventListener("scroll", placeChrome);
  });
  placeScrollbarChrome(host);
  // Quiet holds apply after the scrollbar instance exists.
  applyActiveQuietTo(host);
  return instance;
}

/**
 * Attaches chrome for a viewport whose host renders it outside the scroll container.
 * The caller holds the binding; it survives the host leaving the document.
 */
export function attachThemedViewportScrollbar(
  host: HTMLElement,
  viewport: HTMLElement,
  options: ThemedViewportScrollbarOptions = {},
): () => void {
  retainedHosts.add(host);
  const instance = bindScrollbar(host, viewport, options);
  return () => {
    retainedHosts.delete(host);
    untrackHost(host);
    preservingSelection(() => instance.destroy());
  };
}

export function verticalScrollbarChromeFor(
  host: HTMLElement,
): ScrollbarElements | undefined {
  return instanceFor(host)?.elements().scrollbarVertical;
}

/** Unchanged geometry skips the explicit refresh. */
export function updateThemedViewportScrollbar(
  host: HTMLElement,
  geometrySignature: string | number,
  geometry?: ScrollbarGeometry,
): void {
  if (!instanceFor(host)) return;
  const next = String(geometrySignature);
  if (geometrySignatures.get(host) === next) return;
  geometrySignatures.set(host, next);
  placeScrollbarChrome(host, geometry);
  updates.request(host);
}

function attachHeldFor(frame: HTMLElement): boolean {
  for (const hold of quietHolds) {
    if (holdCovers(hold, frame)) return true;
  }
  return false;
}

export type ScrollportFrameParts = {
  axis: ScrollportAxis;
  viewport: HTMLElement;
  content: HTMLElement;
};

function isScrollportAxis(value: string | null): value is ScrollportAxis {
  return value === "x" || value === "y" || value === "both";
}

/** The frame's viewport and content, or undefined when the markup breaks the contract. */
export function scrollportFrameParts(frame: HTMLElement): ScrollportFrameParts | undefined {
  const axis = frame.getAttribute(DEN_SCROLLPORT_AXIS_ATTR);
  const viewport = frame.firstElementChild;
  const content = viewport?.firstElementChild;
  if (
    !isScrollportAxis(axis) ||
    !(viewport instanceof HTMLElement) || !viewport.classList.contains(DEN_SCROLLPORT_VIEWPORT_CLASS) ||
    !(content instanceof HTMLElement) || !content.classList.contains(DEN_SCROLLPORT_CONTENT_CLASS)
  ) {
    return undefined;
  }
  return { axis, viewport, content };
}

/** Wraps `content` in scrollport markup, for HTML built outside Solid such as rendered markdown. */
export function wrapInScrollportFrame(content: HTMLElement, options: {
  frameClass: string;
  axis: ScrollportAxis;
  defer?: boolean;
  viewportAttributes?: Record<string, string>;
}): HTMLDivElement {
  const doc = content.ownerDocument;
  const frame = doc.createElement("div");
  frame.className = `${DEN_SCROLLPORT_CLASS} ${options.frameClass}`;
  frame.setAttribute(DEN_SCROLLPORT_AXIS_ATTR, options.axis);
  if (options.defer) frame.setAttribute(DEN_SCROLLPORT_DEFER_ATTR, "");
  const viewport = doc.createElement("div");
  viewport.className = DEN_SCROLLPORT_VIEWPORT_CLASS;
  for (const [name, value] of Object.entries(options.viewportAttributes ?? {})) {
    viewport.setAttribute(name, value);
  }
  content.replaceWith(frame);
  content.classList.add(DEN_SCROLLPORT_CONTENT_CLASS);
  viewport.append(content);
  frame.append(viewport);
  return frame;
}

let visibilityObserver: IntersectionObserver | undefined;
const deferredHosts = new Set<HTMLElement>();
const visibleDeferredHosts = new WeakSet<HTMLElement>();

/** Attaches before a deferred frame enters the viewport. */
function attachWhenVisible(frame: HTMLElement): void {
  if (deferredHosts.has(frame)) return;
  if (!visibilityObserver) {
    visibilityObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          const host = entry.target as HTMLElement;
          visibleDeferredHosts.add(host);
          visibilityObserver?.unobserve(host);
          deferredHosts.delete(host);
          if (host.isConnected) {
            if (enqueueAttachment) enqueueAttachment(host);
            else attachScrollportFrame(host);
          }
        }
      },
      { rootMargin: "200px" },
    );
  }
  deferredHosts.add(frame);
  visibilityObserver.observe(frame);
}

/** Attaches a frame found in the document; its binding ends when the frame leaves. */
function attachScrollportFrame(frame: HTMLElement): boolean {
  if (instanceFor(frame)) return false;
  if (attachHeldFor(frame) || frame.closest(INACTIVE_SCROLLBAR_SELECTOR)) {
    heldAttachments.add(frame);
    return false;
  }
  if (frame.hasAttribute(DEN_SCROLLPORT_DEFER_ATTR) && !visibleDeferredHosts.has(frame) &&
    typeof IntersectionObserver !== "undefined") {
    attachWhenVisible(frame);
    return false;
  }
  const parts = scrollportFrameParts(frame);
  if (!parts) {
    console.error("A scrollport frame must wrap .den-scrollport__viewport > .den-scrollport__content.", frame);
    return false;
  }
  bindScrollbar(frame, parts.viewport, { axis: parts.axis, extent: parts.content });
  return true;
}

function detachScrollportFrame(frame: HTMLElement): void {
  if (deferredHosts.delete(frame)) visibilityObserver?.unobserve(frame);
  // Unwrapping restores focus the same way attaching does; keep the selection.
  preservingSelection(() => instanceFor(frame)?.destroy());
  untrackHost(frame);
}

const VIEWPORT_PERCENT_PROPERTY = "--os-viewport-percent";
const SCROLL_PERCENT_PROPERTY = "--os-scroll-percent";

/** Matches the scrollbar library's CSS geometry precision. */
function roundCssNumber(value: number): number {
  return Math.round(value * 1e4) / 1e4;
}

/** Visible fraction of the scroll range used to size the handle. */
function viewportPercent(clientSize: number, scrollSize: number): string {
  return String(roundCssNumber(scrollSize > 0 ? Math.min(1, clientSize / scrollSize) : 1));
}

/** Position of the handle along its track, as the library's scroll percent. */
function scrollPercent(offset: number, range: number): string {
  const fraction = range > 0 ? offset / range : 0;
  return String(roundCssNumber(Math.max(0, Math.min(1, fraction || 0))));
}

/** With a scroll timeline the library animates handle offsets and never publishes the percent. */
function handleOffsetsPublished(): boolean {
  return typeof ScrollTimeline === "undefined";
}

function setStyleValue(
  element: HTMLElement | undefined,
  property: string,
  value: string,
): void {
  if (element && element.style.getPropertyValue(property) !== value) {
    element.style.setProperty(property, value);
  }
}

export type ScrollbarGeometry = {
  verticalPercent: string;
  verticalPosition?: string;
  horizontalPercent: string;
  /** Handle offsets along each track; absent where a scroll timeline drives them. */
  handles?: { x: string; y: string };
};

/** Geometry snapshot for the enclosing layout read phase. */
export function readThemedScrollbarGeometry(host: HTMLElement): ScrollbarGeometry {
  const instance = instanceFor(host);
  const viewport = instance?.elements().scrollOffsetElement ?? host;
  const logical = verticalModels.get(host)?.read();
  // Non-overflowing logical hosts need no layout reads.
  const horizontalOverflow = !logical || instance?.state().hasOverflow?.x !== false;
  const handles = handleOffsetsPublished();
  const logicalRange = logical ? Math.max(1, logical.extent - logical.viewport) : 0;
  const verticalPosition = logical ? String(Math.max(0, Math.min(1, logical.offset / logicalRange))) : undefined;
  const clientHeight = logical ? 0 : viewport.clientHeight;
  const scrollHeight = logical ? 0 : viewport.scrollHeight;
  const clientWidth = horizontalOverflow ? viewport.clientWidth : 0;
  const scrollWidth = horizontalOverflow ? viewport.scrollWidth : 0;
  const geometry: ScrollbarGeometry = {
    verticalPercent: logical ? viewportPercent(logical.viewport, logical.extent)
      : viewportPercent(clientHeight, scrollHeight),
    verticalPosition,
    horizontalPercent: horizontalOverflow ? viewportPercent(clientWidth, scrollWidth) : "1",
  };
  if (handles) {
    geometry.handles = {
      x: horizontalOverflow ? scrollPercent(viewport.scrollLeft, scrollWidth - clientWidth) : "0",
      y: logical ? scrollPercent(logical.offset, logicalRange) : scrollPercent(viewport.scrollTop, scrollHeight - clientHeight),
    };
  }
  return geometry;
}

/** Keeps scrollbar geometry current during direct input. */
function placeScrollbarChrome(
  host: HTMLElement,
  geometry?: ScrollbarGeometry,
  options: { reapplyInput?: boolean } = {},
): void {
  if (isMeasurementHeld(host)) return;
  const elements = instanceFor(host)?.elements();
  const vertical = elements?.scrollbarVertical?.scrollbar;
  const horizontal = elements?.scrollbarHorizontal?.scrollbar;
  if (!vertical && !horizontal) return;
  const moved = options.reapplyInput === false ? false : inputBindings.get(host)?.refreshGeometry();
  const measured = !moved && geometry ? geometry : readThemedScrollbarGeometry(host);
  setStyleValue(vertical, VIEWPORT_PERCENT_PROPERTY, measured.verticalPercent);
  if (measured.verticalPosition !== undefined) {
    setStyleValue(vertical, "--den-scrollbar-viewport", measured.verticalPercent);
    setStyleValue(vertical, "--den-scrollbar-position", measured.verticalPosition);
  }
  setStyleValue(horizontal, VIEWPORT_PERCENT_PROPERTY, measured.horizontalPercent);
  if (measured.handles) {
    setStyleValue(vertical, SCROLL_PERCENT_PROPERTY, measured.handles.y);
    setStyleValue(horizontal, SCROLL_PERCENT_PROPERTY, measured.handles.x);
  }
}

/** Attaches a frame now instead of in the next reconcile frame. */
export function syncThemedScrollbar(frame: HTMLElement): void {
  if (!frame.isConnected) {
    if (pendingHosts.has(frame)) return;
    pendingHosts.add(frame);
    queueMicrotask(() => {
      pendingHosts.delete(frame);
      if (frame.isConnected) attachScrollportFrame(frame);
    });
    return;
  }
  attachScrollportFrame(frame);
}

function framesUnder(root: ParentNode): HTMLElement[] {
  const frames: HTMLElement[] = [];
  if (root instanceof HTMLElement && root.matches(SCROLLPORT_FRAME_SELECTOR)) {
    frames.push(root);
  }
  if (root instanceof Element && root.closest(EDITOR_SCROLL_BOUNDARY_SELECTOR)) {
    return frames;
  }
  for (const frame of root.querySelectorAll<HTMLElement>(SCROLLPORT_FRAME_SELECTOR)) {
    if (!frame.closest(EDITOR_SCROLL_BOUNDARY_SELECTOR)) frames.push(frame);
  }
  return frames;
}

function collectFramesUnder(
  roots: Iterable<HTMLElement>,
  frames: Set<HTMLElement>,
): void {
  const candidates = new Set(roots);
  for (const root of candidates) {
    let parent = root.parentElement;
    while (parent && !candidates.has(parent)) parent = parent.parentElement;
    if (parent) continue;
    for (const frame of framesUnder(root)) frames.add(frame);
  }
}

function collectKnownFramesUnder(
  roots: readonly HTMLElement[],
  frames: Set<HTMLElement>,
): void {
  if (roots.length === 0) return;
  for (const frame of activeHosts) {
    if (!retainedHosts.has(frame) && (!frame.isConnected || frame.closest(EDITOR_SCROLL_BOUNDARY_SELECTOR))) {
      frames.add(frame);
    }
  }
  for (const frame of deferredHosts) {
    if (!frame.isConnected || frame.closest(EDITOR_SCROLL_BOUNDARY_SELECTOR)) {
      frames.add(frame);
    }
  }
}

function releaseDiscoveredFrames(): void {
  for (const frame of [...activeHosts]) {
    if (!retainedHosts.has(frame)) detachScrollportFrame(frame);
  }
  heldAttachments.clear();
  for (const frame of deferredHosts) visibilityObserver?.unobserve(frame);
  deferredHosts.clear();
  visibilityObserver?.disconnect();
  visibilityObserver = undefined;
}

/** Attaches scrollport frames as they enter the document and releases them as they leave. */
export function setupThemedScrollbars(): () => void {
  if (!document.getElementById("root")) return () => undefined;
  ensureScrollbarNonce();
  const observeRoot = document.body;
  const addedRoots = new Set<HTMLElement>([observeRoot]);
  const removedRoots = new Set<HTMLElement>();
  const attachments = new Set<HTMLElement>();
  const residentChanges = new Set<HTMLElement>();
  let frame: number | undefined;
  let stopped = false;
  const schedule = () => {
    if (frame !== undefined || stopped) return;
    frame = requestAnimationFrame(() => {
      frame = undefined;
      measureSync("scrollbar.reconcile", () => {
        for (const surface of residentChanges) {
          for (const host of activeHosts) {
            if (surface === host || surface.contains(host)) {
              applyHostSleep(host);
              updates.request(host);
            }
          }
        }
        residentChanges.clear();
        const changedFrames = new Set<HTMLElement>();
        collectFramesUnder(addedRoots, changedFrames);
        collectKnownFramesUnder([...removedRoots], changedFrames);
        addedRoots.clear();
        removedRoots.clear();
        for (const changed of changedFrames) {
          if (!observeRoot.contains(changed) || changed.closest(EDITOR_SCROLL_BOUNDARY_SELECTOR)) {
            if (!retainedHosts.has(changed)) detachScrollportFrame(changed);
          } else if (instanceFor(changed)) {
            applyHostSleep(changed);
          } else {
            attachments.add(changed);
          }
        }
        // Spread scrollbar attachment across frames.
        let attached = 0;
        const attachmentDeadline = performance.now() + ATTACHMENT_FRAME_BUDGET_MS;
        for (const host of attachments) {
          attachments.delete(host);
          if (!host.isConnected || host.closest(EDITOR_SCROLL_BOUNDARY_SELECTOR)) continue;
          if (attachScrollportFrame(host) &&
            (++attached === ATTACHMENTS_PER_FRAME || performance.now() >= attachmentDeadline)) break;
        }
        if (attachments.size) schedule();
        flushHeldAttachments();
      });
    });
  };

  enqueueAttachment = (host) => { attachments.add(host); schedule(); };
  const observer = new MutationObserver((mutations) => {
    for (const mutation of mutations) {
      const target = mutation.target instanceof Element ? mutation.target : mutation.target.parentElement;
      if (mutation.type === "attributes") {
        if (!(target instanceof HTMLElement)) continue;
        residentChanges.add(target);
        addedRoots.add(target);
        continue;
      }
      if (target?.closest(EDITOR_SCROLL_BOUNDARY_SELECTOR)) continue;
      // A frame's own children change only as the library adds or removes its chrome.
      if (target instanceof HTMLElement && target.matches(SCROLLPORT_FRAME_SELECTOR)) continue;
      if (target?.closest(".os-scrollbar")) continue;
      for (const node of mutation.addedNodes) {
        if (node instanceof HTMLElement && !node.classList.contains("os-scrollbar")) addedRoots.add(node);
      }
      for (const node of mutation.removedNodes) {
        if (node instanceof HTMLElement && !node.classList.contains("os-scrollbar")) removedRoots.add(node);
      }
    }
    if (addedRoots.size || removedRoots.size) schedule();
    updates.resumeConnected();
  });
  observer.observe(observeRoot, {
    childList: true, subtree: true,
    attributes: true, attributeFilter: ["data-resident", "data-resident-portal"],
  });
  schedule();

  return () => {
    stopped = true;
    enqueueAttachment = undefined;
    observer.disconnect();
    if (frame !== undefined) cancelAnimationFrame(frame);
    addedRoots.clear();
    removedRoots.clear();
    attachments.clear();
    residentChanges.clear();
    releaseDiscoveredFrames();
  };
}
