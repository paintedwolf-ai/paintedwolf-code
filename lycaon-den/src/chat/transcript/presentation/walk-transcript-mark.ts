import {
  currentWalkStep,
  subscribeWalk,
  walkState,
} from "../../../files/walk/walk-store.ts";
import { transcriptViewportForSession } from "../../stream/transcript-viewport.tsx";
import { visibleTranscriptToolTarget } from "../projection/transcript-item-anchors.ts";

const MARK_CLASS = "den-walk-step-mark";
const STREAM_SELECTOR = ".den-chat-stream";

export function walkTranscriptStreamForSession(
  sessionId: string,
): HTMLElement | null {
  const wanted = sessionId.trim();
  if (!wanted) return null;
  const registered = transcriptViewportForSession(wanted)?.stream();
  if (registered?.isConnected && registered.dataset.sessionId === wanted) return registered;
  const streams = document.querySelectorAll<HTMLElement>(STREAM_SELECTOR);
  for (const stream of streams) {
    if (stream.dataset.sessionId === wanted) return stream;
  }
  return null;
}

export function clearWalkMarks(): void {
  for (const el of document.querySelectorAll(`.${MARK_CLASS}`)) {
    el.classList.remove(MARK_CLASS);
  }
}

export function applyWalkMark(
  root: HTMLElement | null,
  toolCallId: string,
): boolean {
  const el = root ? visibleTranscriptToolTarget(root, toolCallId) : null;
  for (const marked of document.querySelectorAll(`.${MARK_CLASS}`)) {
    if (marked !== el) marked.classList.remove(MARK_CLASS);
  }
  if (!el) return false;
  if (!el.classList.contains(MARK_CLASS)) el.classList.add(MARK_CLASS);
  return true;
}

export function installWalkTranscriptMark(): () => void {
  let revealEpoch = 0;
  let selectedIdentity = "";
  let mountObserver: MutationObserver | undefined;
  let revealAbort: AbortController | undefined;

  const cancelSelection = () => {
    revealEpoch += 1;
    revealAbort?.abort();
    revealAbort = undefined;
    mountObserver?.disconnect();
    mountObserver = undefined;
  };

  const unsubscribe = subscribeWalk((projectId) => {
    const step = currentWalkStep(projectId);
    const sessionId = walkState(projectId).sessionId.trim();
    const identity = `${projectId}\0${sessionId}\0${step?.key ?? ""}`;
    if (identity === selectedIdentity) return;
    selectedIdentity = identity;
    cancelSelection();
    if (!sessionId) {
      clearWalkMarks();
      return;
    }
    const root = walkTranscriptStreamForSession(sessionId);
    if (!step) {
      clearWalkMarks();
      return;
    }
    if (!step.toolCallId) {
      clearWalkMarks();
      return;
    }
    const toolCallId = step.toolCallId;
    const epoch = revealEpoch;
    let pendingDisclosureReveal = false;
    let markedTarget = root ? visibleTranscriptToolTarget(root, toolCallId) : null;
    const paint = () => {
      const currentRoot = walkTranscriptStreamForSession(sessionId);
      markedTarget = currentRoot ? visibleTranscriptToolTarget(currentRoot, toolCallId) : null;
      applyWalkMark(currentRoot, toolCallId);
    };
    const reveal = () => {
      const viewport = transcriptViewportForSession(sessionId);
      if (!viewport) return;
      revealAbort?.abort();
      revealAbort = new AbortController();
      void viewport.revealAnchor(
        { chicklet: "tool", anchorId: toolCallId },
        {
          align: "center",
          signal: revealAbort.signal,
          element: () => {
            const currentRoot = walkTranscriptStreamForSession(sessionId);
            const target = currentRoot ? visibleTranscriptToolTarget(currentRoot, toolCallId) : null;
            return target?.querySelector<HTMLElement>(":scope > summary") ?? target;
          },
        },
      ).then((revealed) => {
        if (revealed && epoch === revealEpoch) paint();
      }).catch(() => undefined);
    };
    paint();
    if (root) {
      mountObserver = new MutationObserver((records) => {
        const previous = markedTarget;
        paint();
        if (markedTarget && previous !== markedTarget && records.some((record) => record.type === "attributes")) {
          pendingDisclosureReveal = true;
        }
        if (pendingDisclosureReveal && markedTarget && !markedTarget.closest('[data-animating="true"]')) {
          pendingDisclosureReveal = false;
          reveal();
        }
      });
      mountObserver.observe(root, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: ["open", "data-animating", "data-closing"],
      });
    }
    reveal();
  });
  return () => {
    cancelSelection();
    unsubscribe();
    clearWalkMarks();
  };
}
