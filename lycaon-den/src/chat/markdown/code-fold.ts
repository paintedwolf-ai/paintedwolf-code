import type { TranscriptViewportController } from "../stream/transcript-viewport-types.ts";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../transcript/presentation/transcript-disclosure.tsx";
import { findController, refreshFindResults } from "../../find/find-controller.ts";
import { registerFindRevealHost } from "../../find/find-reveal.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";

export const CODE_FOLD_LINE_THRESHOLD = 24;
export const CODE_FOLD_VISIBLE_LINES = 12;

const FADE_CLASS = "den-code-fold-fade";
const TOGGLE_CLASS = "den-code-fold-toggle";

let nextControllerSeq = 0;

/** Counts source lines after stripping at most one trailing newline. */
export function countFenceLines(raw: string): number {
  const trimmed = raw.endsWith("\r\n")
    ? raw.slice(0, -2)
    : raw.endsWith("\n")
      ? raw.slice(0, -1)
      : raw;
  return trimmed.length === 0 ? 0 : trimmed.split("\n").length;
}

type ActiveFenceEntry = {
  frame: HTMLElement;
  lineCount: number;
  fadeEl: HTMLDivElement;
  toggleEl: HTMLButtonElement;
  unregisterHost: () => void;
};

export type MarkdownCodeFoldController = {
  sync: (root: HTMLElement) => void;
  clear: () => void;
  dispose: () => void;
};

function resolveScrollContainer(
  frame: HTMLElement,
  viewport?: TranscriptViewportController | null,
): HTMLElement | null {
  const stream = viewport?.stream();
  if (stream?.isConnected && stream.contains(frame)) return stream;
  for (let el = frame.parentElement; el; el = el.parentElement) {
    if (el.classList.contains("den-chat-stream")) return el;
    if (el.scrollTop > 0 || el.scrollHeight > el.clientHeight) {
      const overflowY = getComputedStyle(el).overflowY;
      if (overflowY === "auto" || overflowY === "scroll" || el.scrollTop > 0) return el;
    }
  }
  return null;
}

function stripFoldChrome(frame: HTMLElement): void {
  delete frame.dataset.codeFold;
  frame.querySelector(`:scope > .${FADE_CLASS}`)?.remove();
  frame.querySelector(`:scope > .${TOGGLE_CLASS}`)?.remove();
}

