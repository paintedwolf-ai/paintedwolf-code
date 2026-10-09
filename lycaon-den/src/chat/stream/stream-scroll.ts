import { streamEngagement, isStreamScrollEngagementSuppressed } from "./reader-intent/reader-state.ts";
import {
  isShellLayoutUnstable,
  onShellLayoutSettled,
} from "../../shell/shell-layout-busy.ts";
import {
  cancelStreamSpringScroll,
  streamSpringScrollTo,
} from "./stream-scroll-spring.ts";
import {
  denScrollDebugLog,
} from "./den-scroll-debug.ts";
import {
  requireScrollportMotionForViewport,
  scrollportMotionForViewport,
  setScrollportTailPolicy,
  type ScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";
import { scrollportClientHeight, type ScrollportCommitSource } from "../../platform/scrolling/scrollport-motion-types.ts";
import { updateThemedViewportScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { cancelScrollportFrame, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { observeScrollportOffset } from "../../platform/scrolling/scrollport-offset.ts";

const streamTailPins = new WeakMap<HTMLElement, () => boolean>();
const streamTailPolicies = new WeakMap<HTMLElement, ScrollportMotion>();

/** Tail extent and pin state survive scrollport replacement. */
function applyStreamTailPolicy(streamTarget: HTMLElement): void {
  const motion = scrollportMotionForViewport(streamTarget);
  if (!motion) return;
  streamTailPolicies.set(streamTarget, motion);
  const pinned = streamTailPins.get(streamTarget);
  setScrollportTailPolicy(motion.host, {
    resolveTail: (naturalTail) =>
      streamRenderedTranscriptTailOffset(streamTarget) ?? naturalTail,
    ...(pinned ? { pinned } : {}),
  });
}

function ensureStreamTailPolicy(streamTarget: HTMLElement): void {
  if (streamTailPolicies.get(streamTarget) !== scrollportMotionForViewport(streamTarget)) {
    applyStreamTailPolicy(streamTarget);
  }
}

function streamScrollMotion(streamTarget: HTMLElement): ScrollportMotion {
  const motion = requireScrollportMotionForViewport(streamTarget);
  ensureStreamTailPolicy(streamTarget);
  return motion;
}

/** Sole application writer for a named stream scrollport. */
export function commitStreamScroll(
  streamTarget: HTMLElement,
  scrollTop: number,
  source: ScrollportCommitSource,
  opts?: { glide?: boolean; cancelMotion?: boolean },
): void {
  // Deferred callbacks may outlive their scrollport.
  if (!scrollportMotionForViewport(streamTarget)) return;
  const motion = streamScrollMotion(streamTarget);
  if (opts?.cancelMotion !== false && source !== "thumb_drag") {
    motion.cancelApplicationMotion();
  }
  if (opts?.glide) void motion.revealOffset(scrollTop);
  else motion.commit(scrollTop, source);
}

/** Moves to the last offset that shows content. */
export function commitStreamTail(
  streamTarget: HTMLElement,
  source: "jump" | "repin_tail",
): void {
  if (!scrollportMotionForViewport(streamTarget)) return;
  cancelStreamSpringScroll(streamTarget);
  commitStreamScroll(streamTarget, streamTailOffset(streamTarget), source, {
    cancelMotion: source === "jump",
  });
}

/** Glides to the live tail as it moves. Resolves `true` on arrival. */
export function glideStreamToTail(streamTarget: HTMLElement): Promise<boolean> {
  if (!scrollportMotionForViewport(streamTarget)) return Promise.resolve(false);
  streamScrollMotion(streamTarget);
  return streamSpringScrollTo(streamTarget, () => streamTailOffset(streamTarget));
}

/** Content shifts carry the offset without ending input. */
export function shiftStreamContent(
  streamTarget: HTMLElement,
  deltaY: number,
  fromOffset: number,
): number {
  const motion = scrollportMotionForViewport(streamTarget);
  if (!motion) return 0;
  const applied = motion.shiftContent(deltaY, fromOffset);
  const state = streamEngagement(streamTarget);
  // Content shifts move the sampling baseline with the reader.
  if (applied !== 0 && state.lastScrollTop !== undefined) {
    state.lastScrollTop += applied;
  }
  return applied;
}

/** Resolves the tail pin and the tail boundary before the next paint. */
export function reconcileStreamLayout(streamTarget: HTMLElement): void {
  if (!scrollportMotionForViewport(streamTarget)) return;
  streamScrollMotion(streamTarget).notifyLayoutMutated();
}

/** Keeps the stream on its live tail while `pin` holds, however it is bound. */
export function setStreamTailPin(
  streamTarget: HTMLElement,
  pin: (() => boolean) | null,
): void {
  if (pin) streamTailPins.set(streamTarget, pin);
  else streamTailPins.delete(streamTarget);
  applyStreamTailPolicy(streamTarget);
}

/** Pixels from the bottom treated as the live tail. */
export const STREAM_SCROLL_BOTTOM_THRESHOLD_PX = 80;

/** Pixels from the top before the top edge fade hides. */
const STREAM_SCROLL_TOP_THRESHOLD_PX = 4;

/** Attribution window for scroll events following reader input. */

function coalesceStreamScrollFrame(
  streamTarget: HTMLElement,
  fn: () => void,
): void {
  scheduleScrollportFrame(streamTarget, "observe", fn);
}

function isStreamNearTop(streamTarget: HTMLElement): boolean {
  return streamTarget.scrollTop <= STREAM_SCROLL_TOP_THRESHOLD_PX;
}

export function measureStreamOverflow(streamTarget: HTMLElement): boolean {
  return streamContentHeight(streamTarget) > streamTarget.clientHeight + 1;
}

const streamFadeQuietUntil = new WeakMap<HTMLElement, number>();

function streamWrapForFade(streamTarget: HTMLElement): HTMLElement | null {
  const wrap = streamTarget.closest(".den-chat-stream-wrap");
  return wrap instanceof HTMLElement ? wrap : null;
}

/** Hides edge fades immediately during session attachment. */
export function suppressStreamScrollFade(
  streamTarget: HTMLElement | undefined,
  durationMs: number,
): void {
  if (!streamTarget) return;
  const wrap = streamWrapForFade(streamTarget);
  if (!wrap) return;
  const until = Date.now() + Math.max(0, durationMs);
  const prev = streamFadeQuietUntil.get(wrap) ?? 0;
  streamFadeQuietUntil.set(wrap, Math.max(prev, until));
  wrap.toggleAttribute("data-fade-quiet", true);
}

function releaseStreamScrollFadeQuiet(wrap: HTMLElement): void {
  const until = streamFadeQuietUntil.get(wrap) ?? 0;
  if (Date.now() < until) return;
  streamFadeQuietUntil.delete(wrap);
  if (wrap.hasAttribute("data-fade-quiet")) {
    wrap.toggleAttribute("data-fade-quiet", false);
  }
}

/** Returns the last offset that still shows content. */
export function streamTailOffset(streamTarget: HTMLElement): number {
  const motion = scrollportMotionForViewport(streamTarget);
  if (motion) {
    ensureStreamTailPolicy(streamTarget);
    return motion.extent.tailOffsetY();
  }
  const naturalTail = Math.max(0, streamTarget.scrollHeight - scrollportClientHeight(streamTarget));
  const renderedTail = streamRenderedTranscriptTailOffset(streamTarget);
  if (renderedTail === null) return naturalTail;
  return Math.max(0, Math.min(naturalTail, renderedTail));
}

/** Resolve the explicit content end, excluding any range published past it. */
function streamRenderedTranscriptTailOffset(streamTarget: HTMLElement): number | null {
  const inner = streamTarget.querySelector<HTMLElement>(
    ".den-chat-stream-inner",
  );
  if (!inner) return null;

  const transcriptEnd = inner.querySelector<HTMLElement>(
    "[data-transcript-end]",
  );
  if (!transcriptEnd) return null;

  const body = inner.closest<HTMLElement>(".den-chat-stream-body");
  if (!body) return null;
  // Both boxes belong to the same scrolled layout. WebKit can report a newer
  // scrollTop than its client rects; mixing them makes the tail chase itself.
  const contentTop = body.getBoundingClientRect().top;
  const contentEnd = transcriptEnd.getBoundingClientRect().bottom;
  if (!Number.isFinite(contentTop) || !Number.isFinite(contentEnd)) {
    return null;
  }
  const tailClearance = Number.parseFloat(getComputedStyle(body).paddingBottom) || 0;
  return Math.max(
    0,
    contentEnd -
      contentTop +
      tailClearance -
      scrollportClientHeight(streamTarget),
  );
}

/** Scrollable content height, synthetic extent excluded. */
function streamContentHeight(streamTarget: HTMLElement): number {
  const motion = scrollportMotionForViewport(streamTarget);
  if (motion) return motion.extent.contentHeight();
  return streamTarget.scrollHeight;
}

const streamScrollHandlersByTarget = new WeakMap<HTMLElement, Map<() => void, object>>();
const streamScrollBindingCleanupByTarget = new WeakMap<
  HTMLElement,
  () => void
>();
const streamScrollDeliveries = new WeakMap<HTMLElement, () => void>();

function dispatchStreamScroll(streamTarget: HTMLElement): void {
  let dispatch = streamScrollDeliveries.get(streamTarget);
  if (!dispatch) {
    dispatch = () => {
      const handlers = streamScrollHandlersByTarget.get(streamTarget);
      if (!handlers) return;
      const deliver = () => {
        for (const [handler, subscription] of [...handlers]) {
          if (handlers.get(handler) === subscription) handler();
        }
      };
      const motion = scrollportMotionForViewport(streamTarget);
      if (motion) motion.extent.measureGeometry(deliver);
      else deliver();
    };
    streamScrollDeliveries.set(streamTarget, dispatch);
  }
  coalesceStreamScrollFrame(streamTarget, dispatch);
}

/** One coalesced delivery per frame for every scroll observer of the stream. */
export function subscribeStreamScroll(
  streamTarget: HTMLElement,
  handler: () => void,
): () => void {
  let handlers = streamScrollHandlersByTarget.get(streamTarget);
  if (!handlers) {
    handlers = new Map();
    streamScrollHandlersByTarget.set(streamTarget, handlers);
    streamScrollBindingCleanupByTarget.set(
      streamTarget,
      observeScrollportOffset(streamTarget, () => dispatchStreamScroll(streamTarget)),
    );
  }
  const subscription = handlers.get(handler) ?? {};
  handlers.set(handler, subscription);
  return () => {
    if (handlers?.get(handler) !== subscription) return;
    handlers.delete(handler);
    if (handlers.size !== 0) return;
    streamScrollBindingCleanupByTarget.get(streamTarget)?.();
    streamScrollBindingCleanupByTarget.delete(streamTarget);
    streamScrollHandlersByTarget.delete(streamTarget);
    const delivery = streamScrollDeliveries.get(streamTarget);
    if (delivery) cancelScrollportFrame(streamTarget, delivery);
    streamScrollDeliveries.delete(streamTarget);
  };
}

export type StreamScrollEdges = {
  fadeTop: boolean;
  fadeBottom: boolean;
};

/** The rows element, or the stream itself before rows mount. */
export function streamTranscriptEl(streamTarget: HTMLElement): HTMLElement {
  const body = streamTarget.querySelector(".den-chat-stream-body");
  if (body instanceof HTMLElement) return body;
  return streamTarget;
}

export function syncStreamScrollbarLayout(streamTarget: HTMLElement | undefined): void {
  if (!streamTarget) return;
  const motion = scrollportMotionForViewport(streamTarget);
  if (!motion) return;
  // Signed updates skip the forced measurement during per-frame reconciliation.
  const { clientWidth, clientHeight, scrollWidth, scrollHeight } = streamTarget;
  updateThemedViewportScrollbar(
    motion.host,
    `${clientWidth}x${clientHeight}:${scrollWidth}x${scrollHeight}`,
  );
}

const TAB_PANEL_INSET_ATTR = "data-tab-panel-inset";

type StreamTabPanelInsetState = {
  active: boolean;
  insetPx: number;
};

export function readStreamTabPanelInset(
  streamTarget: HTMLElement,
): StreamTabPanelInsetState {
  const body = streamTranscriptEl(streamTarget);
  const raw = body.getAttribute(TAB_PANEL_INSET_ATTR);
  const insetPx = raw ? Number.parseInt(raw, 10) : 0;
  return {
    active: Number.isFinite(insetPx) && insetPx > 0,
    insetPx: Number.isFinite(insetPx) ? insetPx : 0,
  };
}

function streamNaturalScrollMaxTop(streamTarget: HTMLElement): number {
  const { insetPx } = readStreamTabPanelInset(streamTarget);
  return Math.max(0, streamTarget.scrollHeight - insetPx - streamTarget.clientHeight);
}

/** Returns the missing scroll range needed to clear the panel. */
function requiredStreamTabPanelInset(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): number {
  if (panelHeightPx <= 0) return 0;
  return Math.max(
    0,
    Math.ceil(panelHeightPx - streamNaturalScrollMaxTop(streamTarget)),
  );
}

export function streamTabPanelOverlayFits(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): boolean {
  return requiredStreamTabPanelInset(streamTarget, panelHeightPx) === 0;
}

function streamIsPastFirstViewport(streamTarget: HTMLElement): boolean {
  return streamTarget.scrollTop >= streamTarget.clientHeight - STREAM_SCROLL_TOP_THRESHOLD_PX;
}

function writeStreamTabPanelInset(
  streamTarget: HTMLElement,
  insetPx: number,
): void {
  const body = streamTranscriptEl(streamTarget);
  if (insetPx <= 0) {
    body.style.paddingTop = "";
    body.removeAttribute(TAB_PANEL_INSET_ATTR);
  } else {
    body.style.paddingTop = `${insetPx}px`;
    body.setAttribute(TAB_PANEL_INSET_ATTR, String(insetPx));
  }
}

export function clearStreamTabPanelInset(
  streamTarget: HTMLElement,
  opts?: { resetScroll?: boolean },
): void {
  const { active, insetPx } = readStreamTabPanelInset(streamTarget);
  if (!active) return;
  const scrollTop = streamTarget.scrollTop;
  writeStreamTabPanelInset(streamTarget, 0);
  syncStreamScrollbarLayout(streamTarget);
  if (
    opts?.resetScroll !== false &&
    scrollTop > 0 &&
    Math.abs(scrollTop - insetPx) <= STREAM_SCROLL_TOP_THRESHOLD_PX
  ) {
    commitStreamScroll(streamTarget, 0, "tab_inset");
  }
}

/** Adds the missing scroll range below the panel. */
export function applyStreamTabPanelInset(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): void {
  const required = requiredStreamTabPanelInset(streamTarget, panelHeightPx);
  const previous = readStreamTabPanelInset(streamTarget);
  if (required <= 0) {
    if (previous.active) clearStreamTabPanelInset(streamTarget);
    return;
  }
  if (previous.insetPx === required) return;
  writeStreamTabPanelInset(streamTarget, required);
  syncStreamScrollbarLayout(streamTarget);
}

function reconcileStreamTabPanelInset(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): void {
  const required = requiredStreamTabPanelInset(streamTarget, panelHeightPx);
  const { active, insetPx } = readStreamTabPanelInset(streamTarget);
  // Taking runway back moves content under a reader still inside it.
  if (required < insetPx && !streamIsPastFirstViewport(streamTarget)) return;
  if (required <= 0) {
    if (active) clearStreamTabPanelInset(streamTarget, { resetScroll: false });
    return;
  }
  applyStreamTabPanelInset(streamTarget, panelHeightPx);
}

export type StreamTabPanelRetractOptions = {
  tabOpen: () => boolean;
  retracted: () => boolean;
  manualOpen?: () => boolean;
  onRetractedChange: (retracted: boolean) => void;
};

/** Project panel retraction from the canonical scroll offset. */
export function watchStreamTabPanelRetract(
  streamTarget: HTMLElement,
  opts: StreamTabPanelRetractOptions,
): () => void {
  const sync = () => {
    const atTop = isStreamNearTop(streamTarget);
    const manualOpen = opts.manualOpen?.() ?? false;
    const next = opts.tabOpen() && !manualOpen && atTop;
    const retracted = opts.retracted();
    if (next === retracted) return;
    denScrollDebugLog("scroll", "tab-panel-retraction", {
      scrollTop: Math.round(streamTarget.scrollTop),
      clientHeight: streamTarget.clientHeight,
      retracted,
      next,
      manualOpen,
      overflowAnchor: streamTarget.style.overflowAnchor,
    });
    opts.onRetractedChange(next);
  };

  const unsubscribe = subscribeStreamScroll(streamTarget, sync);
  sync();

  return unsubscribe;
}

export type StreamTabPanelInsetWatchOptions = {
  tabOpen: () => boolean;
  retracted: () => boolean;
  panelHeightPx: () => number;
};

export type StreamGeometryWatchOptions = StreamTabPanelInsetWatchOptions & {
  /** Runs inside the resize observation, before paint. */
  onGeometryChanged: (originChanged: boolean) => void;
};

/** Removes the synthetic runway once transcript content supplies enough room. */
export function watchStreamTabPanelInset(
  streamTarget: HTMLElement,
  opts: StreamTabPanelInsetWatchOptions,
): () => void {
  const sync = () => {
    if (isStreamScrollEngagementSuppressed(streamTarget)) return;
    const wrap = streamTarget.closest(".den-chat-stream-wrap");
    if (wrap?.classList.contains("den-chat-stream-wrap--scrolling")) return;
    if (!opts.tabOpen() || opts.retracted()) return;
    const height = opts.panelHeightPx();
    if (height <= 0) return;
    const inset = readStreamTabPanelInset(streamTarget);
    if (!inset.active && streamTabPanelOverlayFits(streamTarget, height)) return;
    reconcileStreamTabPanelInset(streamTarget, height);
  };
  return subscribeStreamScroll(streamTarget, sync);
}

/** Geometry reports precede paint; tab runway reconciliation follows next frame. */
export function watchStreamGeometry(
  streamTarget: HTMLElement,
  opts: StreamGeometryWatchOptions,
): () => void {
  let frame: number | undefined;
  const scheduleInset = () => {
    if (frame !== undefined) return;
    frame = requestAnimationFrame(() => {
      frame = undefined;
      if (
        !isStreamScrollEngagementSuppressed(streamTarget) &&
        opts.tabOpen() &&
        !opts.retracted()
      ) {
        const height = opts.panelHeightPx();
        if (height > 0) reconcileStreamTabPanelInset(streamTarget, height);
      }
    });
  };
  const paddingTop = new Map<Element, number>();
  const observer = new ResizeObserver((entries) => {
    let originChanged = false;
    for (const entry of entries) {
      const top = entry.contentRect.y;
      if (paddingTop.get(entry.target) === top) continue;
      paddingTop.set(entry.target, top);
      originChanged = true;
    }
    opts.onGeometryChanged(originChanged);
    scheduleInset();
  });
  observer.observe(streamTarget);
  const body = streamTranscriptEl(streamTarget);
  // Body padding contributes to the tail even when its content box is unchanged.
  if (body !== streamTarget) observer.observe(body, { box: "border-box" });
  return () => {
    observer.disconnect();
    if (frame !== undefined) cancelAnimationFrame(frame);
  };
}

/** Uses the shared overflow sample when available to avoid another layout read. */
export function streamHasVerticalOverflow(streamTarget: HTMLElement): boolean {
  const cached = streamEngagement(streamTarget).cachedVerticalOverflow;
  if (cached !== undefined) return cached;
  // Content overflow excludes held scroll ranges.
  return measureStreamOverflow(streamTarget);
}

export function streamScrollEdges(streamTarget: HTMLElement): StreamScrollEdges {
  const overflow = measureStreamOverflow(streamTarget);
  streamEngagement(streamTarget).cachedVerticalOverflow = overflow;
  return {
    // Top fade only when content can scroll under the header.
    fadeTop: overflow && !isStreamNearTop(streamTarget),
    // Bottom fade for the full overflow range; body padding clears the tail band.
    fadeBottom: overflow,
  };
}

export function syncStreamScrollFade(streamTarget: HTMLElement | undefined): void {
  if (!streamTarget) return;
  const wrap = streamWrapForFade(streamTarget);
  if (!wrap) return;
  const { fadeTop, fadeBottom } = streamScrollEdges(streamTarget);
  if (wrap.hasAttribute("data-fade-top") !== fadeTop) {
    wrap.toggleAttribute("data-fade-top", fadeTop);
  }
  if (wrap.hasAttribute("data-fade-bottom") !== fadeBottom) {
    wrap.toggleAttribute("data-fade-bottom", fadeBottom);
  }
  releaseStreamScrollFadeQuiet(wrap);
}

/** Coalesced {@link syncStreamScrollFade} — one geometry pass per frame per stream. */
export function scheduleSyncStreamScrollFade(
  streamTarget: HTMLElement | undefined,
): void {
  if (!streamTarget) return;
  coalesceStreamScrollFrame(streamTarget, () =>
    syncStreamScrollFade(streamTarget),
  );
}

export function watchStreamScrollFade(streamTarget: HTMLElement): () => void {
  // Suppress unstable geometry during viewport attachment.
  suppressStreamScrollFade(streamTarget, 320);

  const syncFade = () => {
    if (isShellLayoutUnstable()) return;
    syncStreamScrollFade(streamTarget);
  };

  // Coalesce scroll + resize into one layout pass per frame.
  const bumpFade = () => coalesceStreamScrollFrame(streamTarget, syncFade);

  const unsubscribe = subscribeStreamScroll(streamTarget, bumpFade);
  bumpFade();

  const ro = new ResizeObserver(bumpFade);
  ro.observe(streamTarget);

  const stopSettle = onShellLayoutSettled(() => {
    syncStreamScrollFade(streamTarget);
    syncStreamScrollbarLayout(streamTarget);
  });

  const releaseTimer = window.setTimeout(() => {
    const wrap = streamWrapForFade(streamTarget);
    if (wrap) releaseStreamScrollFadeQuiet(wrap);
    syncStreamScrollFade(streamTarget);
  }, 320);

  return () => {
    window.clearTimeout(releaseTimer);
    unsubscribe();
    ro.disconnect();
    stopSettle();
  };
}

export function isStreamNearBottom(
  streamTarget: HTMLElement,
  threshold = STREAM_SCROLL_BOTTOM_THRESHOLD_PX,
): boolean {
  return streamTailOffset(streamTarget) - streamTarget.scrollTop <= threshold;
}
