import type { TranscriptViewportController } from "../stream/transcript-viewport-types.ts";
import type { TranscriptDisclosureStore } from "../transcript/presentation/disclosure-state.tsx";
import {
  transcriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "../transcript/presentation/transcript-disclosure-key.ts";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../transcript/presentation/transcript-disclosure.tsx";
import { findController, refreshFindResults } from "../../find/find-controller.ts";
import { registerFindRevealHost } from "../../find/find-reveal.ts";
import {
  animateHeightToggle,
  heightToggleDirectionFor,
} from "../../ui/height-toggle-motion.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";

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

export type MarkdownCodeFoldOptions = {
  viewport?: TranscriptViewportController | null;
  disclosures?: TranscriptDisclosureStore | null;
  scope?: () => string | undefined;
};

export type MarkdownCodeFoldController = {
  sync: (root: HTMLElement) => void;
  syncState: () => void;
  clear: () => void;
  dispose: () => void;
};

function isViewportController(
  value: TranscriptViewportController | MarkdownCodeFoldOptions,
): value is TranscriptViewportController {
  return "beginDisclosureMotion" in value;
}

function normalizeOptions(
  input?: TranscriptViewportController | MarkdownCodeFoldOptions | null,
): MarkdownCodeFoldOptions {
  if (!input) return {};
  if (isViewportController(input)) {
    return { viewport: input, disclosures: input.disclosures };
  }
  return {
    viewport: input.viewport,
    disclosures: input.disclosures ?? input.viewport?.disclosures,
    scope: input.scope,
  };
}

function notifyFenceLayout(frame: HTMLElement): void {
  frame.dispatchEvent(new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }));
}

function stripFoldChrome(frame: HTMLElement): void {
  delete frame.dataset.codeFold;
  delete frame.dataset.disclosureKey;
  frame.querySelector(`:scope > .${FADE_CLASS}`)?.remove();
  frame.querySelector(`:scope > .${TOGGLE_CLASS}`)?.remove();
}

function measureCollapsedFenceHeight(entry: ActiveFenceEntry): number {
  const startHeight = entry.frame.getBoundingClientRect().height;
  const view = entry.frame.ownerDocument.defaultView;
  const pre = entry.frame.querySelector("pre") ?? entry.frame;
  const code = pre.querySelector("code") ?? pre;
  const lineHeight = Number.parseFloat(view?.getComputedStyle(code).lineHeight ?? "");
  if (Number.isFinite(lineHeight) && lineHeight > 0) {
    const preStyle = view?.getComputedStyle(pre);
    const paddingTop = Number.parseFloat(preStyle?.paddingTop ?? "") || 0;
    const paddingBottom =
      pre === entry.frame ? Number.parseFloat(preStyle?.paddingBottom ?? "") || 0 : 0;
    const toggleHeight = entry.toggleEl.getBoundingClientRect().height;
    return Math.min(
      startHeight,
      CODE_FOLD_VISIBLE_LINES * lineHeight + paddingTop + paddingBottom + toggleHeight,
    );
  }
  const prev = entry.frame.dataset.codeFold;
  entry.frame.dataset.codeFold = "folded";
  const measured = entry.frame.getBoundingClientRect().height;
  if (prev) entry.frame.dataset.codeFold = prev;
  else delete entry.frame.dataset.codeFold;
  return measured;
}

/** Attaches copy controls and syncs tall-fence folding on sanitized Markdown DOM. */
export function syncMarkdownFenceChrome(
  root: HTMLElement,
  codeFold: MarkdownCodeFoldController,
): void {
  for (const pre of Array.from(root.querySelectorAll("pre"))) {
    // The frame holds the button at the fence's visible corner while the code scrolls.
    const fence = pre.closest(".markdown-code-scroll") ?? pre;
    if (fence.querySelector(":scope > .den-code-copy")) continue;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "den-code-copy";
    button.dataset.testid = "code-block-copy";
    button.setAttribute("data-den-find-remote", "");
    button.textContent = "Copy";
    button.setAttribute("aria-label", "Copy code");
    button.addEventListener("click", (e) => {
      e.stopPropagation();
      const code = pre.querySelector("code");
      void copyTextToClipboard((code ?? pre).textContent ?? "");
    });
    fence.appendChild(button);
  }
  codeFold.sync(root);
}