export function createMarkdownCodeFoldController(
  viewport?: TranscriptViewportController | null,
): MarkdownCodeFoldController {
  const instanceId = `markdown-code-fold:${++nextControllerSeq}`;
  const userExpandedByIndex = new Map<number, boolean>();
  const leasesByIndex = new Map<number, Set<symbol>>();
  const entries = new Map<number, ActiveFenceEntry>();
  const boundIndexByButton = new WeakMap<HTMLButtonElement, number>();
  let disposed = false;

  const isExpanded = (index: number): boolean =>
    userExpandedByIndex.get(index) === true || (leasesByIndex.get(index)?.size ?? 0) > 0;

  const applyEntryDom = (index: number, entry: ActiveFenceEntry): void => {
    const expanded = isExpanded(index);
    entry.frame.dataset.codeFold = expanded ? "open" : "folded";
    const label = expanded ? "Show less" : `Show all ${entry.lineCount} lines`;
    const ariaLabel = expanded ? `Show less (${entry.lineCount} lines)` : label;
    if (entry.toggleEl.textContent !== label) entry.toggleEl.textContent = label;
    entry.toggleEl.setAttribute("aria-label", ariaLabel);
    entry.toggleEl.setAttribute("aria-expanded", expanded ? "true" : "false");
  };

  const toggleFence = (button: HTMLButtonElement): void => {
    const index = boundIndexByButton.get(button);
    if (index === undefined || disposed) return;
    const entry = entries.get(index);
    if (!entry) return;

    const nextUserExpanded = !isExpanded(index);
    if (!nextUserExpanded) leasesByIndex.get(index)?.clear();

    const scroller = resolveScrollContainer(entry.frame, viewport);
    const finishMotion = viewport?.beginDisclosureMotion(
      undefined,
      nextUserExpanded ? "open" : "close",
    );
    const scrollTopBefore =
      (scroller && scrollportMotionForViewport(scroller)?.offsetY()) ?? scroller?.scrollTop ?? 0;
    const scrollerRect = scroller?.getBoundingClientRect();
    const frameRectBefore = entry.frame.getBoundingClientRect();
    const buttonTopBefore = entry.toggleEl.getBoundingClientRect().top;
    const row = entry.frame.closest<HTMLElement>(".transcript-viewport-row");
    const measuredBefore = row?.offsetHeight || entry.frame.offsetHeight;
    const hasRectLayout = frameRectBefore.height > 0 && scrollerRect !== undefined;
    const rowVirtualStart =
      row?.dataset.virtualStart !== undefined ? Number(row.dataset.virtualStart) : NaN;
    const frameAboveViewport =
      scroller !== null &&
      (hasRectLayout
        ? frameRectBefore.top < scrollerRect.top
        : Number.isFinite(rowVirtualStart)
          ? rowVirtualStart < scrollTopBefore
          : scrollTopBefore > 0 && entry.frame.offsetTop < scrollTopBefore);

    const anchorSubject = !nextUserExpanded && frameAboveViewport ? entry.toggleEl : entry.frame;
    const holdChange = scroller
      ? scrollportMotionForViewport(scroller)?.declareHeightChange(anchorSubject)
      : undefined;

    userExpandedByIndex.set(index, nextUserExpanded);
    applyEntryDom(index, entry);

    if (!nextUserExpanded && scroller && frameAboveViewport) {
      const drift = hasRectLayout
        ? entry.toggleEl.getBoundingClientRect().top - buttonTopBefore
        : (row?.offsetHeight || entry.frame.offsetHeight) - measuredBefore;
      if (drift < 0) {
        if (viewport && viewport.stream() === scroller) {
          viewport.shiftVirtualContent(drift, scrollTopBefore);
        } else {
          scroller.scrollTop = Math.max(0, scrollTopBefore + drift);
        }
      }
    }

    entry.frame.dispatchEvent(new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }));
    holdChange?.finish();
    finishMotion?.();
    if (findController.isOpen() && findController.query()) refreshFindResults();
  };

  const ensureFoldChrome = (
    frame: HTMLElement,
    index: number,
  ): { fadeEl: HTMLDivElement; toggleEl: HTMLButtonElement } => {
    let fadeEl = frame.querySelector<HTMLDivElement>(`:scope > .${FADE_CLASS}`);
    if (!fadeEl) {
      fadeEl = document.createElement("div");
      fadeEl.className = FADE_CLASS;
      fadeEl.setAttribute("aria-hidden", "true");
      frame.appendChild(fadeEl);
    }
    let toggleEl = frame.querySelector<HTMLButtonElement>(`:scope > .${TOGGLE_CLASS}`);
    if (!toggleEl) {
      toggleEl = document.createElement("button");
      toggleEl.type = "button";
      toggleEl.className = TOGGLE_CLASS;
      toggleEl.dataset.testid = "code-block-fold-toggle";
      // Exclude fold control copy from in-view Find matches inside the host frame.
      toggleEl.setAttribute("data-den-find-remote", "");
      const btn = toggleEl;
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        toggleFence(btn);
      });
      frame.appendChild(toggleEl);
    }
    boundIndexByButton.set(toggleEl, index);
    return { fadeEl, toggleEl };
  };

  const removeEntry = (index: number, stripDom: boolean): boolean => {
    const existing = entries.get(index);
    if (!existing) return false;
    if (stripDom && existing.frame.isConnected) stripFoldChrome(existing.frame);
    existing.unregisterHost();
    entries.delete(index);
    leasesByIndex.delete(index);
    return true;
  };

  const sync = (root: HTMLElement): void => {
    if (disposed || !root.isConnected) return;
    const pres = Array.from(root.querySelectorAll("pre"));
    let findChanged = false;

    for (const index of Array.from(entries.keys())) {
      if (index >= pres.length && removeEntry(index, true)) findChanged = true;
    }

    for (let index = 0; index < pres.length; index += 1) {
      const pre = pres[index]!;
      const frame = pre.closest<HTMLElement>(".markdown-code-scroll") ?? pre;
      const code = pre.querySelector("code");
      const lineCount = countFenceLines((code ?? pre).textContent ?? "");

      if (lineCount <= CODE_FOLD_LINE_THRESHOLD) {
        stripFoldChrome(frame);
        if (removeEntry(index, false)) findChanged = true;
        continue;
      }

      const { fadeEl, toggleEl } = ensureFoldChrome(frame, index);
      let entry = entries.get(index);
      if (!entry) {
        const unregisterHost = registerFindRevealHost({
          id: `${instanceId}:${index}`,
          hostEl: () => entries.get(index)?.frame ?? null,
          isCollapsed: () => !isExpanded(index),
          revealForFind: () => {
            let leases = leasesByIndex.get(index);
            if (!leases) {
              leases = new Set();
              leasesByIndex.set(index, leases);
            }
            const token = Symbol();
            leases.add(token);
            const active = entries.get(index);
            if (active) {
              applyEntryDom(index, active);
              active.frame.dispatchEvent(
                new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }),
              );
            }
            return () => {
              const currentLeases = leasesByIndex.get(index);
              if (!currentLeases?.delete(token)) return;
              const current = entries.get(index);
              if (current) {
                applyEntryDom(index, current);
                current.frame.dispatchEvent(
                  new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }),
                );
              }
            };
          },
        });
        entry = { frame, lineCount, fadeEl, toggleEl, unregisterHost };
        entries.set(index, entry);
        findChanged = true;
      } else {
        if (entry.frame !== frame || entry.lineCount !== lineCount) findChanged = true;
        entry.frame = frame;
        entry.lineCount = lineCount;
        entry.fadeEl = fadeEl;
        entry.toggleEl = toggleEl;
      }
      applyEntryDom(index, entry);
    }

    if (findChanged && findController.isOpen() && findController.query()) {
      refreshFindResults();
    }
  };

  const clear = (): void => {
    let removed = false;
    for (const index of Array.from(entries.keys())) {
      if (removeEntry(index, true)) removed = true;
    }
    if (removed && findController.isOpen() && findController.query()) {
      refreshFindResults();
    }
  };

  const dispose = (): void => {
    disposed = true;
    for (const index of Array.from(entries.keys())) {
      removeEntry(index, false);
    }
  };

  return { sync, clear, dispose };
}
