import { matchInRoot } from "../../../find/find-match.ts";
import {
  parseTranscriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "./transcript-disclosure-key.ts";
import { transcriptViewportForSession } from "../../stream/transcript-viewport.tsx";
import { type TranscriptViewportController } from "../../stream/transcript-viewport-types.ts";
import { REVEAL_FLASH_MS, flashRevealTarget } from "../../../ui/reveal-flash.ts";
import { bindRevealHighlightCleanup, paintRevealHighlight } from "./reveal-highlight.ts";
import { focusWithoutScroll } from "../../../platform/interaction/focus.ts";
import type {
  TranscriptRevealAnchor,
  TranscriptRevealTarget,
} from "./transcript-reveal-target.ts";
import { transcriptToolTarget } from "../projection/transcript-item-anchors.ts";

const DEFAULT_TIMEOUT_MS = 6000;

type ContainerSource =
  | HTMLElement
  | Document
  | null
  | undefined
  | (() => HTMLElement | Document | null | undefined);

function cssEscapeAttr(value: string): string {
  const cssEscape = (
    globalThis as { CSS?: { escape?: (v: string) => string } }
  ).CSS?.escape;
  return cssEscape ? cssEscape(value) : value.replace(/["\\]/g, "\\$&");
}

function messageRow(root: ParentNode, messageId: string): HTMLElement | null {
  return root.querySelector<HTMLElement>(
    `[data-msg-id="${cssEscapeAttr(messageId)}"]`,
  );
}

function revealElement(
  root: ParentNode,
  anchor: TranscriptRevealAnchor,
): HTMLElement | null {
  if (anchor.chicklet === "tool") {
    return transcriptToolTarget(root, anchor.anchorId);
  }
  const row = messageRow(root, anchor.anchorId);
  if (!row || anchor.chicklet === "message") return row;
  return (
    row.querySelector<HTMLElement>(".den-citation-evidence-chicklet") ?? row
  );
}

function disclosureKeys(element: HTMLElement): TranscriptDisclosureKey[] {
  const keys: TranscriptDisclosureKey[] = [];
  let current: HTMLElement | null = element;
  while (current) {
    const key = parseTranscriptDisclosureKey(current.dataset.disclosureKey);
    if (key) keys.unshift(key);
    current = current.parentElement;
  }
  return keys;
}

function nextPaint(): Promise<void> {
  return new Promise((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  );
}

function waitForRevealElement(
  source: ContainerSource,
  anchor: TranscriptRevealAnchor,
  timeoutMs: number,
): Promise<HTMLElement | null> {
  const resolveRoot = () =>
    typeof source === "function" ? source() : source;
  const immediateRoot = resolveRoot();
  const immediate = immediateRoot ? revealElement(immediateRoot, anchor) : null;
  if (immediate) return Promise.resolve(immediate);

  return new Promise((resolve) => {
    const observed = immediateRoot ?? document;
    let timeout = 0;
    const observer = new MutationObserver(() => {
      const root = resolveRoot();
      const element = root ? revealElement(root, anchor) : null;
      if (!element) return;
      clearTimeout(timeout);
      observer.disconnect();
      resolve(element);
    });
    observer.observe(observed, { subtree: true, childList: true });
    timeout = window.setTimeout(() => {
      observer.disconnect();
      const root = resolveRoot();
      resolve(root ? revealElement(root, anchor) : null);
    }, timeoutMs);
  });
}

/** Align a mounted target without letting the browser choose an ancestor. */
export function alignRevealTarget(
  element: HTMLElement,
  block: ScrollLogicalPosition = "start",
): void {
  const stream = element.closest<HTMLElement>(".den-chat-stream");
  const sessionId = stream?.dataset.sessionId?.trim() ?? "";
  transcriptViewportForSession(sessionId)?.ensureVisible(element, {
    align: block === "nearest" ? "nearest" : block,
    smooth: true,
  });
}

function highlightMatchInRow(
  controller: TranscriptViewportController,
  row: HTMLElement,
  matchText: string | undefined,
): boolean {
  const text = matchText?.trim();
  if (!text) return false;
  const candidates = [
    text,
    ...text.split(/\s+/).sort((a, b) => b.length - a.length),
  ];
  for (const candidate of candidates) {
    if (candidate.length < 2) continue;
    const matches = matchInRoot(row, candidate, false);
    if (matches.length === 0) continue;
    const active = paintRevealHighlight(row, matches);
    if (!active) continue;
    controller.ensureVisible(active, { align: "center", smooth: true });
    return true;
  }
  return false;
}

/** Worker navigation may create its transcript after its data arrives. */
function waitForRevealController(sessionId: string, timeoutMs: number): Promise<TranscriptViewportController | null> {
  const current = transcriptViewportForSession(sessionId);
  if (current) return Promise.resolve(current);
  return new Promise(resolve => {
    const observer = new MutationObserver(() => {
      const controller = transcriptViewportForSession(sessionId);
      if (!controller) return;
      clearTimeout(timeout);
      observer.disconnect();
      resolve(controller);
    });
    observer.observe(document.body, { childList: true, subtree: true });
    const timeout = window.setTimeout(() => {
      observer.disconnect();
      resolve(transcriptViewportForSession(sessionId));
    }, timeoutMs);
  });
}

/** Load, expand, align, and highlight one stable transcript target. */
export async function revealChicklet(
  container: ContainerSource,
  target: TranscriptRevealTarget,
  opts: { timeoutMs?: number; waitForMount?: boolean; isCurrent?: () => boolean } = {},
): Promise<boolean> {
  const anchor = target.anchor;
  if (!anchor?.anchorId.trim()) return false;
  const controller = opts.waitForMount
    ? await waitForRevealController(target.sessionId, opts.timeoutMs ?? DEFAULT_TIMEOUT_MS)
    : transcriptViewportForSession(target.sessionId);
  if (!controller || opts.isCurrent?.() === false) return false;
  const controllerStillTargetsSession = () =>
    opts.isCurrent?.() !== false && controller.sessionId() === target.sessionId &&
    transcriptViewportForSession(target.sessionId) === controller;

  if (!(await controller.revealAnchor(anchor, { align: "start" }))) return false;
  if (!controllerStillTargetsSession()) return false;
  const element = await waitForRevealElement(
    container,
    anchor,
    opts.timeoutMs ?? DEFAULT_TIMEOUT_MS,
  );
  if (!element || !controllerStillTargetsSession()) return false;

  controller.setNavigationDisclosures(disclosureKeys(element));
  await nextPaint();
  if (!controllerStillTargetsSession()) {
    controller.clearNavigationDisclosures();
    return false;
  }

  flashRevealTarget(element);
  const row = element.closest<HTMLElement>("[data-msg-id]") ?? element;
  if (!highlightMatchInRow(controller, row, anchor.matchText)) {
    controller.ensureVisible(element, { align: "start", smooth: true });
    setTimeout(
      () => controller.clearNavigationDisclosures(),
      REVEAL_FLASH_MS,
    );
  } else {
    bindRevealHighlightCleanup(() => controller.clearNavigationDisclosures());
  }
  if (!element.matches("button, a, input, select, textarea, [tabindex]")) {
    element.tabIndex = -1;
  }
  focusWithoutScroll(element);
  return true;
}