export function createMarkdownCodeFoldController(
  options?: TranscriptViewportController | MarkdownCodeFoldOptions | null,
): MarkdownCodeFoldController {
  const resolved = normalizeOptions(options);
  const instanceId = `markdown-code-fold:${++nextControllerSeq}`;
  const userExpandedByIndex = new Map<number, boolean>();
  const leasesByIndex = new Map<number, Set<symbol>>();
  const entries = new Map<number, ActiveFenceEntry>();
  const boundIndexByButton = new WeakMap<HTMLButtonElement, number>();
  let disposed = false;

  const disclosureKeyFor = (index: number): TranscriptDisclosureKey | undefined => {
    const scope = resolved.scope?.()?.trim();
    if (!scope) return undefined;
    return transcriptDisclosureKey.codeFence(`${scope}:${index}`);
  };

  const isExpanded = (index: number): boolean => {
    const key = disclosureKeyFor(index);
    if (key && resolved.disclosures) {
      return resolved.disclosures.isOpen(key);
    }
    return userExpandedByIndex.get(index) === true || (leasesByIndex.get(index)?.size ?? 0) > 0;
  };

  const setUserExpanded = (index: number, next: boolean): void => {
    const key = disclosureKeyFor(index);
    if (key && resolved.disclosures) {
      resolved.disclosures.setUserOpen(key, next);
      return;
    }
    if (!next) leasesByIndex.get(index)?.clear();
    userExpandedByIndex.set(index, next);
  };

  const applyEntryDom = (index: number, entry: ActiveFenceEntry): void => {
    const expanded = isExpanded(index);
    const key = disclosureKeyFor(index);
    entry.frame.dataset.codeFold = expanded ? "open" : "folded";
    if (key) entry.frame.dataset.disclosureKey = key;
    else delete entry.frame.dataset.disclosureKey;
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

    const direction = heightToggleDirectionFor(entry.frame, isExpanded(index));
    const key = disclosureKeyFor(index);
    const finishMotion = resolved.viewport?.beginDisclosureMotion(key, direction);

    animateHeightToggle(entry.frame, {
      direction,
      showBody: () => {
        setUserExpanded(index, true);
        applyEntryDom(index, entry);
      },
      hideBody: () => {},
      closeBody: () => {
        setUserExpanded(index, false);
        applyEntryDom(index, entry);
      },
      resetBodyVisibility: () => {},
      collapsedHeight: () => measureCollapsedFenceHeight(entry),
      bodyStillOpen: () => isExpanded(index),
      onSettled: () => {
        notifyFenceLayout(entry.frame);
        finishMotion?.();
        if (findController.isOpen() && findController.query()) refreshFindResults();
      },
    });
    notifyFenceLayout(entry.frame);
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
    if (disposed) return;
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
            const key = disclosureKeyFor(index);
            if (key && resolved.disclosures) {
              const releaseStore = resolved.disclosures.acquire(key);
              const active = entries.get(index);
              if (active) {
                applyEntryDom(index, active);
                notifyFenceLayout(active.frame);
              }
              return () => {
                releaseStore();
                const current = entries.get(index);
                if (current) {
                  applyEntryDom(index, current);
                  notifyFenceLayout(current.frame);
                }
              };
            }
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
              notifyFenceLayout(active.frame);
            }
            return () => {
              const currentLeases = leasesByIndex.get(index);
              if (!currentLeases?.delete(token)) return;
              const current = entries.get(index);
              if (current) {
                applyEntryDom(index, current);
                notifyFenceLayout(current.frame);
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

  const syncState = (): void => {
    if (disposed) return;
    for (const [index, entry] of entries) {
      applyEntryDom(index, entry);
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

  return { sync, syncState, clear, dispose };
}
