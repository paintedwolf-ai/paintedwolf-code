import { cancelScrollportFrame, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import type { TranscriptRevealAnchor } from "../transcript/presentation/transcript-reveal-target.ts";
import type { TranscriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";
import type { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import { transcriptItemContainsMessage, transcriptItemToolCallIds } from "../transcript/projection/transcript-item-anchors.ts";
import { commitStreamScroll, streamTailOffset } from "./stream-scroll.ts";
type EnsureVisibleOptions = { align?: "start" | "center" | "end" | "nearest"; smooth?: boolean };
type RevealOptions = { align?: "start" | "center" | "end" | "auto"; signal?: AbortSignal; element?: () => HTMLElement | null };
type NavigationViewport = {
  items(): readonly TranscriptItem[];
  ensureAnchorLoaded(anchor: TranscriptRevealAnchor): Promise<void>;
  scrollToIndex(index: number, opts?: { align?: "start" | "center" | "end" | "auto" }): void;
  scrollToOffset(offset: number, opts: { glide: boolean }): void;
};
type NavigationPorts = {
  stream(): HTMLElement | null;
  view(): NavigationViewport | null;
  generation(): number;
  stopFollowing(): void;
  acquireDisclosure(key: TranscriptDisclosureKey, lease: symbol): () => void;
};
const REVEAL_SETTLE_TIMEOUT_MS = 6_000;

function resolveRevealIndex(
  items: readonly TranscriptItem[],
  anchor: TranscriptRevealAnchor,
): number {
  const id = anchor.anchorId.trim();
  if (!id) return -1;
  if (anchor.chicklet === "tool") {
    const actionIndex = items.findIndex((item) =>
      item.kind !== "file_edit" && item.kind !== "worker_file_edit" &&
      transcriptItemToolCallIds(item)?.split(" ").includes(id),
    );
    if (actionIndex >= 0) return actionIndex;
    return items.findIndex((item) => transcriptItemToolCallIds(item)?.split(" ").includes(id));
  }
  return items.findIndex((item) => transcriptItemContainsMessage(item, id));
}

type StreamVisibleBounds = {
  top: number;
  bottom: number;
};

/** The header and its expanded tab panel may paint over the scrollport. */
function streamVisibleBounds(stream: HTMLElement): StreamVisibleBounds {
  const viewport = stream.getBoundingClientRect();
  const style = getComputedStyle(stream);
  const insetBounds = (top: number): StreamVisibleBounds => ({
    top: Math.min(viewport.bottom, top + (Number.parseFloat(style.scrollPaddingTop) || 0)),
    bottom: Math.max(top, viewport.bottom - (Number.parseFloat(style.scrollPaddingBottom) || 0)),
  });
  const stage = stream.closest<HTMLElement>(".den-shell-stage--chat");
  const header = stage?.querySelector<HTMLElement>(".den-shell-header-chat");
  if (!header) return insetBounds(viewport.top);

  const blockers = [
    header,
    ...header.querySelectorAll<HTMLElement>(".tabs__panel-clip"),
  ]
    .map((element) => element.getBoundingClientRect())
    .filter(
      (rect) =>
        rect.bottom > viewport.top &&
        rect.top < viewport.bottom &&
        rect.right > viewport.left &&
        rect.left < viewport.right,
    )
    .sort((a, b) => a.top - b.top);

  let top = viewport.top;
  for (const blocker of blockers) {
    // Only a contiguous overlay at the top narrows the usable viewport.
    if (blocker.top > top + 0.5 || blocker.bottom <= top) continue;
    top = Math.min(viewport.bottom, Math.max(top, blocker.bottom));
  }
  return insetBounds(top);
}

export function createTranscriptNavigation(ports: NavigationPorts) {
  let revealSequence = 0;
  let cancelElementReveal: (() => void) | undefined;
  let navigationDisclosureReleases: Array<() => void> = [];
  const scheduleElementReveal = (element: HTMLElement, ensureOpts: EnsureVisibleOptions = {}, isCurrent: () => boolean = () => true): Promise<boolean> => {
    cancelElementReveal?.();
    const target = ports.stream();
    if (!target || !element.isConnected) return Promise.resolve(false);
    const generation = ports.generation();
    return new Promise((resolve) => {
      let destination: number | undefined;
      const cancel = () => {
        cancelScrollportFrame(target, read);
        cancelScrollportFrame(target, commit);
        if (cancelElementReveal === cancel) cancelElementReveal = undefined;
        resolve(false);
      };
      const valid = () => isCurrent() && target === ports.stream() && generation === ports.generation() && element.isConnected;
      const read = () => {
        if (!valid()) { cancel(); return; }
        const viewport = streamVisibleBounds(target);
        const rect = element.getBoundingClientRect();
        const align = ensureOpts.align ?? "nearest";
        let delta = 0;
        if (align === "center") {
          delta =
            rect.top -
            viewport.top -
            (viewport.bottom - viewport.top - rect.height) / 2;
        } else if (align === "end") {
          delta = rect.bottom - viewport.bottom;
        } else if (align === "start") {
          delta = rect.top - viewport.top;
        } else if (rect.top < viewport.top) {
          delta = rect.top - viewport.top;
        } else if (rect.bottom > viewport.bottom) {
          delta = rect.bottom - viewport.bottom;
        }
        // The virtualizer takes the target even at zero delta, retiring an index scroll.
        destination = Math.min(
          streamTailOffset(target),
          Math.max(0, target.scrollTop + delta),
        );
      };
      const commit = () => {
        if (!valid() || destination === undefined) { cancel(); return; }
        const glide = ensureOpts.smooth !== false;
        const view = ports.view();
        if (view) {
          view.scrollToOffset(destination, { glide });
        } else {
          commitStreamScroll(target, destination, "reveal", { glide });
        }
        if (cancelElementReveal === cancel) cancelElementReveal = undefined;
        resolve(true);
      };
      cancelElementReveal = cancel;
      scheduleScrollportFrame(target, "measure", read);
      scheduleScrollportFrame(target, "render", commit);
    });
  };

  const operations = {
    revealAnchor: async (anchor: TranscriptRevealAnchor, revealOpts: RevealOptions = {}) => {
      const id = anchor.anchorId.trim();
      if (!id || revealOpts.signal?.aborted) return false;
      ports.stopFollowing();
      const view = ports.view();
      if (!view) return false;
      const revealGeneration = ports.generation();
      const sequence = ++revealSequence;
      cancelElementReveal?.();
      await view.ensureAnchorLoaded(anchor);
      if (
        sequence !== revealSequence ||
        ports.generation() !== revealGeneration ||
        ports.view() !== view ||
        revealOpts.signal?.aborted
      ) {
        return false;
      }
      let index = resolveRevealIndex(view.items(), anchor);
      // A walk can select an action whose row is still streaming into the transcript.
      const arrivalDeadline = performance.now() + REVEAL_SETTLE_TIMEOUT_MS;
      while (index < 0 && performance.now() < arrivalDeadline) {
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
        if (sequence !== revealSequence || revealOpts.signal?.aborted || ports.generation() !== revealGeneration || ports.view() !== view) return false;
        index = resolveRevealIndex(view.items(), anchor);
      }
      if (index < 0) return false;
      const align = revealOpts.align ?? "start";
      const needsMount = !revealOpts.element?.()?.isConnected;
      if (needsMount) {
        view.scrollToIndex(index, { align });
      }
      if (revealOpts.element) {
        const deadline = performance.now() + REVEAL_SETTLE_TIMEOUT_MS;
        let element = revealOpts.element();
        let quietFrames = needsMount ? 0 : 3;
        let previousGeometry = "";
        while ((!element?.isConnected || quietFrames < 3) && performance.now() < deadline) {
          await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
          if (sequence !== revealSequence || revealOpts.signal?.aborted || ports.generation() !== revealGeneration || ports.view() !== view) return false;
          element = revealOpts.element();
          const rect = element?.isConnected ? element.getBoundingClientRect() : null;
          const geometry = rect ? `${rect.top}:${rect.height}:${ports.stream()?.scrollTop ?? 0}` : "";
          quietFrames = geometry && geometry === previousGeometry ? quietFrames + 1 : 0;
          previousGeometry = geometry;
        }
        if (!element?.isConnected) return false;
        if (sequence !== revealSequence) return false;
        return scheduleElementReveal(element, { align: align === "auto" ? "nearest" : align, smooth: false }, () => sequence === revealSequence && !revealOpts.signal?.aborted);
      }
      return true;
    },
    setNavigationDisclosures: (keys: readonly TranscriptDisclosureKey[]) => {
      for (const release of navigationDisclosureReleases) release();
      navigationDisclosureReleases = [];
      const lease = Symbol("transcript-navigation");
      for (const key of new Set(keys)) {
        navigationDisclosureReleases.push(ports.acquireDisclosure(key, lease));
      }
    },
    clearNavigationDisclosures: () => {
      revealSequence++;
      cancelElementReveal?.();
      for (const release of navigationDisclosureReleases) release();
      navigationDisclosureReleases = [];
    },
    ensureVisible: (element: HTMLElement, ensureOpts: EnsureVisibleOptions = {}) => {
      revealSequence++;
      ports.stopFollowing();
      void scheduleElementReveal(element, ensureOpts);
    },
  };
  return { ...operations, invalidate: () => { revealSequence++; cancelElementReveal?.(); } };
}
